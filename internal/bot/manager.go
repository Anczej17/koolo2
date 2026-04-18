package bot

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"local/internal/svc/cmd/app/log"
	"local/internal/svc/internal/character"
	"local/internal/svc/internal/config"
	"local/internal/svc/internal/context"
	"local/internal/svc/internal/drop"
	"local/internal/svc/internal/event"
	"local/internal/svc/internal/game"
	"local/internal/svc/internal/gamelib/memory"
	"local/internal/svc/internal/health"
	"local/internal/svc/internal/mule"
	"local/internal/svc/internal/ntapi"
	"local/internal/svc/internal/pather"
	"local/internal/svc/internal/presenter"
	"local/internal/svc/internal/utils"
	"local/internal/svc/internal/utils/winproc"
	"github.com/lxn/win"
)

type SupervisorManager struct {
	mu             sync.RWMutex
	logger         *slog.Logger
	supervisors    map[string]Supervisor
	crashDetectors map[string]*game.CrashDetector
	eventListener  *event.Listener
	Drop           *drop.Service // Drop: Service façade to manage Drop domain
}

func NewSupervisorManager(logger *slog.Logger, eventListener *event.Listener) *SupervisorManager {

	GetPartyRegistry().SetLogger(logger)

	return &SupervisorManager{
		logger:         logger,
		supervisors:    make(map[string]Supervisor),
		crashDetectors: make(map[string]*game.CrashDetector),
		eventListener:  eventListener,
		Drop:           drop.NewService(logger),
	}
}

func (mng *SupervisorManager) AvailableSupervisors() []string {
	availableSupervisors := make([]string, 0)
	for name := range config.GetCharacters() {
		if name != "template" {
			availableSupervisors = append(availableSupervisors, name)
		}
	}

	return availableSupervisors
}

// StartClaude attaches to an existing D2R process and initializes the bot in
// Claude mode: no workflow, no automation — just keeps subsystems alive so the
// PacketSender can be driven via /debug/* HTTP endpoints.
//
// Caller MUST run this in a goroutine — it blocks for the supervisor's lifetime.
func (mng *SupervisorManager) StartClaude(supervisorName string, pid, hwnd uint32) error {
	return mng.startInternal(supervisorName, true, false, true, pid, hwnd)
}

// StartClaudeLaunch launches a fresh D2R process for the character and attaches
// the bot in Claude mode (no workflow, no automation, just packet experiment harness).
//
// Caller MUST run this in a goroutine — it blocks for the supervisor's lifetime.
func (mng *SupervisorManager) StartClaudeLaunch(supervisorName string) error {
	return mng.startInternal(supervisorName, false, false, true, 0, 0)
}

func (mng *SupervisorManager) Start(supervisorName string, attachToExisting bool, manualMode bool, pidHwnd ...uint32) error {
	var pid, hwnd uint32
	if len(pidHwnd) >= 2 {
		pid, hwnd = pidHwnd[0], pidHwnd[1]
	}
	return mng.startInternal(supervisorName, attachToExisting, manualMode, false, pid, hwnd)
}

func (mng *SupervisorManager) startInternal(supervisorName string, attachToExisting bool, manualMode bool, claudeMode bool, pid uint32, hwnd uint32) error {
	// Avoid multiple instances of the supervisor - shitstorm prevention
	mng.mu.RLock()
	_, exists := mng.supervisors[supervisorName]
	mng.mu.RUnlock()
	if exists {
		return fmt.Errorf("supervisor %s is already running", supervisorName)
	}

	// buildSupervisor reads CLAUDE_MODE to decide between rmod.dll and
	// rmod_sniffer.dll for the in-process presenter. Set the env var now,
	// BEFORE buildSupervisor runs — setting it later leaves the wrong DLL
	// injected and the sniffer's Present hook gets overridden.
	if claudeMode {
		os.Setenv("CLAUDE_MODE", "1")
	}

	// Reload config to get the latest local changes before starting the supervisor
	err := config.Load()
	if err != nil {
		return fmt.Errorf("error loading config: %w", err)
	}

	supervisorLogger, err := log.NewLogger(config.App.Debug.Log, config.App.LogSaveDirectory, supervisorName)
	if err != nil {
		return err
	}

	var optionalPID uint32
	var optionalHWND win.HWND

	if attachToExisting {
		if pid != 0 && hwnd != 0 {
			mng.logger.Info("Attaching to existing game", "pid", pid, "hwnd", hwnd)
			optionalPID = pid
			optionalHWND = win.HWND(hwnd)
		} else {
			return fmt.Errorf("pid and hwnd are required when attaching to an existing game")
		}
	}

	supervisor, crashDetector, err := mng.buildSupervisor(supervisorName, supervisorLogger, attachToExisting, optionalPID, optionalHWND)
	if err != nil {
		return err
	}

	// Set mode flags
	ctx := supervisor.GetContext()
	if ctx != nil {
		if claudeMode {
			ctx.ClaudeModeActive = true
			os.Setenv("CLAUDE_MODE", "1")
			supervisorLogger.Info("Claude mode enabled")
		} else if manualMode {
			ctx.ManualModeActive = true
			supervisorLogger.Info("Manual mode enabled")
		} else {
			ctx.ManualModeActive = false
			supervisorLogger.Info("Normal mode enabled")
		}
	}

	mng.mu.Lock()
	if oldCrashDetector, exists := mng.crashDetectors[supervisorName]; exists {
		oldCrashDetector.Stop() // Stop the old crash detector if it exists
	}
	mng.supervisors[supervisorName] = supervisor
	mng.crashDetectors[supervisorName] = crashDetector
	mng.mu.Unlock()

	if config.App.GameWindowArrangement {
		go func() {
			// When the game starts, its doing some weird stuff like repositioning and resizing window automatically
			// we need to wait until this is done in order to reposition, or it will be overridden
			time.Sleep(time.Second * 5)
			mng.rearrangeWindows()
		}()
	}

	// Start the Crash Detector in a thread to avoid blocking and speed up start
	go crashDetector.Start()

	err = supervisor.Start()
	if err != nil {
		mng.logger.Error(fmt.Sprintf("error running supervisor %s: %s", supervisorName, err.Error()))
	}

	return nil
}

func (mng *SupervisorManager) ReloadConfig() error {
	// Clear NIP rules cache so edited files are picked up
	config.ClearNIPCache()

	// Load fresh configs
	if err := config.Load(); err != nil {
		return err
	}

	// Apply new configs to running supervisors
	mng.mu.RLock()
	supervisorsCopy := make(map[string]Supervisor, len(mng.supervisors))
	for k, v := range mng.supervisors {
		supervisorsCopy[k] = v
	}
	mng.mu.RUnlock()
	for name, sup := range supervisorsCopy {
		newCfg, exists := config.GetCharacter(name)
		if !exists {
			continue
		}

		ctx := sup.GetContext()
		if ctx == nil {
			continue
		}

		// Preserve volatile runtime fields that are set by event handlers at runtime
		// and would be wiped by a full struct overwrite from disk config.
		savedGameName := ctx.CharacterCfg.Companion.CompanionGameName
		savedGamePassword := ctx.CharacterCfg.Companion.CompanionGamePassword
		savedPublicGameCounter := ctx.CharacterCfg.Game.PublicGameCounter

		// Update the config
		*ctx.CharacterCfg = *newCfg

		// Restore runtime fields
		if savedGameName != "" {
			ctx.CharacterCfg.Companion.CompanionGameName = savedGameName
			ctx.CharacterCfg.Companion.CompanionGamePassword = savedGamePassword
		}
		ctx.CharacterCfg.Game.PublicGameCounter = savedPublicGameCounter
	}

	return nil
}

func (mng *SupervisorManager) StopAll() {
	mng.mu.RLock()
	names := make([]string, 0, len(mng.supervisors))
	for name := range mng.supervisors {
		names = append(names, name)
	}
	mng.mu.RUnlock()
	for _, name := range names {
		mng.Stop(name)
	}
}

func (mng *SupervisorManager) Stop(supervisor string) {
	mng.mu.RLock()
	s, found := mng.supervisors[supervisor]
	cd := mng.crashDetectors[supervisor]
	mng.mu.RUnlock()

	if found {
		// Log the stop sequence
		mng.logger.Info("Stopping supervisor instance", slog.String("supervisor", supervisor))

		// Stop the Supervisor's internal loops and kill the client if configured
		s.Stop()

		// Stop the crash detector associated with it
		if cd != nil {
			cd.Stop()
		}

		// Delete from the list of active Supervisors
		mng.mu.Lock()
		delete(mng.supervisors, supervisor)
		delete(mng.crashDetectors, supervisor)
		mng.mu.Unlock()

		// The logic to start the next character has been removed from here.
		// The restartFunc is now the single source of truth for this,
		// preventing the mule from restarting itself.
	}
}

func (mng *SupervisorManager) TogglePause(supervisor string) {
	mng.mu.RLock()
	s, found := mng.supervisors[supervisor]
	mng.mu.RUnlock()
	if found {
		s.TogglePause()
	}
}

func (mng *SupervisorManager) Status(characterName string) Stats {
	mng.mu.RLock()
	sup, found := mng.supervisors[characterName]
	mng.mu.RUnlock()
	if found {
		return sup.Stats()
	}
	return Stats{}
}

func (mng *SupervisorManager) GetData(characterName string) *game.Data {
	mng.mu.RLock()
	sup, found := mng.supervisors[characterName]
	mng.mu.RUnlock()
	if found {
		return sup.GetData()
	}
	return nil
}

func (mng *SupervisorManager) GetContext(characterName string) *context.Context {
	mng.mu.RLock()
	sup, found := mng.supervisors[characterName]
	mng.mu.RUnlock()
	if found {
		return sup.GetContext()
	}
	return nil
}

func (mng *SupervisorManager) GetSupervisor(supervisor string) Supervisor {
	mng.mu.RLock()
	defer mng.mu.RUnlock()
	if sup, ok := mng.supervisors[supervisor]; ok {
		return sup
	}
	return nil
}

func (mng *SupervisorManager) buildSupervisor(supervisorName string, logger *slog.Logger, attach bool, optionalPID uint32, optionalHWND win.HWND) (Supervisor, *game.CrashDetector, error) {
	cfg, found := config.GetCharacter(supervisorName)
	if !found {
		return nil, nil, fmt.Errorf("character %s not found", supervisorName)
	}

	var pid uint32
	var hwnd win.HWND

	if attach {
		if optionalPID != 0 && optionalHWND != 0 {
			pid = optionalPID
			hwnd = optionalHWND
		} else {
			return nil, nil, fmt.Errorf("pid and hwnd are required when attaching to an existing game")
		}
	} else {
		var err error
		if kbResult, kbErr := config.EnsureSkillKeyBindings(cfg, config.App.UseCustomSettings); kbErr != nil {
			logger.Warn("Failed to ensure skill key bindings", slog.Any("error", kbErr))
		} else if kbResult.Missing {
			logger.Info("Key binding file missing; will bootstrap in-game", slog.String("character", cfg.CharacterName))
		}
		pid, hwnd, err = game.StartGame(cfg.Username, cfg.Password, cfg.AuthMethod, cfg.AuthToken, cfg.Realm, cfg.CommandLineArgs, config.App.UseCustomSettings)
		if err != nil {
			return nil, nil, fmt.Errorf("error starting game: %w", err)
		}
	}

	gr, err := game.NewGameReader(cfg, supervisorName, pid, hwnd, logger)
	if err != nil {
		return nil, nil, fmt.Errorf("error creating game reader: %w", err)
	}

	gi, err := game.InjectorInit(logger, gr.GetPID())
	if err != nil {
		return nil, nil, fmt.Errorf("error creating game injector: %w", err)
	}

	// Present hook — Phase 8. Requires absolute path for LoadLibraryW in D2R.
	func() {
		defer func() {
			if r := recover(); r != nil {
				logger.Error(fmt.Sprintf("PANIC in presenter init: %v", r))
			}
		}()
		presenterDLLName := "rmod.dll"
		// In Claude mode, use the sniffer-enabled DLL that includes
		// in-process buf0/buf1 polling with zero detection surface.
		if os.Getenv("CLAUDE_MODE") == "1" {
			if _, err := os.Stat(filepath.Join("tools", "rmod_sniffer.dll")); err == nil {
				presenterDLLName = "rmod_sniffer.dll"
				logger.Info("Claude mode: using sniffer-enabled DLL")
			}
		}
		presenterDLLPath := filepath.Join("tools", presenterDLLName)
		if absPath, err := filepath.Abs(presenterDLLPath); err == nil {
			presenterDLLPath = absPath
		}
		if _, statErr := os.Stat(presenterDLLPath); statErr == nil {
			logger.Debug("init presenter", slog.Uint64("pid", uint64(pid)), slog.Uint64("grPid", uint64(gr.GetPID())))
			fnSendPacket, fnErr := gr.Process.GetD2GSSendPacketFn()
			if fnErr != nil {
				logger.Warn("could not resolve dispatch function, using legacy path", slog.Any("error", fnErr))
			} else {
				// UI NetMan global RVA — required for identify/buy/sell/cube/gamble.
				// RE'd from D2R image: UI NetMan global @ base + 0x19ED860 (vtable[5]
				// of struct = the queue inserter at FUN_7ff79d8b81b0).
				const uiNetManRVA uintptr = 0x19ED860
				uiNetManAddr := gr.Process.ModuleBaseAddress() + uiNetManRVA

				// Mirror buffer RVA — for dual-send vendor path.
				// RE'd 2026-04-10: D2R's wrapper at ~RVA 0x117500 does memcpy to
				// this global before calling send_fn. Resolved from:
				//   lea rcx, [rip + 0x1e09cf0] at RVA 0x117639 → target RVA 0x1F21330
				// Mirror buffer used by D2R's dual_send_wrap (vendor trade path).
				// Verified live 2026-04-12 via memdiff capture — dual_send_wrap at
				// RVA 0x147639 does `LEA RCX, [rip+disp]` where the target is this
				// global. The value was 0x1F21330 prior to a recent D2R patch that
				// shifted it by +0x30000. See logs/capture2_analysis_2026-04-12.md
				// for the full decode. TODO: resolve this dynamically via a sigscan
				// that disassembles dual_send_wrap at startup so the next patch
				// can't silently break the dual-send path again.
				const mirrorBufRVA uintptr = 0x1F51330
				mirrorBufAddr := gr.Process.ModuleBaseAddress() + mirrorBufRVA

				// Phase 9: in-process click via real_click_worker.
				// Failure here is non-fatal — the legacy HID path still works.
				var realClickFn uintptr
				if rcFn, rcErr := gr.Process.GetRealClickWorkerFn(); rcErr == nil {
					realClickFn = rcFn
				} else {
					logger.Warn("could not resolve real_click_worker; in-process click disabled", slog.Any("error", rcErr))
				}

				logger.Debug("resolved runtime addresses",
					slog.Uint64("send", uint64(fnSendPacket)),
					slog.Uint64("ui", uint64(uiNetManAddr)),
					slog.Uint64("click", uint64(realClickFn)),
					slog.Uint64("mirror", uint64(mirrorBufAddr)))
				// Claude mode delegates pres.Init to single_supervisor.initClaudePresenter
				// (runs after game entry). Two pres.Init calls inject two rmod images
				// whose Present detours chain — image_2's UninstallDetour then restores
				// image_1's JMP back instead of the original Present prologue, so
				// SnapshotInit acks time out and D2R dies. Normal/Manual modes still
				// get their presenter here (they never reach initClaudePresenter).
				if os.Getenv("MODE2") == "1" && os.Getenv("CLAUDE_MODE") != "1" {
					pres := presenter.New(pid, presenterDLLPath)
					if initErr := pres.Init(fnSendPacket, uiNetManAddr, realClickFn, uintptr(hwnd), mirrorBufAddr); initErr != nil {
						logger.Error("runtime module init FAILED — falling through to APC path", slog.Any("error", initErr))
					} else {
						gi.SetPresenter(pres)
						gr.Process.SetExternalCallFn(pres.CallFn)
						gr.Process.SetExternalWriteMem(pres.WriteMem)
						forceMoveAddr := gr.Process.GetModuleBase() + 0x19D25B4 + 0x49C + 4
						pres.SetForceMoveAddr(forceMoveAddr)
						if os.Getenv("MODE2") == "1" {
							gr.Process.SetExternalSender(pres.SendPacket)
							gr.Process.SetExternalUISender(pres.SendUIPacket)
							gr.Process.SetExternalDualSender(pres.SendDualPacket)
							gr.Process.SetExternalClick(func(x, y int32, btn byte) error {
								return pres.ClickAt(x, y, presenter.ClickButton(btn))
							})
							gr.Process.SetExternalForceClick(func(x, y int32) error {
								return pres.ForceClick(x, y)
							})
							logger.Info("runtime module: Present hook path enabled for packet sending")
						}
						logger.Info("runtime module initialized")

						// P1-GID Phase B0: wire in-process PlayerUnit snapshot.
						// Opt-in via SNAPSHOT_ENABLE=1 — default off because Phase-2
						// dup-injection (manager.go pres.Init + single_supervisor
						// initClaudePresenter both call loadModule) creates two
						// rmod images in D2R; the install_detour G_TRAMPOLINE guard
						// is per-image and doesn't unify them, so SnapshotInit acks
						// time out and D2R dies. Live test 2026-04-17 16:28 confirmed.
						// Re-enable after dup-injection is fixed (SetPresenter check).
						if os.Getenv("SNAPSHOT_ENABLE") != "1" {
							logger.Info("MODE2: skipping snapshot init (SNAPSHOT_ENABLE not set — presenter-only)")
						} else {
							unitTableVA, expansionVA, waypointVA := gr.GameReader.SnapshotInitOffsets()
							// Hard-fail semantics per P1_GID_PLAN.md, but clean teardown
							// so rmod restores Present before we exit — otherwise D2R
							// zombies (see Desktop/reports/RESUME_AFTER_REBOOT_4.md).
							failSnapshot := func(msg string) {
								logger.Error(msg)
								if uerr := pres.UninstallDetour(); uerr != nil {
									logger.Warn("uninstall detour during hard-fail",
										slog.Any("error", uerr))
								}
								os.Exit(1)
							}
							if setErr := pres.WriteSnapshotStatics(gr.GameReader.SnapshotStaticEntries()); setErr != nil {
								failSnapshot(fmt.Sprintf("snapshot statics write failed: %v", setErr))
							}
							if snapErr := pres.SnapshotInit(unitTableVA, expansionVA, waypointVA); snapErr != nil {
								failSnapshot(fmt.Sprintf("snapshot init failed: %v", snapErr))
							}
							sr := memory.NewSnapshotReader(pres.LocalView(), uintptr(presenter.SharedBufSize))
							if tickErr := sr.WaitForFirstTick(3 * time.Second); tickErr != nil {
								failSnapshot(fmt.Sprintf("snapshot tick never advanced: %v", tickErr))
							}
							gr.GameReader.AttachSnapshot(sr)
							logger.Info("P1-GID snapshot attached; bot memory reads now in-process via SHM",
								slog.Uint64("tick", sr.Tick()),
								slog.Uint64("regions", uint64(sr.RegionCount())),
								slog.Uint64("data_bytes", uint64(sr.DataBytes())))
						}
					}
				}
				// Liveness watchdog: tight poll (200 ms) so we can snapshot the
				// in-process VEH crash record before the supervisor restart
				// tears down the presenter (and its SHM mapping).
				go func(watchPID uint32, gi *game.MemoryInjector) {
					defer func() {
						if r := recover(); r != nil {
							logger.Error(fmt.Sprintf("D2R watchdog PANIC: %v", r))
						}
					}()
					const query = 0x0400 // PROCESS_QUERY_LIMITED_INFORMATION
					h, err := ntapi.OpenProcess(query, watchPID)
					if err != nil {
						logger.Warn("D2R watchdog: could not open process", slog.Any("error", err))
						return
					}
					defer ntapi.CloseHandle(h)

					// Cache D2R's module list now so ResolveAddress can map
					// crash frames to "module+0xRVA" strings.
					if err := RefreshD2RModules(watchPID); err != nil {
						logger.Warn("D2R watchdog: module refresh failed", slog.Any("error", err))
					}

					tick := time.NewTicker(200 * time.Millisecond)
					defer tick.Stop()
					var exit uint32
					var sec int
					for range tick.C {
						if err := windows.GetExitCodeProcess(h, &exit); err != nil {
							logger.Error("D2R watchdog: GetExitCodeProcess failed — D2R gone", slog.Any("error", err), slog.Uint64("pid", uint64(watchPID)))
							snapshotAndLogCrashDiag(gi, logger, watchPID)
							return
						}
						if exit != 259 { // STILL_ACTIVE
							logger.Error(fmt.Sprintf("D2R watchdog: D2R EXITED pid=%d exitCode=%#x (%d)", watchPID, exit, exit))
							snapshotAndLogCrashDiag(gi, logger, watchPID)
							return
						}
						sec++
						if sec%25 == 0 { // ~5 s heartbeat
							logger.Info(fmt.Sprintf("D2R watchdog: alive pid=%d", watchPID))
						}
					}
				}(pid, gi)
			}
		}
	}()

	ctx := context.NewContext(supervisorName)

	hidM := game.NewHID(gr, gi)
	pf := pather.NewPathFinder(gr, ctx.Data, hidM, cfg)

	bm := health.NewBeltManager(ctx.Data, hidM, logger, supervisorName)
	hm := health.NewHealthManager(bm, ctx.Data)

	ctx.CharacterCfg = cfg
	ctx.EventListener = mng.eventListener
	ctx.HID = hidM
	ctx.PacketSender = game.NewPacketSender(gr.Process)
	ctx.Logger = logger
	ctx.Manager = game.NewGameManager(gr, hidM, supervisorName)
	ctx.GameReader = gr
	ctx.MemoryInjector = gi
	ctx.PathFinder = pf
	pf.SetPacketSender(ctx.PacketSender)
	ctx.BeltManager = bm
	ctx.HealthManager = hm
	char, err := character.BuildCharacter(ctx.Context)
	if err != nil {
		return nil, nil, fmt.Errorf("error creating character: %w", err)
	}
	ctx.Char = char

	muleManager := mule.NewManager(logger)
	bot := NewBot(ctx.Context, muleManager)

	statsHandler := NewStatsHandler(supervisorName, logger)

	// Register per-supervisor handlers keyed by name — replaces any stale handlers
	// from a previous incarnation of this supervisor, preventing handler accumulation.
	companionHandler := NewCompanionEventHandler(supervisorName, logger, cfg)
	mng.eventListener.RegisterKeyed(supervisorName, statsHandler.Handle, companionHandler.Handle)

	// Seed companion game info from party registry after restart.
	// Events sent while this supervisor was restarting (handler unregistered) are lost,
	// so check the registry for the leader's current game as a fallback.
	if cfg.Companion.Enabled && !cfg.Companion.Leader && cfg.Companion.WaitForParty {
		if activeGame := GetPartyRegistry().GetActiveGame(); activeGame != nil {
			leaderOK := cfg.Companion.LeaderName == "" ||
				cfg.Companion.LeaderName == activeGame.LeaderName
			if leaderOK && cfg.Companion.CompanionGameName == "" {
				cfg.Companion.CompanionGameName = activeGame.GameName
				cfg.Companion.CompanionGamePassword = activeGame.Password
				logger.Info("Seeded companion game info from party registry after restart",
					slog.String("game", activeGame.GameName),
					slog.String("leader", activeGame.LeaderName))
			}
		}
	}

	supervisor, err := NewSinglePlayerSupervisor(supervisorName, bot, statsHandler)

	if err != nil {
		return nil, nil, err
	}

	supervisor.GetContext().StopSupervisorFn = supervisor.Stop

	// Drop: Attach Drop manager to Drop service (filters, callbacks, queued requests)
	if mng.Drop != nil {
		if ctx.Drop == nil {
			ctx.Drop = drop.NewManager(supervisorName, logger)
		}
		mng.Drop.AttachManager(supervisorName, ctx.Drop)
	}

	// This function will be used to restart the client - passed to the crashDetector
	restartFunc := func() {

		ctx := supervisor.GetContext()

		// Manual mode: just stop, don't restart
		if ctx.ManualModeActive {
			mng.logger.Info("Manual mode: D2R closed, stopping without restart", slog.String("supervisor", supervisorName))
			ctx.ManualModeActive = false // Clear the flag before stopping
			mng.Stop(supervisorName)
			return
		}

		if ctx.CleanStopRequested {
			if ctx.RestartWithCharacter != "" {
				mng.logger.Info("Supervisor requested restart with different character",
					slog.String("from", supervisorName),
					slog.String("to", ctx.RestartWithCharacter))
				nextCharacter := ctx.RestartWithCharacter
				mng.Stop(supervisorName)
				time.Sleep(5 * time.Second) // Wait before starting new character
				if err := mng.Start(nextCharacter, false, false); err != nil {
					mng.logger.Error("Failed to start next character",
						slog.String("character", nextCharacter),
						slog.String("error", err.Error()))
				}
				return
			}
			mng.logger.Info("Supervisor stopped cleanly by game logic. Preventing restart.", slog.String("supervisor", supervisorName))
			mng.Stop(supervisorName)
			return
		}

		wasClaudeMode := false
		if sup, ok := mng.supervisors[supervisorName]; ok {
			if ctx := sup.GetContext(); ctx != nil {
				wasClaudeMode = ctx.ClaudeModeActive
			}
		}

		mng.logger.Info("Restarting supervisor after crash",
			slog.String("supervisor", supervisorName),
			slog.Bool("claudeMode", wasClaudeMode))
		mng.Stop(supervisorName)
		time.Sleep(5 * time.Second) // Wait a bit before restarting

		// Get a list of all available Supervisors
		supervisorList := mng.AvailableSupervisors()

		for {

			// Set the default state
			tokenAuthStarting := false

			// Get the current supervisor's config
			supCfg, _ := config.GetCharacter(supervisorName)

			for _, sup := range supervisorList {

				// If the current don't check against the one we're trying to launch
				if sup == supervisorName {
					continue
				}

				if mng.GetSupervisorStats(sup).SupervisorStatus == Starting {
					if supCfg.AuthMethod == "TokenAuth" {
						tokenAuthStarting = true
						mng.logger.Info("Waiting before restart as another client is already starting and we're using token auth", slog.String("supervisor", sup))
						break
					}

					sCfg, found := config.GetCharacter(sup)
					if found {
						if sCfg.AuthMethod == "TokenAuth" {
							// A client that uses token auth is currently starting, hold off restart
							tokenAuthStarting = true
							mng.logger.Info("Waiting before restart as a client that's using token auth is already starting", slog.String("supervisor", sup))
							break
						}
					}
				}
			}

			if !tokenAuthStarting {
				break
			}

			// Wait 5 seconds before checking again
			utils.Sleep(5000)
		}

		gameTitle := supervisorName
		// Take the *uint16 into a local so the GC keeps the backing array
		// alive across the lazy-DLL call. Go special-cases uintptr(unsafe.Pointer(x))
		// in call arg lists, but this pattern has crashed with rip near-zero on
		// the Hyper-V VM — likely a lazy-DLL resolution interacting with GC. Keep
		// the local + explicit runtime.KeepAlive below.
		titlePtr, _ := syscall.UTF16PtrFromString(gameTitle)
		winproc.SetWindowText.Call(uintptr(hwnd), uintptr(unsafe.Pointer(titlePtr)))
		runtime.KeepAlive(titlePtr)

		var err error
		if wasClaudeMode {
			err = mng.StartClaudeLaunch(supervisorName)
		} else {
			err = mng.Start(supervisorName, false, false)
		}
		if err != nil {
			mng.logger.Error("Failed to restart supervisor", slog.String("supervisor", supervisorName), slog.String("Error: ", err.Error()))
		}
	}

	gameTitle := supervisorName
	winproc.SetWindowText.Call(uintptr(hwnd), uintptr(unsafe.Pointer(syscall.StringToUTF16Ptr(gameTitle))))
	crashDetector := game.NewCrashDetector(supervisorName, int32(pid), uintptr(hwnd), mng.logger, restartFunc)

	return supervisor, crashDetector, nil
}

// Drop: Expose Drop service for server wiring
func (mng *SupervisorManager) DropService() *drop.Service {
	return mng.Drop
}

func (mng *SupervisorManager) GetSupervisorStats(supervisor string) Stats {
	mng.mu.RLock()
	sup := mng.supervisors[supervisor]
	mng.mu.RUnlock()
	if sup == nil {
		return Stats{}
	}
	return sup.Stats()
}

func (mng *SupervisorManager) rearrangeWindows() {
	width := win.GetSystemMetrics(0)
	height := win.GetSystemMetrics(1)
	var windowBorderX int32 = 2   // left + right window border is 2px
	var windowBorderY int32 = 40  // upper window border is usually 40px
	var windowOffsetX int32 = -10 // offset horizontal window placement by -10 pixel
	maxColumns := width / (1280 + windowBorderX)
	maxRows := height / (720 + windowBorderY)

	mng.logger.Debug(
		"Arranging windows",
		slog.String("displaywidth", strconv.FormatInt(int64(width), 10)),
		slog.String("displayheight", strconv.FormatInt(int64(height), 10)),
		slog.String("max columns", strconv.FormatInt(int64(maxColumns+1), 10)), // +1 as we are counting from 0
		slog.String("max rows", strconv.FormatInt(int64(maxRows+1), 10)),
	)

	mng.mu.RLock()
	supsCopy := make(map[string]Supervisor, len(mng.supervisors))
	for k, v := range mng.supervisors {
		supsCopy[k] = v
	}
	mng.mu.RUnlock()

	var column, row int32
	for _, sp := range supsCopy {
		// reminder that columns are vertical (they go up and down) and rows are horizontal (they go left and right)
		if column > maxColumns {
			column = 0
			row++
		}

		if row <= maxRows {
			sp.SetWindowPosition(int(column*(1280+windowBorderX)+windowOffsetX), int(row*(720+windowBorderY)))
			mng.logger.Debug(
				"Window Positions",
				slog.String("supervisor", sp.Name()),
				slog.String("column", strconv.FormatInt(int64(column), 10)),
				slog.String("row", strconv.FormatInt(int64(row), 10)),
				slog.String("position", strconv.FormatInt(int64(column*(1280+windowBorderX)+windowOffsetX), 10)+"x"+strconv.FormatInt(int64(row*(720+windowBorderY)), 10)),
			)
			column++
		} else {
			mng.logger.Debug("Window position of supervisor " + sp.Name() + " was not changed, no free space for it")
		}
	}
}


// LastCrashDiag is a package-level snapshot of the most recent in-process VEH
// capture. The watchdog writes here when D2R exits so /debug/crash-info can
// still serve the data after the supervisor has been torn down.
var (
	lastCrashDiagMu sync.RWMutex
	lastCrashDiag   = make(map[string]presenter.CrashDiag)
)

// GetLastCrashDiag returns the most recent crash record for the given
// supervisor (empty struct if none captured this session).
func GetLastCrashDiag(supervisor string) (presenter.CrashDiag, bool) {
	lastCrashDiagMu.RLock()
	defer lastCrashDiagMu.RUnlock()
	d, ok := lastCrashDiag[supervisor]
	return d, ok
}

// snapshotAndLogCrashDiag pulls the in-process VEH crash record (if rmod.dll
// captured anything) and dumps it to the bot log AND saves to the package
// snapshot map so the HTTP endpoint can still serve it after restart.
func snapshotAndLogCrashDiag(gi *game.MemoryInjector, logger *slog.Logger, pid uint32) {
	if gi == nil {
		return
	}
	pres := gi.GetPresenter()
	if pres == nil {
		return
	}
	d := pres.ReadCrashDiag()
	logger.Error(fmt.Sprintf("VEH crash record for pid=%d: valid=%t count=%d code=0x%08X tid=%d fault_type=%d rip=0x%016X (%s) fault_va=0x%016X rsp=0x%016X",
		pid, d.Valid, d.Count, d.Code, d.TID, d.FaultType, d.RIP, ResolveAddress(uintptr(d.RIP)), d.FaultVA, d.RSP))
	if d.Valid {
		logger.Error(fmt.Sprintf(
			"VEH regs: rax=0x%X rcx=0x%X rdx=0x%X rbx=0x%X rsp=0x%X rbp=0x%X rsi=0x%X rdi=0x%X r8=0x%X r9=0x%X r10=0x%X r11=0x%X r12=0x%X r13=0x%X r14=0x%X r15=0x%X",
			d.Regs[0], d.Regs[1], d.Regs[2], d.Regs[3], d.Regs[4], d.Regs[5], d.Regs[6], d.Regs[7],
			d.Regs[8], d.Regs[9], d.Regs[10], d.Regs[11], d.Regs[12], d.Regs[13], d.Regs[14], d.Regs[15]))
		for i, f := range d.Frames {
			logger.Error(fmt.Sprintf("VEH stack frame[%02d] = 0x%016X (%s)", i, f, ResolveAddress(uintptr(f))))
		}
	}
	// Snapshot under the supervisor name. We don't have it here directly so
	// stash by PID; the HTTP handler can iterate over the map and pick the
	// matching entry. Simpler: also store under "_last".
	lastCrashDiagMu.Lock()
	lastCrashDiag[fmt.Sprintf("pid=%d", pid)] = d
	lastCrashDiag["_last"] = d
	lastCrashDiagMu.Unlock()
}
