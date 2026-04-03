package bot

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"local/internal/svc/cmd/app/log"
	"local/internal/svc/internal/character"
	"local/internal/svc/internal/config"
	"local/internal/svc/internal/context"
	"local/internal/svc/internal/drop"
	"local/internal/svc/internal/event"
	"local/internal/svc/internal/game"
	"local/internal/svc/internal/health"
	"local/internal/svc/internal/mule"
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

func (mng *SupervisorManager) Start(supervisorName string, attachToExisting bool, manualMode bool, pidHwnd ...uint32) error {
	// Avoid multiple instances of the supervisor - shitstorm prevention
	mng.mu.RLock()
	_, exists := mng.supervisors[supervisorName]
	mng.mu.RUnlock()
	if exists {
		return fmt.Errorf("supervisor %s is already running", supervisorName)
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
		if len(pidHwnd) == 2 {
			mng.logger.Info("Attaching to existing game", "pid", pidHwnd[0], "hwnd", pidHwnd[1])
			optionalPID = pidHwnd[0]
			optionalHWND = win.HWND(pidHwnd[1])
		} else {
			return fmt.Errorf("pid and hwnd are required when attaching to an existing game")
		}
	}

	supervisor, crashDetector, err := mng.buildSupervisor(supervisorName, supervisorLogger, attachToExisting, optionalPID, optionalHWND)
	if err != nil {
		return err
	}

	// Set manual mode flag
	ctx := supervisor.GetContext()
	if ctx != nil {
		if manualMode {
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
		presenterDLLPath := filepath.Join("tools", "rmod.dll")
		if absPath, err := filepath.Abs(presenterDLLPath); err == nil {
			presenterDLLPath = absPath
		}
		if _, statErr := os.Stat(presenterDLLPath); statErr == nil {
			logger.Info(fmt.Sprintf("Phase8: pid=%d grPID=%d dll=%s", pid, gr.GetPID(), presenterDLLPath))
			fnSendPacket, fnErr := gr.Process.GetD2GSSendPacketFn()
			if fnErr != nil {
				logger.Warn("could not resolve dispatch function, using legacy path", slog.Any("error", fnErr))
			} else {
				logger.Info(fmt.Sprintf("Phase8: SendPacket=%#x, creating presenter for pid=%d", fnSendPacket, pid))
				pres := presenter.New(pid, presenterDLLPath)
				if initErr := pres.Init(fnSendPacket); initErr != nil {
					logger.Warn("runtime module init failed, using legacy path", slog.Any("error", initErr))
				} else {
					gr.Process.SetExternalSender(pres.SendPacket)
					gi.SetPresenter(pres)
					logger.Info("runtime module initialized, using Present hook path")
				}
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

		mng.logger.Info("Restarting supervisor after crash", slog.String("supervisor", supervisorName))
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
		winproc.SetWindowText.Call(uintptr(hwnd), uintptr(unsafe.Pointer(syscall.StringToUTF16Ptr(gameTitle))))

		err := mng.Start(supervisorName, false, false)
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
