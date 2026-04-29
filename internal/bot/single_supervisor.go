package bot

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"local/internal/svc/internal/action"
	"local/internal/svc/internal/config"
	ct "local/internal/svc/internal/context"
	"local/internal/svc/internal/drop"
	"local/internal/svc/internal/event"
	"local/internal/svc/internal/game"
	"local/internal/svc/internal/gamelib/data"
	"local/internal/svc/internal/gamelib/data/difficulty"
	"local/internal/svc/internal/gamelib/data/item"
	"local/internal/svc/internal/gamelib/data/skill"
	"local/internal/svc/internal/gamelib/data/stat"
	"local/internal/svc/internal/gamelib/memory"
	"local/internal/svc/internal/health"
	"local/internal/svc/internal/presenter"
	"local/internal/svc/internal/run"
	"local/internal/svc/internal/utils"
)

var (
	user32DLL                  = syscall.NewLazyDLL("user32.dll")
	isHungAppWindowFn          = user32DLL.NewProc("IsHungAppWindow")
	getWindowThreadProcessIdFn = user32DLL.NewProc("GetWindowThreadProcessId")
)

// Define a constant for the timeout on menu operations
const menuActionTimeout = 30 * time.Second

// Define constants for the in-game activity monitor
const (
	activityCheckInterval = 15 * time.Second
	maxStuckDuration      = 3 * time.Minute
)

type SinglePlayerSupervisor struct {
	*baseSupervisor
	lastPlayedGame string // tracks the last game we SUCCESSFULLY played in (not just attempted to join)
}

func (s *SinglePlayerSupervisor) GetData() *game.Data {
	return s.bot.ctx.Data
}

func (s *SinglePlayerSupervisor) GetContext() *ct.Context {
	return s.bot.ctx
}

func NewSinglePlayerSupervisor(name string, bot *Bot, statsHandler *StatsHandler) (*SinglePlayerSupervisor, error) {
	bs, err := newBaseSupervisor(bot, name, statsHandler)
	if err != nil {
		return nil, err
	}

	return &SinglePlayerSupervisor{
		baseSupervisor: bs,
	}, nil
}

var ErrUnrecoverableClientState = errors.New("unrecoverable client state, forcing restart")

func summarizeOpenMenus(m data.OpenMenus) string {
	flags := make([]string, 0, 10)
	if m.LoadingScreen {
		flags = append(flags, "loading")
	}
	if m.Inventory {
		flags = append(flags, "inventory")
	}
	if m.Character {
		flags = append(flags, "character")
	}
	if m.SkillTree {
		flags = append(flags, "skilltree")
	}
	if m.Waypoint {
		flags = append(flags, "waypoint")
	}
	if m.Stash {
		flags = append(flags, "stash")
	}
	if m.QuitMenu {
		flags = append(flags, "quit")
	}
	if m.ChatOpen {
		flags = append(flags, "chat")
	}
	if m.Cinematic {
		flags = append(flags, "cinematic")
	}
	if m.MapShown {
		flags = append(flags, "map")
	}
	if len(flags) == 0 {
		return "-"
	}
	return strings.Join(flags, ",")
}

func (s *SinglePlayerSupervisor) startupDebugSnapshot() string {
	ctx := s.bot.ctx
	if ctx == nil || ctx.GameReader == nil || ctx.GameReader.Process == nil {
		return "startup-state unavailable"
	}

	running, exitCode, statusErr := ctx.GameReader.Process.ProcessStatus()
	statusPart := fmt.Sprintf("running=%t", running)
	if statusErr != nil {
		statusPart = fmt.Sprintf("running=? statusErr=%v", statusErr)
	} else if !running {
		statusPart = fmt.Sprintf("running=false exitCode=%#x", exitCode)
	}

	panels := ctx.GameReader.ReadAllPanels()
	visibleRoots := make([]string, 0, 8)
	for name, panel := range panels {
		if panel.PanelEnabled && panel.PanelVisible {
			visibleRoots = append(visibleRoots, name)
		}
	}
	sort.Strings(visibleRoots)
	if len(visibleRoots) > 8 {
		visibleRoots = visibleRoots[:8]
	}

	return fmt.Sprintf(
		"%s rawInGame=%t uiInGame=%t managerInGame=%t charSelect=%t lobby=%t online=%t blocking=%t selected=%q playerID=%d area=%d pos=(%d,%d) menus=%s visibleRoots=%s",
		statusPart,
		ctx.GameReader.InGame(),
		ctx.Data.IsIngame,
		ctx.Manager.InGame(),
		ctx.GameReader.IsInCharacterSelectionScreen(),
		ctx.GameReader.IsInLobby(),
		ctx.GameReader.IsOnline(),
		ctx.GameReader.IsBlocking(),
		ctx.GameReader.GetSelectedCharacterName(),
		ctx.Data.PlayerUnit.ID,
		ctx.Data.PlayerUnit.Area,
		ctx.Data.PlayerUnit.Position.X,
		ctx.Data.PlayerUnit.Position.Y,
		summarizeOpenMenus(ctx.Data.OpenMenus),
		strings.Join(visibleRoots, "|"),
	)
}

func (s *SinglePlayerSupervisor) logStartupState(label string) {
	if s.bot.ctx == nil || s.bot.ctx.Logger == nil {
		return
	}
	s.bot.ctx.Logger.Info(label, slog.String("state", s.startupDebugSnapshot()))
}

func (s *SinglePlayerSupervisor) orderRuns(runs []string) []string {

	// Difficulty is handled inside Manager.NewGame which clicks Play (600,650)
	// then the difficulty button at the correct Y (Normal 311 / Nightmare 355
	// / Hell 403) per manager.go:96-102. Calling changeDifficulty here in
	// orderRuns created a DUPLICATE click sequence with wrong coords that
	// interfered with the proper flow (observed 2026-04-20 12:00+: bot hit
	// Play but then clicked at 640,470 which is BELOW the difficulty panel,
	// so Hell never got selected).
	_ = difficulty.Hell // keep import

	lvl, _ := s.bot.ctx.Data.PlayerUnit.FindStat(stat.Level, 0)

	if s.bot.ctx.CharacterCfg.Game.StopLevelingAt > 0 && lvl.Value >= s.bot.ctx.CharacterCfg.Game.StopLevelingAt {

		s.bot.ctx.Logger.Info("Character level is already high enough, stopping.")

		s.Stop()

		return nil

	}

	return runs

}

func (s *SinglePlayerSupervisor) changeDifficulty(d difficulty.Difficulty) {

	s.bot.ctx.GameReader.GetSelectedCharacterName()

	s.bot.ctx.HID.Click(game.LeftButton, 6, 6)

	utils.Sleep(1000)

	// D2R's Offline Difficulty modal at the default 1280x720 window places
	// the three difficulty buttons on the vertical centre-line (x≈640) at
	// roughly 370/420/470 y. The old 400 y-values were shifted off the
	// buttons on this resolution — user observed "bot nie trafia w
	// difficulty" 2026-04-20 11:50. Adjusted to match the centre column.
	switch d {

	case difficulty.Normal:

		s.bot.ctx.HID.Click(game.LeftButton, 640, 370)

	case difficulty.Nightmare:

		s.bot.ctx.HID.Click(game.LeftButton, 640, 420)

	case difficulty.Hell:

		s.bot.ctx.HID.Click(game.LeftButton, 640, 470)

	}

	utils.Sleep(1000)

	s.bot.ctx.HID.Click(game.LeftButton, 6, 6)

	utils.Sleep(1000)

}

func (s *SinglePlayerSupervisor) shouldSkipKeybindingsForRespec() bool {
	ctx := s.bot.ctx
	if ctx == nil || ctx.CharacterCfg == nil {
		return false
	}
	if _, isLevelingChar := ctx.Char.(ct.LevelingCharacter); isLevelingChar {
		return false
	}

	autoCfg := ctx.CharacterCfg.Character.AutoStatSkill
	if !autoCfg.Enabled || !autoCfg.Respec.Enabled || autoCfg.Respec.Applied {
		return false
	}
	if autoCfg.Respec.TargetLevel == 0 {
		return true
	}

	level, ok := ctx.Data.PlayerUnit.FindStat(stat.Level, 0)
	return ok && level.Value == autoCfg.Respec.TargetLevel
}

// Start will return error if it can be started, otherwise will always return nil
func (s *SinglePlayerSupervisor) Start() error {
	ctx, cancel := context.WithCancel(context.Background())
	s.cancelFn = cancel

	err := s.ensureProcessIsRunningAndPrepare()
	if err != nil {
		return fmt.Errorf("error preparing game: %w", err)
	}

	if err := s.ensureSkillKeyBindingsReady(); err != nil {
		return err
	}

	s.bot.ctx.Logger.Info("breadcrumb: entering ClaudeModeActive check", slog.Bool("active", s.bot.ctx.ClaudeModeActive))
	// CLAUDE MODE: auto-enter game then idle for HTTP-driven tests.
	if s.bot.ctx.ClaudeModeActive {
		s.bot.ctx.Logger.Info("breadcrumb: entered Claude block, installing defer/recover")
		defer func() {
			if r := recover(); r != nil {
				s.bot.ctx.Logger.Error("Claude mode Start() panic recovered",
					slog.Any("panic", r))
			}
		}()
		if s.bot.ctx.ClaudeAttachExisting {
			s.bot.ctx.Logger.Info("Claude mode: attached to existing client; initializing presenter without pre-game probes")
			s.initClaudePresenter()
			s.bot.ctx.SwitchPriority(ct.PriorityPause)
			event.Send(event.GamePaused(event.Text(s.name, "Claude mode active"), true))
			s.bot.ctx.Logger.Info("Claude mode: READY — use /debug/* endpoints")
			for {
				select {
				case <-ctx.Done():
					return nil
				default:
					utils.Sleep(1000)
				}
			}
		}
		s.bot.ctx.Logger.Info("Claude mode: fresh client start; skipping early InGame memory read")
		if false && s.bot.ctx.Manager.InGame() {
			s.bot.ctx.RefreshGameData()
		}
		if false && s.bot.ctx.Manager.InGame() && s.bot.ctx.Data.PlayerUnit.Area != 0 {
			s.bot.ctx.Logger.Info("Claude mode: already in game, skipping entry...")
			s.initClaudePresenter()
			s.bot.ctx.SwitchPriority(ct.PriorityPause)
			event.Send(event.GamePaused(event.Text(s.name, "Claude mode active"), true))
			s.bot.ctx.Logger.Info("Claude mode: READY — use /debug/* endpoints")
			for {
				select {
				case <-ctx.Done():
					return nil
				default:
					utils.Sleep(1000)
				}
			}
		}

		s.bot.ctx.Logger.Info("Claude mode: waiting for D2R to reach char select (30s)...")

		var charSelectReady bool
		for i := 0; i < 120; i++ { // 120 × 250ms = 30s max
			s.bot.ctx.HID.Click(game.LeftButton, 100, 100)
			utils.Sleep(250)
			if s.bot.ctx.GameReader.IsInCharacterSelectionScreen() {
				charSelectReady = true
				break
			}
		}
		if !charSelectReady {
			s.bot.ctx.Logger.Warn("Claude mode: char select not detected after 30s, proceeding anyway...")
		} else {
			s.bot.ctx.Logger.Info("Claude mode: char select detected, creating game...")
		}

		for attempt := 0; attempt < 8; attempt++ {
			s.bot.ctx.Logger.Info("Claude mode: NewGame attempt", "n", attempt+1)
			if attempt%2 == 0 {
				gameErr := s.bot.ctx.Manager.NewGame()
				if gameErr == nil {
					break
				}
				s.bot.ctx.Logger.Warn("Claude mode: mouse NewGame failed, trying Enter", "error", gameErr)
			}
			s.bot.ctx.HID.PressKey(0x0D)
			utils.Sleep(1500)
			s.bot.ctx.HID.PressKey(0x0D)
			utils.Sleep(4000)
			if s.bot.ctx.Manager.InGame() {
				s.bot.ctx.RefreshGameData()
				break
			}
		}

		// Wait until in-game
		for i := 0; i < 50; i++ {
			if s.bot.ctx.Manager.InGame() {
				s.bot.ctx.RefreshGameData()
				if s.bot.ctx.Data.PlayerUnit.Area != 0 {
					break
				}
			}
			utils.Sleep(200)
		}

		if !s.bot.ctx.Manager.InGame() {
			s.bot.ctx.Logger.Error("Claude mode: failed to enter game")
			return fmt.Errorf("claude mode: failed to enter game after retries")
		}

		s.bot.ctx.Logger.Info("Claude mode: IN GAME — packet diagnostics only")
		utils.Sleep(1000)

		if os.Getenv("DISABLE_CLAUDE_PRESENTER") == "1" {
			s.bot.ctx.Logger.Warn("Claude mode: presenter disabled via DISABLE_CLAUDE_PRESENTER=1")
		} else {
			s.bot.ctx.Logger.Info("Claude mode: injecting presenter...")
			s.initClaudePresenter()
		}

		s.bot.ctx.SwitchPriority(ct.PriorityPause)
		event.Send(event.GamePaused(event.Text(s.name, "Claude mode active"), true))
		s.bot.ctx.Logger.Info("Claude mode: READY — use /debug/* endpoints")

		for {
			select {
			case <-ctx.Done():
				return nil
			default:
				utils.Sleep(1000)
			}
		}
	}

	// MANUAL MODE: Early exit - handle before normal game loop
	if s.bot.ctx.ManualModeActive {
		s.bot.ctx.Logger.Info("Manual mode: reaching character selection...")
		if err = s.waitUntilCharacterSelectionScreen(); err != nil {
			return fmt.Errorf("manual mode: error waiting for character selection: %w", err)
		}

		s.bot.ctx.Logger.Info("Manual mode: waiting for window repositioning...")
		time.Sleep(5 * time.Second)

		// Pause/resume cycle to free resources
		s.bot.ctx.Logger.Info("Manual mode: pausing...")
		s.bot.ctx.SwitchPriority(ct.PriorityPause)
		s.bot.ctx.MemoryInjector.RestoreMemory()
		event.Send(event.GamePaused(event.Text(s.name, "Manual mode active"), true))

		time.Sleep(500 * time.Millisecond)

		s.bot.ctx.Logger.Info("Manual mode: resuming...")
		s.bot.ctx.MemoryInjector.Load()
		s.bot.ctx.SwitchPriority(ct.PriorityNormal)
		event.Send(event.GamePaused(event.Text(s.name, "Manual mode ready"), false))

		s.bot.ctx.Logger.Info("Manual mode: initialization complete")

		// Keep process alive until stopped
		for {
			select {
			case <-ctx.Done():
				return nil
			default:
				utils.Sleep(1000)
			}
		}
	}

	// NORMAL MODE: Original code unchanged from here
	firstRun := true
	var timeSpentNotInGameStart = time.Now()
	const maxTimeNotInGame = 300 * time.Second // D2R title-screen→char-select→in-game transition observed at ~3m50s on this VM (2026-04-20 11:50); 180s triggered false-positive restart loops. 300s gives ample slack.

	for {
		// Check if the main context has been cancelled
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		// Check for pending Drop via Drop manager
		if s.bot.ctx.Drop != nil && s.bot.ctx.Drop.Pending() != nil {
			// Skip if Drop is already in progress
			if s.bot.ctx.Drop.Active() != nil {
				s.bot.ctx.Logger.Debug("Drop already in progress, skipping check")
				continue
			}

			// Immediately run the pending Drop before entering the normal menu flow
			s.bot.ctx.Logger.Info("Pending Drop detected, launching Drop before menu flow")
			s.bot.ctx.SwitchPriority(ct.PriorityNormal)
			action.SwitchToLegacyMode()
			action.SwitchToLegacyMode()
			DropRun := run.NewDrop()
			if err := DropRun.Run(nil); err != nil {
				s.bot.ctx.Logger.Error("Drop run failed", "error", err)
			}
			continue
		}

		if firstRun {
			// Companions may end up in lobby after a failed game join — skip waiting
			// for character selection screen if we're already in lobby, HandleMenuFlow
			// will handle it via HandleCompanionMenuFlow.
			if !(s.bot.ctx.CharacterCfg.Companion.Enabled && !s.bot.ctx.CharacterCfg.Companion.Leader && s.bot.ctx.GameReader.IsInLobby()) {
				if err = s.waitUntilCharacterSelectionScreen(); err != nil {
					return fmt.Errorf("error waiting for character selection screen: %w", err)
				}
			}
		}

		// LOGIC OUTSIDE OF GAME (MENUS)
		if !s.bot.ctx.Manager.InGame() {
			// This outer timer is the ultimate watchdog. If the bot is out of game for too long,
			// for any reason (including a frozen state read), this will trigger.
			if time.Since(timeSpentNotInGameStart) > maxTimeNotInGame {
				s.bot.ctx.Logger.Error(fmt.Sprintf("Has been outside of a game for more than %s. Forcing client restart.", maxTimeNotInGame))
				if killErr := s.KillClient(); killErr != nil {
					s.bot.ctx.Logger.Error(fmt.Sprintf("Error killing client after timeout: %s", killErr.Error()))
				}
				return ErrUnrecoverableClientState
			}

			// We execute the menu handling in a goroutine so we can timeout the whole process
			// if it gets stuck reading game state. Use a context so the goroutine can observe
			// cancellation on timeout instead of being leaked.
			menuCtx, menuCancel := context.WithTimeout(ctx, maxTimeNotInGame)
			errChan := make(chan error, 1)
			go func() {
				errChan <- s.HandleMenuFlow()
			}()

			select {
			case err := <-errChan:
				menuCancel()
				// Menu flow finished (or returned an error) before the timeout.
				if err != nil {
					if errors.Is(err, ErrUnrecoverableClientState) {
						s.bot.ctx.Logger.Error(fmt.Sprintf("Unrecoverable client state detected: %s. Forcing client restart.", err.Error()))
						return err
					}
					if err.Error() == "loading screen" || err.Error() == "" || err.Error() == "idle" {
						// Companions legitimately idle in menu waiting for leader's new game —
						// reset watchdog so 45s timeout doesn't kill the client
						if err.Error() == "idle" {
							timeSpentNotInGameStart = time.Now()
						}
						utils.Sleep(100)
						continue
					}
					s.bot.ctx.Logger.Error(fmt.Sprintf("Error during menu flow: %s", err.Error()))
					utils.Sleep(1000)
					continue
				}
			case <-menuCtx.Done():
				menuCancel()
				// The entire HandleMenuFlow function took too long. This means a game state read is likely frozen.
				s.bot.ctx.Logger.Error(fmt.Sprintf("Menu flow frozen for more than %s. Forcing client restart.", maxTimeNotInGame))
				if killErr := s.KillClient(); killErr != nil {
					s.bot.ctx.Logger.Error(fmt.Sprintf("Error killing client after menu flow timeout: %s", killErr.Error()))
				}
				return ErrUnrecoverableClientState
			}
		}

		// In-game logic. A transient/stale InGame() read is not enough here:
		// wait until player data is populated and the loading screen cleared
		// before we announce a new game or enter the run loop.
		if err := s.bot.ctx.WaitForStableInGame(10 * time.Second); err != nil {
			s.bot.ctx.Logger.Error("Post-menu game-entry validation failed",
				slog.Any("error", err),
				slog.Uint64("pid", uint64(s.bot.ctx.GameReader.Process.GetPID())),
				slog.Bool("managerInGame", s.bot.ctx.Manager.InGame()),
				slog.Int("playerID", int(s.bot.ctx.Data.PlayerUnit.ID)),
				slog.Int("area", int(s.bot.ctx.Data.PlayerUnit.Area)))
			if errors.Is(err, ct.ErrGameProcessExited) {
				return ErrUnrecoverableClientState
			}
			timeSpentNotInGameStart = time.Now()
			utils.Sleep(500)
			continue
		}

		timeSpentNotInGameStart = time.Now()
		if s.bot.ctx.HID != nil {
			s.bot.ctx.HID.Disable("post-game-load full-packet run")
			s.bot.ctx.Logger.Info("Game loaded; HID disabled for full-packet run")
		}

		stringRuns := make([]string, len(s.bot.ctx.CharacterCfg.Game.Runs))
		for i, r := range s.bot.ctx.CharacterCfg.Game.Runs {
			stringRuns[i] = string(r)
		}
		orderedRuns := s.orderRuns(stringRuns)
		if orderedRuns == nil {
			return nil
		}
		developmentOnly := len(orderedRuns) == 1 && orderedRuns[0] == string(config.DevelopmentRun)

		runs := run.BuildRuns(s.bot.ctx.CharacterCfg, orderedRuns)
		gameStart := time.Now()
		cfg, _ := config.GetCharacter(s.name)

		if cfg.Game.RandomizeRuns {
			rand.Shuffle(len(runs), func(i, j int) { runs[i], runs[j] = runs[j], runs[i] })
		}

		// Check if this is a rejoin (same game) — skip already completed runs.
		// Offline/single-player has empty game names so every game would match
		// empty == empty and trigger "rejoin" → 0 runs remain → ExitGame loop
		// observed 2026-04-20. Require a non-empty gameID to enable rejoin
		// detection; offline play is treated as a fresh game every time.
		currentGameID := s.bot.ctx.Data.Game.LastGameName
		if currentGameID != "" && s.bot.ctx.CompletedGameID == currentGameID {
			completed := s.bot.ctx.GetCompletedRuns()
			if len(completed) > 0 {
				completedSet := make(map[string]bool, len(completed))
				for _, name := range completed {
					completedSet[name] = true
				}
				var filtered []run.Run
				for _, r := range runs {
					if !completedSet[r.Name()] {
						filtered = append(filtered, r)
					}
				}
				s.bot.ctx.Logger.Info(fmt.Sprintf("Rejoined game %s, skipping %d already completed runs, %d remaining", currentGameID, len(completed), len(filtered)))
				runs = filtered
				if len(runs) == 0 {
					s.bot.ctx.Logger.Info("All runs already completed in this game")
					s.lastPlayedGame = currentGameID

					// Instead of exiting immediately, do bonus runs while waiting for leader's new game
					partyWaitEnabled := s.bot.ctx.CharacterCfg.Companion.Enabled && s.bot.ctx.CharacterCfg.Companion.WaitForParty
					if partyWaitEnabled {
						pr := GetPartyRegistry()
						pr.RegisterMember(s.name, currentGameID)
						pr.MarkDone(s.name)

						if s.bot.ctx.CharacterCfg.Companion.BonusRuns {
							s.bot.ctx.Logger.Info("Party: doing bonus runs while waiting (rejoin with 0 runs left)")
							if needsChicken := s.doBonusRuns(ctx, pr); needsChicken {
								s.bot.ctx.Logger.Warn("Party: chicken during bonus run after rejoin")
							}
						}
						// Wait for leader to start new game
						s.waitForPartyMembers(ctx)
					}

					// Reset completed runs before exiting so the next game
					// (even if D2R reuses the same empty game name for offline
					// play) doesn't re-trigger "all runs completed" → ExitGame
					// → rejoin loop observed 2026-04-20.
					s.bot.ctx.ResetCompletedRuns("")
					s.bot.ctx.Manager.ExitGame()
					utils.Sleep(3000)
					timeSpentNotInGameStart = time.Now()
					continue
				}
			}
		} else {
			s.bot.ctx.ResetCompletedRuns(currentGameID)
		}

		event.Send(event.GameCreated(event.Text(s.name, "New game created"), s.bot.ctx.Data.Game.LastGameName, s.bot.ctx.Data.Game.LastGamePassword))
		s.bot.ctx.FailedToCreateGameAttempts = 0
		s.bot.ctx.LastBuffAt = time.Time{}
		s.bot.ctx.WeaponCacheReady = false // Force re-probe of weapon sets each new game
		s.logGameStart(runs)
		s.bot.ctx.RefreshGameData()

		// Register in party registry if WaitForParty or LeaderPriorityRuns is enabled
		partyRegistryEnabled := s.bot.ctx.CharacterCfg.Companion.Enabled &&
			(s.bot.ctx.CharacterCfg.Companion.WaitForParty || s.bot.ctx.CharacterCfg.Companion.LeaderPriorityRuns)
		if partyRegistryEnabled {
			gameID := s.bot.ctx.Data.Game.LastGameName
			GetPartyRegistry().RegisterMember(s.name, gameID)
			runNames := make([]string, len(s.bot.ctx.CharacterCfg.Game.Runs))
			for i, r := range s.bot.ctx.CharacterCfg.Game.Runs {
				runNames[i] = string(r)
			}
			GetPartyRegistry().SetMemberRuns(s.name, runNames)
		}

		// Dump armory data on game start
		if err := s.dumpArmory(); err != nil {
			s.bot.ctx.Logger.Warn("Failed to dump armory data", slog.Any("error", err))
		}

		if s.bot.ctx.Data.IsLevelingCharacter && s.bot.ctx.Data.ActiveWeaponSlot != 0 {
			for attempt := 0; attempt < 3 && s.bot.ctx.Data.ActiveWeaponSlot != 0; attempt++ {
				// Full-packet bot: always emit 0x50; no HID fallback (user 2026-04-19).
				if s.bot.ctx.PacketSender != nil {
					if err := s.bot.ctx.PacketSender.SwapWeaponFromData(s.bot.ctx.Data); err != nil {
						s.bot.ctx.Logger.Warn("supervisor SwapWeapon packet failed", "err", err)
					}
				}
				utils.PingSleep(utils.Light, 150)
				s.bot.ctx.RefreshGameData()
			}
			if s.bot.ctx.Data.ActiveWeaponSlot != 0 {
				s.bot.ctx.Logger.Warn("Failed to return to main weapon slot after game start", "slot", s.bot.ctx.Data.ActiveWeaponSlot)
			}
		}

		// Normal-mode presenter is injected only after successful game entry.
		// Injecting during title/char-select breaks menu navigation. Post-game
		// timing matches the external full-packet bot model: game is stable,
		// then the same presenter wiring used by Claude-mode packet tests is
		// attached before production flow starts.
		if os.Getenv("DISABLE_CLAUDE_PRESENTER") != "1" &&
			os.Getenv("DISABLE_NORMAL_PRESENTER") != "1" &&
			(s.bot.ctx.MemoryInjector == nil || s.bot.ctx.MemoryInjector.GetPresenter() == nil) {
			s.bot.ctx.Logger.Info("Normal mode: activating Claude-equivalent presenter after game entry")
			s.initClaudePresenter()
		}

		if s.bot.ctx.CharacterCfg.Companion.Enabled && s.bot.ctx.CharacterCfg.Companion.Leader {
			event.Send(event.RequestCompanionJoinGame(event.Text(s.name, "New Game Started "+s.bot.ctx.Data.Game.LastGameName), s.bot.ctx.CharacterCfg.CharacterName, s.bot.ctx.Data.Game.LastGameName, s.bot.ctx.Data.Game.LastGamePassword))
			// Store game info in party registry so companions can rejoin after crash/chicken
			if s.bot.ctx.CharacterCfg.Companion.WaitForParty || s.bot.ctx.CharacterCfg.Companion.LeaderPriorityRuns {
				GetPartyRegistry().SetActiveGame(
					s.bot.ctx.Data.Game.LastGameName,
					s.bot.ctx.Data.Game.LastGamePassword,
					s.bot.ctx.CharacterCfg.CharacterName,
				)
			}
		}

		if firstRun {
			if s.shouldSkipKeybindingsForRespec() {
				s.bot.ctx.Logger.Info("Auto respec pending; skipping keybinding check for this run")
			} else {
				missingKeybindings := s.bot.ctx.Char.CheckKeyBindings()
				if len(missingKeybindings) > 0 {
					var missingKeybindingsText = "Missing key binding for skill(s):"
					for _, v := range missingKeybindings {
						missingKeybindingsText += fmt.Sprintf("\n%s", skill.SkillNames[v])
					}
					missingKeybindingsText += "\nPlease bind the skills. Pausing..."

					utils.ShowDialog("Missing keybindings for "+s.name, missingKeybindingsText)
					s.TogglePause()
				}
			}
		}

		// Context with a timeout for the game itself
		runCtx := ctx
		var runCancel context.CancelFunc
		if s.bot.ctx.CharacterCfg.MaxGameLength > 0 {
			runCtx, runCancel = context.WithTimeout(ctx, time.Duration(s.bot.ctx.CharacterCfg.MaxGameLength)*time.Second)
		} else {
			runCtx, runCancel = context.WithCancel(ctx)
		}
		// NOTE: runCancel() is called explicitly at each loop exit point
		// instead of using defer, which would leak contexts in a loop.

		// Initialize ping monitor for this game session
		// Configuration from settings.yaml (default: quit after 30s of ping > 500ms)
		pingThreshold := 500
		sustainedDuration := 30 * time.Second
		pingEnabled := false

		if config.App.PingMonitor.Enabled {
			pingEnabled = true
			if config.App.PingMonitor.HighPingThreshold > 0 {
				pingThreshold = config.App.PingMonitor.HighPingThreshold
			}
			if config.App.PingMonitor.SustainedDuration > 0 {
				sustainedDuration = time.Duration(config.App.PingMonitor.SustainedDuration) * time.Second
			}
		}

		pingMonitor := health.NewPingMonitor(
			s.bot.ctx.Logger,
			pingThreshold,
			sustainedDuration,
		)
		pingMonitor.Enabled = pingEnabled
		pingMonitor.SetCallback(func() {
			s.bot.ctx.Logger.Error("Sustained high ping detected. Forcing game exit.",
				slog.Int("threshold", pingThreshold),
				slog.Duration("duration", sustainedDuration))
			runCancel()
		})

		// Fast party monitor — checks leader signals every 2s for quick follower response
		if s.bot.ctx.CharacterCfg.Companion.Enabled && !s.bot.ctx.CharacterCfg.Companion.Leader {
			go func() {
				partyTicker := time.NewTicker(2 * time.Second)
				defer partyTicker.Stop()
				for {
					select {
					case <-runCtx.Done():
						return
					case <-partyTicker.C:
						if !s.bot.ctx.GameReader.InGame() || s.bot.ctx.Data.PlayerUnit.ID == 0 {
							continue
						}
						if s.bot.ctx.WaitingForParty.Load() {
							continue
						}
						if s.bot.ctx.CharacterCfg.Companion.WaitForParty && GetPartyRegistry().GameAborted() {
							s.bot.ctx.Logger.Info("Party: leader aborted game, cancelling current run to resync")
							runCancel()
							return
						}
						if GetPartyRegistry().IsLeaderDone() {
							s.bot.ctx.Logger.Info("Party: leader priority mode — leader finished runs, aborting current run to rejoin")
							runCancel()
							return
						}
					}
				}
			}()
		}

		// In-Game Activity Monitor. Development mode intentionally keeps the
		// character idle while HTTP/debug tooling drives experiments.
		if !developmentOnly {
			go func() {
				ticker := time.NewTicker(activityCheckInterval)
				defer ticker.Stop()
				var lastPosition data.Position
				var stuckSince time.Time
				var droppedMouseItem bool // Track if we've already tried dropping mouse item

				// Initial position check
				if s.bot.ctx.GameReader.InGame() && s.bot.ctx.Data.PlayerUnit.ID > 0 {
					lastPosition = s.bot.ctx.Data.PlayerUnit.Position
				}

				for {
					select {
					case <-runCtx.Done(): // Exit when the run is over (either completed, errored, or timed out)
						return
					case <-ticker.C:
						if s.bot.ctx.GetPriority() == ct.PriorityPause {
							continue
						}

						if !s.bot.ctx.GameReader.InGame() || s.bot.ctx.Data.PlayerUnit.ID == 0 {
							continue
						}

						// Skip stuck detection while waiting for party members
						if s.bot.ctx.WaitingForParty.Load() {
							stuckSince = time.Time{}
							droppedMouseItem = false
							lastPosition = s.bot.ctx.Data.PlayerUnit.Position
							continue
						}

						// Check for sustained high ping
						if pingMonitor.CheckPing(s.bot.ctx.Data.Game.Ping) {
							s.bot.ctx.Logger.Error("Ping monitor triggered game exit.")
							return
						}

						currentPos := s.bot.ctx.Data.PlayerUnit.Position
						lastAction := s.bot.ctx.ContextDebug[s.bot.ctx.GetPriority()].LastAction
						isAllocating := lastAction == "AutoRespecIfNeeded" ||
							lastAction == "EnsureStatPoints" ||
							lastAction == "EnsureSkillPoints" ||
							lastAction == "EnsureSkillBindings" ||
							lastAction == "AllocateStatPointPacket" ||
							lastAction == "LearnSkillPacket"
						if isAllocating && (s.bot.ctx.Data.OpenMenus.Character || s.bot.ctx.Data.OpenMenus.SkillTree || s.bot.ctx.Data.OpenMenus.Inventory) {
							stuckSince = time.Time{}
							droppedMouseItem = false
							lastPosition = currentPos
							continue
						}
						if currentPos.X == lastPosition.X && currentPos.Y == lastPosition.Y {
							if stuckSince.IsZero() {
								stuckSince = time.Now()
								droppedMouseItem = false // Reset flag when first detecting stuck
							}

							stuckDuration := time.Since(stuckSince)

							// After 90 seconds stuck, try dropping mouse item
							if stuckDuration > 90*time.Second {
								if len(s.bot.ctx.Data.Inventory.ByLocation(item.LocationCursor)) > 0 && !droppedMouseItem {
									s.bot.ctx.Logger.Warn("Player stuck for 90 seconds - dropping mouse item without HID - Continuing to monitor for movement...")
									if s.bot.ctx.HID != nil && s.bot.ctx.HID.IsDisabled() && s.bot.ctx.PacketSender != nil {
										if err := s.bot.ctx.PacketSender.ClickAt(500, 500, game.MouseLeft); err != nil {
											s.bot.ctx.Logger.Warn("Failed to drop mouse item through packet click", slog.Any("error", err))
										}
									} else {
										s.bot.ctx.HID.Click(game.LeftButton, 500, 500)
									}
									droppedMouseItem = true
								} else if s.bot.ctx.IsAllocatingStatsOrSkills.Load() {
									// We don't want a false positive on being stuck when the character is respeccing
									s.bot.ctx.Logger.Debug("Player stuck for 90 seconds - Currently respeccing - letting it continue.")
									stuckSince = time.Now()
								} else if droppedMouseItem {
									s.bot.ctx.Logger.Warn("Player still stuck after dropping the item - Forcing client restart.")
									if err := s.KillClient(); err != nil {
										s.bot.ctx.Logger.Error(fmt.Sprintf("Activity monitor failed to kill client: %v", err))
									}
									runCancel()
									return
								}
							}

							// After 3 minutes stuck, force restart
							if stuckDuration > maxStuckDuration {
								s.bot.ctx.Logger.Error(fmt.Sprintf("In-game activity monitor: Player has been stuck for over %s. Forcing client restart.", maxStuckDuration))
								if err := s.KillClient(); err != nil {
									s.bot.ctx.Logger.Error(fmt.Sprintf("Activity monitor failed to kill client: %v", err))
								}
								runCancel() // Also cancel the context to stop bot.Run gracefully
								return
							}
						} else {
							stuckSince = time.Time{} // Reset timer if the player has moved
							droppedMouseItem = false // Reset flag if player moved
						}
						lastPosition = currentPos
					}
				}
			}()
		}

		if os.Getenv("CODEX_PAUSE_BEFORE_BOT_RUN") == "1" {
			s.bot.ctx.Logger.Warn("Debug pause before bot.Run requested; town/native capture can be performed now")
			s.bot.ctx.SwitchPriority(ct.PriorityPause)
			event.Send(event.GamePaused(event.Text(s.name, "Debug pause before bot.Run"), true))
			for s.bot.ctx.GetPriority() == ct.PriorityPause {
				select {
				case <-runCtx.Done():
					return runCtx.Err()
				default:
					utils.Sleep(500)
				}
			}
			s.bot.ctx.Logger.Info("Debug pause before bot.Run resumed")
		}

		err = s.bot.Run(runCtx, firstRun, runs)
		firstRun = false
		if s.bot.ctx.HID != nil && s.bot.ctx.HID.BlockedCount() > 0 {
			blocked := s.bot.ctx.HID.BlockedCount()
			s.bot.ctx.Logger.Error("Full-packet HID guard violation; run is invalid",
				slog.Uint64("blockedHIDActions", blocked))
			if err == nil {
				err = fmt.Errorf("full-packet HID guard violation: %d blocked HID actions after game load", blocked)
			}
		}

		if err != nil {
			// Track the failed run so rejoin skips it (prevents infinite retry loops)
			if failedRun := s.bot.ctx.CurrentRunName; failedRun != "" {
				s.bot.ctx.AddCompletedRun(failedRun)
				s.bot.ctx.Logger.Info("Marking failed run as completed for skip on rejoin",
					slog.String("run", failedRun),
					slog.String("error", err.Error()))
			}

			if errors.Is(err, drop.ErrInterrupt) {
				s.bot.ctx.Logger.Info("Drop interrupt received. Exiting game and restarting loop.")
				s.bot.ctx.Manager.ExitGame()
				utils.Sleep(2000)
				runCancel()
				continue
			}

			// Party: on death with WaitForParty, stay in game to maintain player count
			// for better XP/loot for remaining party members
			partyWaitEnabled := s.bot.ctx.CharacterCfg.Companion.Enabled && s.bot.ctx.CharacterCfg.Companion.WaitForParty
			isDeath := errors.Is(err, health.ErrDied)

			if partyWaitEnabled && isDeath {
				s.bot.ctx.Logger.Info("Party: died but staying in game to maintain player count for party members")
				event.Send(event.GameFinished(event.WithScreenshot(s.name, err.Error(), s.bot.ctx.GameReader.Screenshot()), event.FinishedDied))

				// Mark self as done so others don't wait for a dead member
				GetPartyRegistry().MarkDone(s.name)

				// Wait for other party members to finish while idling in game
				s.waitForPartyMembers(ctx)

				s.bot.ctx.Logger.Info("Party: all members done after death idle, now exiting game")
			} else {
				if errors.Is(err, context.DeadlineExceeded) {
					// We don't log the generic "run finished with error" message if it was a planned timeout
				} else {
					s.bot.ctx.Logger.Info(fmt.Sprintf("Run finished with error: %s. Initiating game exit and cooldown.", err.Error()))
				}

				// Party: mark done on non-death errors (chicken/timeout) so others don't wait forever
				if partyWaitEnabled {
					GetPartyRegistry().MarkDone(s.name)
					if s.bot.ctx.CharacterCfg.Companion.Leader {
						// Signal all companions to abort their runs and exit the game —
						// no point continuing alone in a stale game, resync as a party.
						// Don't ClearActiveGame here — followers need to see abort flag first.
						// Active game will be cleared when leader creates new game (SetActiveGame resets state).
						GetPartyRegistry().AbortGame()
					}
				}
				// Leader priority: also signal followers to abort on error/chicken/timeout
				if s.bot.ctx.CharacterCfg.Companion.Enabled &&
					s.bot.ctx.CharacterCfg.Companion.Leader &&
					s.bot.ctx.CharacterCfg.Companion.LeaderPriorityRuns {
					GetPartyRegistry().MarkLeaderDone()
				}
			}

			if exitErr := s.bot.ctx.Manager.ExitGame(); exitErr != nil {
				s.bot.ctx.Logger.Error(fmt.Sprintf("Error trying to exit game: %s", exitErr.Error()))
				runCancel()
				return ErrUnrecoverableClientState
			}

			s.bot.ctx.Logger.Info("Waiting 5 seconds for game client to close completely...")
			utils.Sleep(int(5 * time.Second / time.Millisecond))

			timeout := time.After(15 * time.Second)
			for s.bot.ctx.Manager.InGame() {
				select {
				case <-ctx.Done():
					runCancel()
					return nil
				case <-timeout:
					s.bot.ctx.Logger.Error("Timeout waiting for game to report 'not in game' after exit attempt. Forcing client kill.")
					if killErr := s.KillClient(); killErr != nil {
						s.bot.ctx.Logger.Error(fmt.Sprintf("Failed to kill client after timeout and InGame() check: %s", killErr.Error()))
					}
					runCancel()
					return ErrUnrecoverableClientState
				default:
					s.bot.ctx.Logger.Debug("Still detected as in game, waiting for RefreshGameData to update...")
					utils.Sleep(int(500 * time.Millisecond / time.Millisecond))
					s.bot.ctx.RefreshGameData()
				}
			}
			s.bot.ctx.Logger.Info("Game client successfully detected as 'not in game'.")
			s.bot.ctx.GameReader.ClearMapData() // Free map data memory while not in game
			s.bot.ctx.Data.Areas = nil          // Clear context's map reference to allow GC
			s.bot.ctx.Data.AreaData = game.AreaData{}
			timeSpentNotInGameStart = time.Now()

			if !isDeath || !partyWaitEnabled {
				// Only send GameFinished event if we didn't already send it above (death+party case)
				var gameFinishReason event.FinishReason
				switch {
				case errors.Is(err, health.ErrChicken):
					gameFinishReason = event.FinishedChicken
				case errors.Is(err, health.ErrMercChicken):
					gameFinishReason = event.FinishedMercChicken
				case errors.Is(err, health.ErrDied):
					gameFinishReason = event.FinishedDied
				default:
					gameFinishReason = event.FinishedError
				}
				event.Send(event.GameFinished(event.WithScreenshot(s.name, err.Error(), s.bot.ctx.GameReader.Screenshot()), gameFinishReason))
			}

			s.bot.ctx.Logger.Warn(
				fmt.Sprintf("Game finished with errors, reason: %s. Game total time: %0.2fs", err.Error(), time.Since(gameStart).Seconds()),
				slog.String("supervisor", s.name),
				slog.Uint64("mapSeed", uint64(s.bot.ctx.GameReader.MapSeed())),
			)

			runCancel()
			continue
		}

		gameFinishReason := event.FinishedOK
		event.Send(event.GameFinished(event.Text(s.name, "Game finished successfully"), gameFinishReason))
		s.bot.ctx.Logger.Info(
			fmt.Sprintf("Game finished successfully. Game total time: %0.2fs", time.Since(gameStart).Seconds()),
			slog.String("supervisor", s.name),
			slog.Uint64("mapSeed", uint64(s.bot.ctx.GameReader.MapSeed())),
		)

		// Track the game we just played for companion menu flow (don't rejoin same game)
		s.lastPlayedGame = s.bot.ctx.GameReader.LastGameName()

		// Party wait: idle in game until all party members finish their runs
		// LeaderPriorityRuns: leader skips waiting and immediately exits to create new game
		if s.bot.ctx.CharacterCfg.Companion.Enabled &&
			s.bot.ctx.CharacterCfg.Companion.Leader &&
			s.bot.ctx.CharacterCfg.Companion.LeaderPriorityRuns {
			s.bot.ctx.Logger.Info("Party: leader priority mode — signalling followers and exiting immediately")
			GetPartyRegistry().MarkDone(s.name)
			GetPartyRegistry().MarkLeaderDone()
		} else {
			s.waitForPartyMembers(ctx)
		}

		if s.bot.ctx.CharacterCfg.Companion.Enabled && s.bot.ctx.CharacterCfg.Companion.Leader {
			// Clear active game from registry so companions don't try to rejoin a closing game
			if s.bot.ctx.CharacterCfg.Companion.WaitForParty || s.bot.ctx.CharacterCfg.Companion.LeaderPriorityRuns {
				GetPartyRegistry().ClearActiveGame()
			}
			event.Send(event.ResetCompanionGameInfo(event.Text(s.name, "Game "+s.bot.ctx.Data.Game.LastGameName+" finished"), s.bot.ctx.CharacterCfg.CharacterName))
		}
		if exitErr := s.bot.ctx.Manager.ExitGame(); exitErr != nil {
			errMsg := fmt.Sprintf("Error exiting game %s", exitErr.Error())
			event.Send(event.GameFinished(event.WithScreenshot(s.name, errMsg, s.bot.ctx.GameReader.Screenshot()), event.FinishedError))
			runCancel()
			return errors.New(errMsg)
		}
		s.bot.ctx.Logger.Info("Game finished successfully. Waiting 3 seconds for client to close.")
		utils.Sleep(int(3 * time.Second / time.Millisecond))
		s.bot.ctx.GameReader.ClearMapData() // Free map data memory while not in game
		s.bot.ctx.Data.Areas = nil          // Clear context's map reference to allow GC
		s.bot.ctx.Data.AreaData = game.AreaData{}
		timeSpentNotInGameStart = time.Now()
		runCancel()
	}
}

// waitForPartyMembers idles in-game until all party members have finished their runs.
// Skipped if WaitForParty is disabled. Sets WaitingForParty flag to suppress activity monitor.
//
// Grace period: if only 1 member is registered (just self), waits up to 30s for
// other members to join and register before accepting AllDone. This prevents the
// leader from exiting before companions even enter the game.
func (s *SinglePlayerSupervisor) waitForPartyMembers(ctx context.Context) {
	if !s.bot.ctx.CharacterCfg.Companion.Enabled || !s.bot.ctx.CharacterCfg.Companion.WaitForParty {
		return
	}

	pr := GetPartyRegistry()
	pr.MarkDone(s.name)

	// Grace period: wait for other members to register before checking AllDone.
	// Without this, leader finishes before companions enter the game → AllDone=true instantly.
	const gracePeriod = 30 * time.Second
	graceDeadline := time.Now().Add(gracePeriod)

	if pr.MemberCount() <= 1 {
		s.bot.ctx.Logger.Info("Party: only self registered, waiting grace period for others to join",
			slog.Duration("gracePeriod", gracePeriod))
		s.bot.ctx.WaitingForParty.Store(true)

		graceTicker := time.NewTicker(2 * time.Second)
	graceWait:
		for {
			select {
			case <-ctx.Done():
				graceTicker.Stop()
				s.bot.ctx.WaitingForParty.Store(false)
				return
			case <-graceTicker.C:
				if s.leaderStartedNewGame() {
					s.bot.ctx.Logger.Info("Party: leader started new game during grace period, exiting to rejoin",
						slog.String("newGame", s.bot.ctx.CharacterCfg.Companion.CompanionGameName))
					graceTicker.Stop()
					s.bot.ctx.WaitingForParty.Store(false)
					return
				}
				if pr.MemberCount() > 1 {
					s.bot.ctx.Logger.Info("Party: other members joined during grace period",
						slog.Int("members", pr.MemberCount()))
					break graceWait
				}
				if time.Now().After(graceDeadline) {
					s.bot.ctx.Logger.Info("Party: grace period expired, no other members joined — proceeding solo")
					graceTicker.Stop()
					s.bot.ctx.WaitingForParty.Store(false)
					return
				}
			}
		}
		graceTicker.Stop()
	}

	// Remove members that were preserved from previous game but didn't rejoin
	// (e.g. paused/stopped bots). Without this, leader waits PartyWaitTimeout every game.
	pr.PurgeStaleMembers()

	isLeader := s.bot.ctx.CharacterCfg.Companion.Leader

	if pr.AllDone() {
		if isLeader {
			s.bot.ctx.Logger.Info("Party: all members already done, leader proceeding to create new game")
			s.bot.ctx.WaitingForParty.Store(false)
			return
		}
		s.bot.ctx.Logger.Info("Party: all members done, companion waiting in town for leader's new game")
		// Fall through to idle loop — companion stays in game until leader creates new game
	}

	// Bonus runs: do extra runs while waiting for party members instead of idling
	if s.bot.ctx.CharacterCfg.Companion.BonusRuns {
		if needsChicken := s.doBonusRuns(ctx, pr); needsChicken {
			s.bot.ctx.Logger.Warn("Party: chicken during bonus run, exiting game")
			if s.bot.ctx.CharacterCfg.Companion.Leader {
				GetPartyRegistry().AbortGame()
			}
			return // exit waitForPartyMembers → main flow will ExitGame + rejoin
		}
		if pr.AllDone() && isLeader {
			return
		}
	}

	// Set flag so activity monitor doesn't kill us
	s.bot.ctx.WaitingForParty.Store(true)
	defer s.bot.ctx.WaitingForParty.Store(false)

	timeout := time.Duration(s.bot.ctx.CharacterCfg.Companion.PartyWaitTimeout) * time.Second
	if timeout <= 0 {
		timeout = 300 * time.Second // 5 min default
	}

	s.bot.ctx.Logger.Info("Party: waiting for other members to finish",
		slog.Any("status", pr.Status()),
		slog.Duration("timeout", timeout))

	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	purgeInterval := 30 * time.Second
	lastPurge := time.Now()

	for {
		select {
		case <-ctx.Done():
			s.bot.ctx.Logger.Info("Party: context cancelled while waiting")
			return
		case <-ticker.C:
			// Periodically purge stale members (crashed/stopped bots)
			if time.Since(lastPurge) >= purgeInterval {
				pr.PurgeStaleMembers()
				lastPurge = time.Now()
			}

			if isLeader && pr.AllDone() {
				s.bot.ctx.Logger.Info("Party: all members done, leader proceeding to exit game")
				return
			}
			if !isLeader && pr.GameAborted() {
				s.bot.ctx.Logger.Info("Party: game aborted by leader, exiting to resync")
				return
			}
			if !isLeader && pr.IsLeaderDone() {
				s.bot.ctx.Logger.Info("Party: leader priority mode — leader done, exiting to rejoin new game")
				return
			}
			if s.leaderStartedNewGame() {
				s.bot.ctx.Logger.Info("Party: leader started new game, exiting to rejoin",
					slog.String("newGame", s.bot.ctx.CharacterCfg.Companion.CompanionGameName))
				return
			}
			if time.Now().After(deadline) {
				s.bot.ctx.Logger.Warn("Party: wait timeout reached, proceeding to exit game",
					slog.Any("status", pr.Status()))
				return
			}
		}
	}
}

// leaderStartedNewGame returns true if the leader has created a new game and is
// waiting for this follower to join. Used to break out of bonus runs / idle wait
// when the leader crashes/chickens and starts fresh.
func (s *SinglePlayerSupervisor) leaderStartedNewGame() bool {
	if !s.bot.ctx.CharacterCfg.Companion.Enabled || s.bot.ctx.CharacterCfg.Companion.Leader {
		return false
	}
	newGame := s.bot.ctx.CharacterCfg.Companion.CompanionGameName

	// Fallback: check party registry (same as HandleCompanionMenuFlow)
	if newGame == "" {
		if activeGame := GetPartyRegistry().GetActiveGame(); activeGame != nil {
			leaderOK := s.bot.ctx.CharacterCfg.Companion.LeaderName == "" ||
				s.bot.ctx.CharacterCfg.Companion.LeaderName == activeGame.LeaderName
			if leaderOK {
				newGame = activeGame.GameName
			}
		}
	}

	return newGame != "" && newGame != s.bot.ctx.Data.Game.LastGameName
}

// doBonusRuns executes random short farming runs while waiting for party members.
// It reuses bot.Run() for each bonus run, which provides all background infrastructure
// (data refresh, health manager, item pickup, panic recovery) automatically.
// Returns true if the bot needs to chicken (exit game immediately).
func (s *SinglePlayerSupervisor) doBonusRuns(ctx context.Context, pr *PartyRegistry) bool {
	// Don't start bonus runs if all members already done — saves ~20s of map fetch + town prep
	if pr.AllDone() {
		s.bot.ctx.Logger.Info("Party: all members already done, skipping bonus runs")
		return false
	}

	// Don't start bonus runs if character is dead
	s.bot.ctx.RefreshGameData()
	if s.bot.ctx.Data.PlayerUnit.HPPercent() <= 0 {
		s.bot.ctx.Logger.Warn("Party: character is dead, skipping bonus runs")
		return false // dead = can't run, but don't signal chicken (misleading log)
	}

	// Build bonus pool: use configured list or all short runs, minus runs taken by party members
	allowedRuns := s.bot.ctx.CharacterCfg.Companion.BonusRunsList
	allowedSet := make(map[string]bool, len(allowedRuns))
	for _, r := range allowedRuns {
		allowedSet[r] = true
	}

	takenRuns := pr.GetAllPartyRuns()
	var bonusPool []string
	for _, r := range config.ShortBonusRuns {
		name := string(r)
		if takenRuns[name] {
			continue
		}
		// If user configured specific runs, only include those
		if len(allowedSet) > 0 && !allowedSet[name] {
			continue
		}
		bonusPool = append(bonusPool, name)
	}
	if len(bonusPool) == 0 {
		s.bot.ctx.Logger.Info("Party: no available bonus runs (all short runs taken by party)")
		return false
	}

	// Shuffle pool so bonus runs are in random order, then execute without repeats
	rand.Shuffle(len(bonusPool), func(i, j int) { bonusPool[i], bonusPool[j] = bonusPool[j], bonusPool[i] })

	s.bot.ctx.Logger.Info("Party: starting bonus runs while waiting",
		slog.Any("bonusPool", bonusPool),
		slog.Int("available", len(bonusPool)))

	// Create a cancellable context for bonus runs — cancelled when leader starts new game
	// or all members are done, so the current run aborts IMMEDIATELY.
	bonusCtx, bonusCancel := context.WithCancel(ctx)
	defer bonusCancel()

	// Reset abort signal at start, set it on abort so long-running actions (gambling) stop
	s.bot.ctx.AbortBonusRun.Store(false)
	defer s.bot.ctx.AbortBonusRun.Store(false) // cleanup on exit

	// Monitor goroutine: cancel bonus runs when leader creates new game or all done
	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-bonusCtx.Done():
				return
			case <-ticker.C:
				if s.leaderStartedNewGame() {
					s.bot.ctx.Logger.Info("Party: leader started new game, aborting bonus run immediately")
					s.bot.ctx.AbortBonusRun.Store(true)
					bonusCancel()
					return
				}
				if pr.AllDone() {
					s.bot.ctx.Logger.Info("Party: all members done, aborting bonus run immediately")
					s.bot.ctx.AbortBonusRun.Store(true)
					bonusCancel()
					return
				}
				if pr.GameAborted() {
					s.bot.ctx.Logger.Info("Party: game aborted, aborting bonus run immediately")
					s.bot.ctx.AbortBonusRun.Store(true)
					bonusCancel()
					return
				}
			}
		}
	}()

	for i := 0; i < len(bonusPool); i++ {
		select {
		case <-bonusCtx.Done():
			return false
		default:
		}

		if pr.AllDone() {
			s.bot.ctx.Logger.Info("Party: all members done, stopping bonus runs")
			return false
		}

		if pr.GameAborted() {
			s.bot.ctx.Logger.Info("Party: game aborted by leader, stopping bonus runs")
			return false
		}

		if s.leaderStartedNewGame() {
			s.bot.ctx.Logger.Info("Party: leader started new game, aborting bonus runs to rejoin")
			return false
		}

		runName := bonusPool[i]

		bonusRun := run.BuildRun(runName)
		if bonusRun == nil {
			continue
		}

		// Atomically reserve the run — prevents two followers from picking the same run
		if !pr.ReserveRun(s.name, runName) {
			s.bot.ctx.Logger.Debug("Party: bonus run already taken, skipping", slog.String("run", runName))
			continue
		}

		s.bot.ctx.Logger.Info("Party: starting bonus run", slog.String("run", runName))

		err := s.bot.Run(bonusCtx, false, []run.Run{bonusRun})
		if err != nil {
			// Context cancelled = leader started new game or all done — not an error
			if bonusCtx.Err() != nil {
				s.bot.ctx.Logger.Info("Party: bonus run aborted (party event)", slog.String("run", runName))
				return false
			}
			// Chicken or death — must exit game immediately
			if errors.Is(err, health.ErrChicken) || errors.Is(err, health.ErrDied) || errors.Is(err, health.ErrMercChicken) {
				s.bot.ctx.Logger.Warn("Party: bonus run chicken/death, exiting game", slog.String("run", runName), slog.Any("error", err))
				return true
			}
			// Non-fatal error (seal not found, destination missing, timeout, etc.) — skip and try next run
			s.bot.ctx.Logger.Warn("Party: bonus run failed, skipping to next", slog.String("run", runName), slog.Any("error", err))
			continue
		}

		s.bot.ctx.Logger.Info("Party: bonus run completed", slog.String("run", runName))
	}

	s.bot.ctx.Logger.Info("Party: all bonus runs exhausted")
	return false
}

func (s *SinglePlayerSupervisor) ensureSkillKeyBindingsReady() error {
	cfg := s.bot.ctx.CharacterCfg
	if cfg == nil {
		s.bot.ctx.Logger.Debug("Skipping key binding check: character config is nil")
		return nil
	}
	characterName := strings.TrimSpace(cfg.CharacterName)
	if characterName == "" {
		s.bot.ctx.Logger.Debug("Skipping key binding check: character name is empty")
		return nil
	}
	if s.bot.ctx.ManualModeActive {
		s.bot.ctx.Logger.Debug("Skipping key binding check: manual mode active")
		return nil
	}
	if s.bot.ctx.ClaudeModeActive {
		s.bot.ctx.Logger.Debug("Skipping key binding check: claude mode active")
		return nil
	}
	if s.bot.ctx.Manager.InGame() {
		s.bot.ctx.Logger.Debug("Skipping key binding check: already in game")
		return nil
	}

	kbResult, kbErr := config.EnsureSkillKeyBindings(cfg, config.App.UseCustomSettings)
	if kbErr != nil {
		s.bot.ctx.Logger.Warn("Failed to ensure skill key bindings", slog.Any("error", kbErr))
	}

	if kbResult.Updated {
		s.bot.ctx.Logger.Info("Skill key bindings updated on disk; restarting client to apply")
		if killErr := s.KillClient(); killErr != nil {
			return killErr
		}
		return ErrUnrecoverableClientState
	}

	if !kbResult.Missing {
		return nil
	}

	s.bot.ctx.Logger.Info("Key binding file missing; entering a game to bootstrap", slog.String("character", characterName))

	if err := s.waitUntilCharacterSelectionScreen(); err != nil {
		return err
	}
	enterDeadline := time.Now().Add(2 * time.Minute)
	for !s.bot.ctx.Manager.InGame() {
		if time.Now().After(enterDeadline) {
			return fmt.Errorf("timed out entering game for key binding bootstrap")
		}
		if err := s.HandleMenuFlow(); err != nil {
			if errors.Is(err, ErrUnrecoverableClientState) {
				return err
			}
			if err.Error() == "loading screen" || err.Error() == "" || err.Error() == "idle" {
				utils.Sleep(100)
				continue
			}
			return err
		}
		utils.Sleep(100)
	}

	if waitErr := config.WaitForKeyBindings(kbResult.SaveDir, characterName, cfg.AuthMethod, 45*time.Second); waitErr != nil {
		s.bot.ctx.Logger.Warn("Timed out waiting for key binding file", slog.Any("error", waitErr))
	}

	kbResult, kbErr = config.EnsureSkillKeyBindings(cfg, config.App.UseCustomSettings)
	if kbErr != nil {
		s.bot.ctx.Logger.Warn("Failed to ensure skill key bindings after bootstrap", slog.Any("error", kbErr))
	}

	if s.bot.ctx.Manager.InGame() {
		if exitErr := s.bot.ctx.Manager.ExitGame(); exitErr != nil {
			s.bot.ctx.Logger.Warn("Failed to exit game after key binding bootstrap", slog.Any("error", exitErr))
		} else {
			exitDeadline := time.Now().Add(15 * time.Second)
			for s.bot.ctx.Manager.InGame() && time.Now().Before(exitDeadline) {
				utils.Sleep(250)
				s.bot.ctx.RefreshGameData()
			}
		}
	}

	if kbResult.Updated {
		s.bot.ctx.Logger.Info("Skill key bindings updated on disk; restarting client to apply")
		if killErr := s.KillClient(); killErr != nil {
			return killErr
		}
		return ErrUnrecoverableClientState
	}

	if kbResult.Missing {
		s.bot.ctx.Logger.Warn("Key binding file still missing after bootstrap", slog.String("character", characterName))
	}

	return nil
}

// NEW HELPER FUNCTION that wraps a blocking operation with a timeout
func (s *SinglePlayerSupervisor) callManagerWithTimeout(fn func() error) error {
	errChan := make(chan error, 1)
	go func() {
		errChan <- fn()
	}()

	select {
	case err := <-errChan:
		return err
	case <-time.After(menuActionTimeout):
		if s.isGameWindowHung() {
			s.bot.ctx.Logger.Error("[Menu Flow]: manager action timed out and D2R window is hung; killing client",
				slog.Duration("timeout", menuActionTimeout),
				slog.Uint64("pid", uint64(s.bot.ctx.GameReader.Process.GetPID())))
			if killErr := s.KillClient(); killErr != nil {
				s.bot.ctx.Logger.Error("[Menu Flow]: failed to kill hung client after manager timeout",
					slog.Any("error", killErr))
			}
			return ErrUnrecoverableClientState
		}
		return fmt.Errorf("menu action timed out after %s", menuActionTimeout)
	}
}

func (s *SinglePlayerSupervisor) isGameWindowHung() bool {
	if s == nil || s.bot == nil || s.bot.ctx == nil || s.bot.ctx.GameReader == nil || s.bot.ctx.GameReader.HWND == 0 {
		return false
	}

	ret, _, _ := isHungAppWindowFn.Call(uintptr(s.bot.ctx.GameReader.HWND))
	return ret != 0
}

func (s *SinglePlayerSupervisor) HandleMenuFlow() error {
	s.bot.ctx.RefreshGameData()

	if s.bot.ctx.Data.OpenMenus.LoadingScreen {
		utils.Sleep(500)
		return fmt.Errorf("loading screen")
	}

	if !s.bot.ctx.Manager.InGame() && s.bot.ctx.HID != nil && s.bot.ctx.HID.IsDisabled() {
		s.disablePacketRuntimeForMenu()
		s.bot.ctx.Logger.Info("Menu flow reached; HID re-enabled before next game load")
		s.bot.ctx.HID.Enable("menu flow before game load")
	}

	s.bot.ctx.Logger.Debug("[Menu Flow]: Starting menu flow ...")

	if s.bot.ctx.GameReader.IsInCharacterCreationScreen() {
		s.bot.ctx.Logger.Debug("[Menu Flow]: We're in character creation screen, exiting ...")
		s.bot.ctx.HID.PressKey(0x1B)
		time.Sleep(2000 * time.Millisecond)
		if s.bot.ctx.GameReader.IsInCharacterCreationScreen() {
			return errors.New("[Menu Flow]: Failed to exit character creation screen")
		}
	}

	if s.bot.ctx.Manager.InGame() {
		s.bot.ctx.Logger.Debug("[Menu Flow]: We're still ingame, exiting ...")
		return s.bot.ctx.Manager.ExitGame()
	}

	isDismissableModalPresent, text := s.bot.ctx.GameReader.IsDismissableModalPresent()
	if isDismissableModalPresent {
		s.bot.ctx.Logger.Debug("[Menu Flow]: Detected dismissable modal with text: " + text)
		s.bot.ctx.HID.PressKey(0x1B)
		time.Sleep(1000 * time.Millisecond)

		isDismissableModalStillPresent, _ := s.bot.ctx.GameReader.IsDismissableModalPresent()
		if isDismissableModalStillPresent {
			s.bot.ctx.Logger.Warn(fmt.Sprintf("[Menu Flow]: Dismissable modal still present after attempt to dismiss: %s", text))
			s.bot.ctx.FailedModalDismissAttempts++
			const MAX_MODAL_DISMISS_ATTEMPTS = 3
			if s.bot.ctx.FailedModalDismissAttempts >= MAX_MODAL_DISMISS_ATTEMPTS {
				s.bot.ctx.Logger.Error(fmt.Sprintf("[Menu Flow]: Failed to dismiss modal '%s' %d times. Assuming unrecoverable state.", text, MAX_MODAL_DISMISS_ATTEMPTS))
				s.bot.ctx.FailedModalDismissAttempts = 0
				return ErrUnrecoverableClientState
			}
			return errors.New("[Menu Flow]: Failed to dismiss popup (still present)")
		}
	} else {
		// Reset modal dismiss counter when no modal is present
		s.bot.ctx.FailedModalDismissAttempts = 0
	}

	if s.bot.ctx.CharacterCfg.Companion.Enabled && !s.bot.ctx.CharacterCfg.Companion.Leader {
		return s.HandleCompanionMenuFlow()
	}

	return s.HandleStandardMenuFlow()
}

func (s *SinglePlayerSupervisor) disablePacketRuntimeForMenu() {
	if s == nil || s.bot == nil || s.bot.ctx == nil {
		return
	}
	ctx := s.bot.ctx
	var presReady bool
	if ctx.MemoryInjector != nil {
		if pres := ctx.MemoryInjector.GetPresenter(); pres != nil && pres.IsReady() {
			presReady = true
		}
	}
	if ctx.GameReader != nil && ctx.GameReader.Process != nil && !presReady {
		ctx.GameReader.Process.SetExternalDualSender(nil)
		ctx.GameReader.Process.SetExternalClick(nil)
		ctx.GameReader.Process.SetExternalForceClick(nil)
		ctx.GameReader.Process.SetExternalPostKey(nil)
		ctx.GameReader.Process.SetPreferredPacketThreadID(0)
	}
	if ctx.MemoryInjector == nil {
		return
	}
	hadPresenter := ctx.MemoryInjector.GetPresenter() != nil
	if hadPresenter {
		ctx.Logger.Info("Menu flow: presenter retained for next in-game packet run")
	}
	if ctx.MemoryInjector.CursorOverrideActive() {
		if err := ctx.MemoryInjector.DisableCursorOverride(); err != nil {
			ctx.Logger.Warn("Menu flow: cursor override cleanup failed", slog.Any("error", err))
		} else {
			ctx.Logger.Info("Menu flow: cursor override disabled before menu input")
		}
	}
}

func (s *SinglePlayerSupervisor) HandleStandardMenuFlow() error {
	atCharacterSelectionScreen := s.bot.ctx.GameReader.IsInCharacterSelectionScreen()

	if atCharacterSelectionScreen && s.bot.ctx.CharacterCfg.AuthMethod != "None" && !s.bot.ctx.CharacterCfg.Game.CreateLobbyGames {
		s.bot.ctx.Logger.Debug("[Menu Flow]: We're at the character selection screen, ensuring we're online ...")

		err := s.ensureOnline()
		if err != nil {
			return err
		}

		s.bot.ctx.Logger.Debug("[Menu Flow]: We're online, creating new game ...")

		// USE THE NEW TIMEOUT FUNCTION
		return s.callManagerWithTimeout(s.bot.ctx.Manager.NewGame)

	} else if atCharacterSelectionScreen && s.bot.ctx.CharacterCfg.AuthMethod == "None" {

		s.bot.ctx.Logger.Debug("[Menu Flow]: Creating new game ...")
		return s.callManagerWithTimeout(s.bot.ctx.Manager.NewGame)
	}

	atLobbyScreen := s.bot.ctx.GameReader.IsInLobby()

	if atLobbyScreen && s.bot.ctx.CharacterCfg.Game.CreateLobbyGames {
		s.bot.ctx.Logger.Debug("[Menu Flow]: We're at the lobby screen and we should create a lobby game ...")

		if s.bot.ctx.CharacterCfg.Game.PublicGameCounter == 0 {
			s.bot.ctx.CharacterCfg.Game.PublicGameCounter = 1
		}

		return s.createLobbyGame()
	} else if !atLobbyScreen && s.bot.ctx.CharacterCfg.Game.CreateLobbyGames {
		s.bot.ctx.Logger.Debug("[Menu Flow]: We're not at the lobby screen, trying to enter lobby ...")
		err := s.tryEnterLobby()
		if err != nil {
			return err
		}

		return s.createLobbyGame()
	} else if atLobbyScreen && !s.bot.ctx.CharacterCfg.Game.CreateLobbyGames {
		s.bot.ctx.Logger.Debug("[Menu Flow]: We're at the lobby screen, but we shouldn't be, going back to character selection screen ...")

		s.bot.ctx.HID.PressKey(0x1B)
		time.Sleep(2000 * time.Millisecond)

		if s.bot.ctx.GameReader.IsInLobby() {
			return fmt.Errorf("[Menu Flow]: Failed to exit lobby")
		}

		if s.bot.ctx.GameReader.IsInCharacterSelectionScreen() {
			return s.callManagerWithTimeout(s.bot.ctx.Manager.NewGame)
		}
	}

	return fmt.Errorf("[Menu Flow]: Unhandled menu scenario")
}

func (s *SinglePlayerSupervisor) HandleCompanionMenuFlow() error {
	s.bot.ctx.Logger.Debug("[Menu Flow]: Trying to enter lobby ...")

	gameName := s.bot.ctx.CharacterCfg.Companion.CompanionGameName
	gamePassword := s.bot.ctx.CharacterCfg.Companion.CompanionGamePassword

	// If no game info from event, check party registry for active game (rejoin after crash/chicken)
	if gameName == "" && (s.bot.ctx.CharacterCfg.Companion.WaitForParty || s.bot.ctx.CharacterCfg.Companion.LeaderPriorityRuns) {
		if activeGame := GetPartyRegistry().GetActiveGame(); activeGame != nil {
			// Only rejoin if the leader matches our config (or no leader configured)
			leaderOK := s.bot.ctx.CharacterCfg.Companion.LeaderName == "" ||
				s.bot.ctx.CharacterCfg.Companion.LeaderName == activeGame.LeaderName
			if leaderOK {
				s.bot.ctx.Logger.Info("Party: found active game in registry, rejoining after crash/chicken",
					slog.String("game", activeGame.GameName),
					slog.String("leader", activeGame.LeaderName))
				gameName = activeGame.GameName
				gamePassword = activeGame.Password
			}
		}
	}

	// Don't rejoin the game we just finished — wait for leader to create a new one.
	// Use lastPlayedGame (set on successful game completion) instead of GameReader.LastGameName()
	// because LastGameName can be set by a failed join attempt, causing a permanent block.
	if gameName != "" && gameName == s.lastPlayedGame {
		s.bot.ctx.Logger.Debug("Party: active game is the same we just left, waiting for new game",
			slog.String("game", gameName))
		utils.Sleep(5000)
		return fmt.Errorf("idle")
	}

	if gameName == "" {
		utils.Sleep(2000)
		return fmt.Errorf("idle")
	}

	joinGameFunc := func() error {
		return s.bot.ctx.Manager.JoinOnlineGame(gameName, gamePassword)
	}

	var joinErr error
	if s.bot.ctx.GameReader.IsInCharacterSelectionScreen() {
		err := s.ensureOnline()
		if err != nil {
			return err
		}

		err = s.tryEnterLobby()
		if err != nil {
			return err
		}

		joinErr = s.callManagerWithTimeout(joinGameFunc)
	} else if s.bot.ctx.GameReader.IsInLobby() {
		s.bot.ctx.Logger.Debug("[Menu Flow]: We're in lobby, joining game ...")
		joinErr = s.callManagerWithTimeout(joinGameFunc)
	} else {
		return fmt.Errorf("[Menu Flow]: Unhandled Companion menu scenario")
	}

	// If join failed, clear stale game info so we don't retry the same dead game in a loop.
	// Return "idle" to reset the out-of-game watchdog and wait for a fresh event or registry update.
	if joinErr != nil {
		s.bot.ctx.Logger.Warn("Party: failed to join game, clearing stale game info to avoid retry loop",
			slog.String("game", gameName),
			slog.String("error", joinErr.Error()))
		s.bot.ctx.CharacterCfg.Companion.CompanionGameName = ""
		s.bot.ctx.CharacterCfg.Companion.CompanionGamePassword = ""
		return fmt.Errorf("idle")
	}
	return nil
}

func (s *SinglePlayerSupervisor) tryEnterLobby() error {
	if s.bot.ctx.GameReader.IsInLobby() {
		s.bot.ctx.Logger.Debug("[Menu Flow]: We're already in lobby, exiting ...")
		return nil
	}

	retryCount := 0
	for !s.bot.ctx.GameReader.IsInLobby() {
		s.bot.ctx.Logger.Info("Entering lobby", slog.String("supervisor", s.name))
		if retryCount >= 5 {
			return fmt.Errorf("[Menu Flow]: Failed to enter bnet lobby after 5 retries")
		}

		s.bot.ctx.HID.Click(game.LeftButton, 744, 650)
		utils.Sleep(1000)
		retryCount++
	}

	return nil
}

func (s *SinglePlayerSupervisor) createLobbyGame() error {
	s.bot.ctx.Logger.Debug("[Menu Flow]: Trying to create lobby game ...")

	// USE THE NEW TIMEOUT FUNCTION
	createGameFunc := func() error {
		_, err := s.bot.ctx.Manager.CreateLobbyGame(s.bot.ctx.CharacterCfg.Game.PublicGameCounter)
		return err
	}
	err := s.callManagerWithTimeout(createGameFunc)

	if err != nil {
		s.bot.ctx.CharacterCfg.Game.PublicGameCounter++
		s.bot.ctx.FailedToCreateGameAttempts++
		const MAX_GAME_CREATE_ATTEMPTS = 5
		if s.bot.ctx.FailedToCreateGameAttempts >= MAX_GAME_CREATE_ATTEMPTS {
			s.bot.ctx.Logger.Error(fmt.Sprintf("[Menu Flow]: Failed to create lobby game %d times. Forcing client restart.", MAX_GAME_CREATE_ATTEMPTS))
			s.bot.ctx.FailedToCreateGameAttempts = 0
			return ErrUnrecoverableClientState
		}
		return fmt.Errorf("[Menu Flow]: Failed to create lobby game: %w", err)
	}

	isDismissableModalPresent, text := s.bot.ctx.GameReader.IsDismissableModalPresent()
	if isDismissableModalPresent {
		s.bot.ctx.CharacterCfg.Game.PublicGameCounter++
		s.bot.ctx.Logger.Warn(fmt.Sprintf("[Menu Flow]: Dismissable modal present after game creation attempt: %s", text))

		if strings.Contains(strings.ToLower(text), "failed to create game") || strings.Contains(strings.ToLower(text), "unable to join") {
			s.bot.ctx.FailedToCreateGameAttempts++
			const MAX_GAME_CREATE_ATTEMPTS_MODAL = 3
			if s.bot.ctx.FailedToCreateGameAttempts >= MAX_GAME_CREATE_ATTEMPTS_MODAL {
				s.bot.ctx.Logger.Error(fmt.Sprintf("[Menu Flow]: 'Failed to create game' modal detected %d times. Forcing client restart.", MAX_GAME_CREATE_ATTEMPTS_MODAL))
				s.bot.ctx.FailedToCreateGameAttempts = 0
				return ErrUnrecoverableClientState
			}
		}
		return fmt.Errorf("[Menu Flow]: Failed to create lobby game: %s", text)
	}

	s.bot.ctx.Logger.Debug("[Menu Flow]: Lobby game created successfully")
	s.bot.ctx.CharacterCfg.Game.PublicGameCounter++
	s.bot.ctx.FailedToCreateGameAttempts = 0
	return nil
}

// dumpArmory saves the current character inventory state to a JSON file
func (s *SinglePlayerSupervisor) dumpArmory() error {
	if s.bot.ctx.Data == nil {
		return fmt.Errorf("game data not available")
	}

	gameName := s.bot.ctx.GameReader.LastGameName()
	return dumpArmoryData(s.name, s.bot.ctx.Data, gameName)
}

// initPresenterDualOnly is the minimal-side-effect variant of
// initClaudePresenter: load rmod.dll, run pres.Init, wire only
// SetExternalDualSender + SetForceMoveAddr. Skips the Claude-mode wiring of
// CallFn/WriteMem/RopRead/BatchRead/SlotBatchRead, which break HID-driven
// movement and door/WP click in normal mode (test15 04-19: bot stuck on
// object=119 immediately after full Claude init). With dual-only, every
// dual-buffer packet (0x38, 0x50, 0x33, 0x32, 0x18, 0x19, 0x54, 0x27) reaches
// D2R via the Present hook; everything else stays on the legacy APC path.
func (s *SinglePlayerSupervisor) initPresenterDualOnly() {
	defer func() {
		if r := recover(); r != nil {
			s.bot.ctx.Logger.Error(fmt.Sprintf("PANIC in dual-only presenter init: %v", r))
		}
	}()

	if s.bot.ctx.MemoryInjector != nil && s.bot.ctx.MemoryInjector.GetPresenter() != nil {
		s.bot.ctx.Logger.Warn("Normal mode: presenter already initialized — skipping duplicate init")
		return
	}

	s.bot.ctx.Logger.Info("Normal mode: dual-only presenter bootstrap start (post-game timing)")
	gr := s.bot.ctx.GameReader
	gi := s.bot.ctx.MemoryInjector
	pid := gr.GetPID()
	hwnd := gr.HWND

	fnSendPacket, fnErr := gr.Process.GetD2GSSendPacketFn()
	if fnErr != nil {
		s.bot.ctx.Logger.Warn("dual-only presenter: could not resolve dispatch function", slog.Any("error", fnErr))
		return
	}

	const uiNetManRVA uintptr = 0x19ED860
	uiNetManAddr := gr.Process.ModuleBaseAddress() + uiNetManRVA
	const mirrorBufRVA uintptr = 0x1F51330
	mirrorBufAddr := gr.Process.ModuleBaseAddress() + mirrorBufRVA
	const dualSendWrapRVA uintptr = 0x147110
	dualSendWrapAddr := gr.Process.ModuleBaseAddress() + dualSendWrapRVA

	var realClickFn uintptr
	if rcFn, rcErr := gr.Process.GetRealClickWorkerFn(); rcErr == nil {
		realClickFn = rcFn
	}

	presenterDLLPath := filepath.Join("tools", "rmod.dll")
	if absPath, err := filepath.Abs(presenterDLLPath); err == nil {
		presenterDLLPath = absPath
	}
	if _, statErr := os.Stat(presenterDLLPath); statErr != nil {
		s.bot.ctx.Logger.Warn("dual-only presenter: rmod.dll missing", slog.String("path", presenterDLLPath))
		return
	}

	pres := presenter.New(pid, presenterDLLPath)
	// SKIP Present hook — only GetTickCount64 IAT + TimerQueue worker. Avoids
	// render thread slowdown that breaks HID clicks in normal mode (test35/36).
	pres.SetSkipPresentHook(true)
	s.bot.ctx.Logger.Info("Normal mode: dual-only presenter init attempt",
		slog.String("path", presenterDLLPath),
		slog.Uint64("pid", uint64(pid)))
	if initErr := pres.Init(fnSendPacket, uiNetManAddr, realClickFn, uintptr(hwnd), mirrorBufAddr, dualSendWrapAddr); initErr != nil {
		s.bot.ctx.Logger.Error("dual-only presenter init FAILED", slog.Any("error", initErr))
		return
	}

	gi.SetPresenter(pres)
	if tid, _, _ := getWindowThreadProcessIdFn.Call(uintptr(hwnd), 0); tid != 0 {
		pres.SetGameThreadID(uint32(tid))
		gr.Process.SetPreferredPacketThreadID(uint32(tid))
		s.bot.ctx.Logger.Info("Normal mode: D2R window thread recorded; packet APC sender pinned to window/game thread",
			slog.Uint64("tid", uint64(tid)),
			slog.Uint64("hwnd", uint64(uintptr(hwnd))))
	}
	// NOTE 2026-04-20 test40: tried SendDualPacketGT — D2R crashes 0xC0000005
	// because dual_send_wrap's Arxan tamper byte-rotates caller return address.
	// When called from GTC64 thunk (our allocated page), rotation corrupts our
	// shellcode after ~2 calls → crash. Proper fix requires ROP chain with
	// gadgets from D2R.text (Arxan tamper harmless self-mod of D2R code).
	// Reverted to SendDualPacket (render-thread) — same limitation but D2R survives.
	gr.Process.SetExternalDualSender(pres.SendDualPacket)
	gr.Process.SetExternalPostKey(func(vk byte) error {
		return gi.PostKeyInProcess(uintptr(hwnd), vk)
	})
	gr.Process.SetExternalCallFn(pres.CallFn)
	gr.Process.SetExternalCallFnGameThread(pres.CallFnGameThread)
	if realClickFn != 0 {
		gr.Process.SetExternalClick(func(x, y int32, btn byte) error {
			return pres.NativeClick(x, y, btn)
		})
		gr.Process.SetExternalForceClick(func(x, y int32) error {
			return pres.ForceClick(x, y)
		})
	}
	forceMoveAddr := gr.Process.GetModuleBase() + 0x19D25B4 + 0x49C + 4
	pres.SetForceMoveAddr(forceMoveAddr)
	s.bot.ctx.Logger.Info("Normal mode: dual-only presenter initialized — dual-buffer packets routed via rmod")
}

func (s *SinglePlayerSupervisor) initClaudePresenter() {
	defer func() {
		if r := recover(); r != nil {
			s.bot.ctx.Logger.Error(fmt.Sprintf("PANIC in claude presenter init: %v", r))
		}
	}()

	// Defence-in-depth: if manager.go's buildSupervisor already wired up a
	// presenter (pre-Claude-gate flow or future regression), a second pres.Init
	// injects a second rmod image into D2R with chained Present detours. That
	// kills SnapshotInit/UninstallDetour acks. Manager.go now skips its init
	// for CLAUDE_MODE; this guard surfaces any drift back to double-init.
	if s.bot.ctx.MemoryInjector != nil && s.bot.ctx.MemoryInjector.GetPresenter() != nil {
		s.bot.ctx.Logger.Warn("Claude mode: presenter already initialized — skipping duplicate init")
		return
	}

	s.bot.ctx.Logger.Info("Claude mode: presenter bootstrap start")
	presenterDLLName, presenterDLLPath, presenterDLLReason, dllErr := resolvePresenterDLL(true)
	if dllErr != nil {
		s.bot.ctx.Logger.Error("presenter DLL resolution failed", slog.Any("error", dllErr))
		return
	}
	s.bot.ctx.Logger.Info("Claude mode: presenter DLL resolved",
		slog.String("dll", presenterDLLName),
		slog.String("path", presenterDLLPath),
		slog.String("reason", presenterDLLReason))

	gr := s.bot.ctx.GameReader
	gi := s.bot.ctx.MemoryInjector
	pid := gr.GetPID()
	hwnd := gr.HWND

	fnSendPacket, fnErr := gr.Process.GetD2GSSendPacketFn()
	if fnErr != nil {
		s.bot.ctx.Logger.Warn("could not resolve dispatch function", slog.Any("error", fnErr))
		return
	}

	const uiNetManRVA uintptr = 0x19ED860
	uiNetManAddr := gr.Process.ModuleBaseAddress() + uiNetManRVA

	const mirrorBufRVA uintptr = 0x1F51330
	mirrorBufAddr := gr.Process.ModuleBaseAddress() + mirrorBufRVA

	const dualSendWrapRVA uintptr = 0x147110
	dualSendWrapAddr := gr.Process.ModuleBaseAddress() + dualSendWrapRVA

	var realClickFn uintptr
	if rcFn, rcErr := gr.Process.GetRealClickWorkerFn(); rcErr == nil {
		realClickFn = rcFn
	}

	pres := presenter.New(pid, presenterDLLPath)
	if os.Getenv("CLAUDE_USE_PRESENT_HOOK") != "1" {
		pres.SetSkipPresentHook(true)
	}
	s.bot.ctx.Logger.Info("Claude mode: presenter init attempt",
		slog.String("dll", presenterDLLName),
		slog.String("path", presenterDLLPath),
		slog.Bool("skipPresentHook", os.Getenv("CLAUDE_USE_PRESENT_HOOK") != "1"),
		slog.Uint64("pid", uint64(pid)))
	if initErr := pres.Init(fnSendPacket, uiNetManAddr, realClickFn, uintptr(hwnd), mirrorBufAddr, dualSendWrapAddr); initErr != nil {
		s.bot.ctx.Logger.Error("presenter init FAILED", slog.Any("error", initErr))
		return
	}

	if loadErr := gi.Load(); loadErr != nil {
		s.bot.ctx.Logger.Error("Claude mode: MemoryInjector.Load after presenter init FAILED", slog.Any("error", loadErr))
		return
	}

	gi.SetPresenter(pres)
	if tid, _, _ := getWindowThreadProcessIdFn.Call(uintptr(hwnd), 0); tid != 0 {
		pres.SetGameThreadID(uint32(tid))
		gr.Process.SetPreferredPacketThreadID(uint32(tid))
		s.bot.ctx.Logger.Info("Claude mode: D2R window thread recorded; packet APC sender pinned to window/game thread",
			slog.Uint64("tid", uint64(tid)),
			slog.Uint64("hwnd", uint64(uintptr(hwnd))))
	}
	gr.Process.SetExternalCallFn(pres.CallFn)
	gr.Process.SetExternalCallFnGameThread(pres.CallFnGameThread)
	gr.Process.SetExternalWriteMem(pres.WriteMem)
	// GID-6 ROP read wiring. The hook itself is always registered; whether
	// reads actually route through it depends on (a) the ROP_READ env var
	// AND (b) a live pool-health check below. Enabling ROP_READ before the
	// pool has rsi/rdi/rcx pop gadgets + rep-movsb stalls Present on every
	// read as build_memcpy fails repeatedly, so callers should only opt in
	// after /debug/rop-scan returns a complete breakdown.
	gr.Process.SetExternalRopRead(pres.RopReadToScratch)
	// Batched ROP-read path: N (src, len) tuples in one Present round-trip.
	// Adapter bridges presenter.BatchReadEntry <-> memory.BatchReadEntry so
	// gamelib stays free of a presenter import.
	gr.Process.SetExternalBatchRead(func(entries []memory.BatchReadEntry) ([][]byte, error) {
		conv := make([]presenter.BatchReadEntry, len(entries))
		for i, e := range entries {
			conv[i] = presenter.BatchReadEntry{Src: e.Src, Len: e.Len}
		}
		return pres.RopReadBatch(conv)
	})
	// Multi-slot pipelined path — the pump uses this when set so multiple
	// slot workers dispatch in parallel instead of serialising on the
	// single CMD_ROP_READ_BATCH flag.
	gr.Process.SetExternalSlotBatchRead(func(slot int, entries []memory.BatchReadEntry) ([][]byte, error) {
		conv := make([]presenter.BatchReadEntry, len(entries))
		for i, e := range entries {
			conv[i] = presenter.BatchReadEntry{Src: e.Src, Len: e.Len}
		}
		return pres.RopReadSlotBatch(slot, conv)
	})
	if os.Getenv("ROP_READ") == "1" {
		// ROP_READ was found to be architecturally wrong for per-read use:
		// each CMD_ROP_READ round-trips through a Present frame (~16 ms at
		// 60 fps), and live trace showed the bot issues ~2400 reads per
		// GetData tick. 2400 × 16 ms = 38 s/tick — unusable.
		//
		// ROP_READ=batch is the fix — CMD_ROP_READ_BATCH serves 1..64 reads
		// per round-trip. Bot collects the tick's addresses, issues one
		// BatchReadBytes, then serves individual reads from the returned
		// slices. Ceiling (how many entries before Arxan trips) is measured
		// live; default ships conservative.
		if os.Getenv("ROP_READ") == "force" {
			gr.Process.EnableRopRead(true)
			s.bot.ctx.Logger.Info("ROP_READ: ACTIVE (force mode — reads slow, diagnostic only)")
		} else {
			s.bot.ctx.Logger.Info("ROP_READ: infra available via /debug/rop-read; hot path stays on RPM (per-read Present round-trip is too slow)")
		}
	}
	if os.Getenv("ROP_READ") == "batch" {
		gr.Process.EnableBatchRead(true)
		s.bot.ctx.Logger.Info("ROP_READ=batch: CMD_ROP_READ_BATCH active; GameReader.GetData will group hot-path reads")
	}
	// ROP_READ=full: belt-and-suspenders. Batch path serves the clusters
	// that were explicitly converted; single-shot CMD_ROP_READ catches
	// EVERYTHING else (ReadBytesFromMemory / ReadUInt / ReadString). Result
	// is zero cross-process NtReadVirtualMemory from app.exe — every D2R
	// byte comes out of rmod's in-process copy. Slower per-read than RPM
	// (~5-15 ms Present round-trip vs ~microseconds) but the "no RPM"
	// behavior Bartek asked for.
	if os.Getenv("ROP_READ") == "full" {
		gr.Process.EnableBatchRead(true)
		gr.Process.EnableRopRead(true)
		s.bot.ctx.Logger.Info("ROP_READ=full: CMD_ROP_READ_BATCH + CMD_ROP_READ single-shot — every read routes through rmod, zero external RPM")
	}
	// Keep classic APC for SendPacket (game-state opcodes like 0x3C).
	// UI sender: DON'T use rmod (render thread crashes D2R).
	// Instead, Process.SendUIPacket falls back to APC-based SendUIPacketViaMainThread.
	// gr.Process.SetExternalSender(pres.SendPacket)
	// gr.Process.SetExternalUISender(pres.SendUIPacket) // DISABLED: render thread crash
	gr.Process.SetExternalDualSender(pres.SendDualPacket)
	gr.Process.SetExternalPostKey(func(vk byte) error {
		return gi.PostKeyInProcess(uintptr(hwnd), vk)
	})
	if realClickFn != 0 {
		gr.Process.SetExternalClick(func(x, y int32, btn byte) error {
			return pres.ClickAt(x, y, presenter.ClickButton(btn))
		})
		gr.Process.SetExternalForceClick(func(x, y int32) error {
			return pres.ForceClick(x, y)
		})
	}
	forceMoveAddr := gr.Process.GetModuleBase() + 0x19D25B4 + 0x49C + 4
	pres.SetForceMoveAddr(forceMoveAddr)
	s.bot.ctx.Logger.Info("Claude mode: presenter initialized (rmod.dll injected, all send paths)")

	// P1-GID snapshot wiring is opt-in via SNAPSHOT_ENABLE=1 AND MODE2=1.
	// Phase 1 (CLAUDE_MODE) is for game entry only and must NEVER trigger
	// snapshot init (game state not stable until in-area; AV storm in walker
	// → Arxan VEH overflow → STATUS_STACK_OVERFLOW). Phase 2 (MODE2=1) is
	// the post-attach steady state where snapshot init runs.
	if os.Getenv("SNAPSHOT_ENABLE") != "1" || os.Getenv("MODE2") != "1" {
		s.bot.ctx.Logger.Info("Claude mode: skipping snapshot init (require MODE2=1 AND SNAPSHOT_ENABLE=1 — presenter-only mode)")
		// Auto-unlock cursor in Claude mode so user can interact with D2R.
		if s.bot.ctx.MemoryInjector != nil {
			if err := s.bot.ctx.MemoryInjector.DisableCursorOverride(); err != nil {
				s.bot.ctx.Logger.Warn("Claude mode: failed to auto-unlock cursor", "error", err)
			} else {
				s.bot.ctx.Logger.Info("Claude mode: cursor auto-unlocked")
			}
		}
		return
	}

	// P1-GID Phase B0: wire in-process PlayerUnit snapshot. Hard-fail per
	// P1_GID_PLAN.md user directive — no silent regression to RPM — but with
	// a clean teardown so rmod restores Present before we exit. Skipping the
	// teardown is what produced the "HasExited but VM reboot required"
	// zombies documented in Desktop/reports/RESUME_AFTER_REBOOT_4.md.
	unitTableVA, expansionVA, waypointVA := gr.GameReader.SnapshotInitOffsets()
	failSnapshot := func(msg string) {
		s.bot.ctx.Logger.Error(msg)
		if uerr := pres.UninstallDetour(); uerr != nil {
			s.bot.ctx.Logger.Warn("uninstall detour during hard-fail",
				slog.Any("error", uerr))
		}
		os.Exit(1)
	}
	// Phase B2: publish simple static regions BEFORE init.
	if setErr := pres.WriteSnapshotStatics(gr.GameReader.SnapshotStaticEntries()); setErr != nil {
		failSnapshot(fmt.Sprintf("snapshot statics write failed: %v", setErr))
	}
	if snapErr := pres.SnapshotInit(unitTableVA, expansionVA, waypointVA); snapErr != nil {
		failSnapshot(fmt.Sprintf("snapshot init failed: %v", snapErr))
	}
	sr := memory.NewSnapshotReader(pres.LocalView(), uintptr(presenter.SharedBufSize))
	// Soft-fallback to RPM for walker-coverage gaps during bootstrap.
	sr.SetFallback(gr.Process)
	if tickErr := sr.WaitForFirstTick(3 * time.Second); tickErr != nil {
		failSnapshot(fmt.Sprintf("snapshot tick never advanced: %v", tickErr))
	}
	gr.GameReader.AttachSnapshot(sr)
	s.bot.ctx.Logger.Info("P1-GID snapshot attached; memory reads now in-process via SHM",
		slog.Uint64("tick", sr.Tick()),
		slog.Uint64("regions", uint64(sr.RegionCount())))

	// Auto-unlock cursor in Claude mode so user can interact with D2R.
	if s.bot.ctx.MemoryInjector != nil {
		if err := s.bot.ctx.MemoryInjector.DisableCursorOverride(); err != nil {
			s.bot.ctx.Logger.Warn("Claude mode: failed to auto-unlock cursor", "error", err)
		} else {
			s.bot.ctx.Logger.Info("Claude mode: cursor auto-unlocked")
		}
	}
}
