package main

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/binary"
	"fmt"
	"log"
	"log/slog"
	"net"
	// pprof removed - exposes debug endpoints and goroutine stacks
	"os"
	"path/filepath"
	"runtime/debug"
	"syscall"
	"time"
	"unsafe"

	sloggger "local/internal/svc/cmd/app/log"
	"local/internal/svc/internal/bot"
	"local/internal/svc/internal/config"
	"local/internal/svc/internal/event"
	"local/internal/svc/internal/game"
	"local/internal/svc/internal/gamelib/memory"
	"local/internal/svc/internal/ntapi"
	"local/internal/svc/internal/remote/discord"
	"local/internal/svc/internal/remote/droplog"
	ngrokremote "local/internal/svc/internal/remote/ngrok"
	"local/internal/svc/internal/remote/telegram"
	"local/internal/svc/internal/server"
	"local/internal/svc/internal/utils"
	"local/internal/svc/internal/utils/winproc"
	"github.com/inkeliz/gowebview"
	"golang.org/x/sync/errgroup"
)

var (
	buildID   string
	buildTime string
)

// Window title pool — rotated periodically to avoid static-title fingerprinting.
// Pool entries chosen to look like ordinary Windows utilities. NEVER add the
// project name or any bot-related variant — the goal is process anonymity.
var windowTitlePool = []string{
	"Settings",
	"Calculator",
	"Notepad",
	"Task Manager",
	"Resource Monitor",
	"Event Viewer",
	"Registry Editor",
	"Disk Management",
	"System Information",
	"Device Manager",
	"Services",
	"Windows Security",
	"Performance Monitor",
	"Control Panel",
	"File Explorer",
	"Command Prompt",
	"Paint",
	"Clock",
	"Weather",
	"Camera",
}

// pickRandomTitle picks one entry from windowTitlePool using crypto-strong
// randomness (so the choice does not reveal a math/rand seed in the binary).
func pickRandomTitle() string {
	var b [8]byte
	_, _ = cryptorand.Read(b[:])
	v := binary.LittleEndian.Uint64(b[:])
	return windowTitlePool[v%uint64(len(windowTitlePool))]
}

// initialWindowTitle returns the first title set on window creation.
var initialWindowTitle = pickRandomTitle()

// findFreePort picks a random available TCP port by binding to :0
func findFreePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return port, nil
}

// wrapWithRecover wraps a function with panic recovery logic
func wrapWithRecover(logger *slog.Logger, f func() error) func() error {
	return func() error {
		defer func() {
			if r := recover(); r != nil {
				stackTrace := debug.Stack()
				errMsg := fmt.Sprintf("panic recovered: %v\nStacktrace: %s", r, stackTrace)
				logger.Error(errMsg)
				sloggger.FlushLog()
			}
		}()
		return f()
	}
}

func main() {

	_ = buildID
	_ = buildTime

	// Stealth RPM Layer default ON in production. Opt-out: STEALTH_READ=0.
	// Must run before any code path imports `internal/gamelib/memory` and
	// triggers stealthOnce (sync.Once gate inside StealthEnabled()).
	if os.Getenv("STEALTH_READ") == "" {
		os.Setenv("STEALTH_READ", "1")
	}

	// Capture stderr (panic stacks) to file. Without this, -H windowsgui swallows
	// all panic output and crashes are silent. Cheap insurance.
	if exe, err := os.Executable(); err == nil {
		stderrPath := filepath.Join(filepath.Dir(exe), "logs", "stderr.txt")
		if f, ferr := os.OpenFile(stderrPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644); ferr == nil {
			os.Stderr = f
			fmt.Fprintf(f, "\n=== dev_test start %s ===\n", time.Now().Format(time.RFC3339))
		}
	}

	// Init indirect syscalls, start anti-debug
	if ntErr := ntapi.Init(); ntErr != nil {
		log.Printf("Warning: NT syscall init failed, falling back to standard API: %v", ntErr)
	}
	// Disable the anti-debug monitor when SKIP_ANTIDEBUG=1 env var OR a sentinel
	// file `SKIP_ANTIDEBUG` exists next to the exe. The file route matters because
	// double-clicking dev_test.exe doesn't pick up env vars set in another shell.
	skipAntiDebug := os.Getenv("SKIP_ANTIDEBUG") == "1"
	if !skipAntiDebug {
		if exe, err := os.Executable(); err == nil {
			if _, statErr := os.Stat(filepath.Join(filepath.Dir(exe), "SKIP_ANTIDEBUG")); statErr == nil {
				skipAntiDebug = true
			}
		}
	}
	if skipAntiDebug {
		fmt.Fprintf(os.Stderr, "anti-debug monitor DISABLED (SKIP_ANTIDEBUG sentinel present)\n")
	} else {
		fmt.Fprintf(os.Stderr, "anti-debug monitor ENABLED (no SKIP_ANTIDEBUG sentinel)\n")
		ntapi.StartAntiDebugMonitor(30*time.Second, func() {
			fmt.Fprintf(os.Stderr, "anti-debug detected, exiting\n")
			os.Exit(0)
		})
	}

	// Reap zombie D2R processes from a prior run BEFORE config/logger so any
	// subsequent OpenProcess-by-name can't latch onto a dead PID. An Arxan
	// cascade leaves D2R in a state where taskkill cannot release it; our bot
	// (or a crashed predecessor) holds the handle that pins the EPROCESS.
	// See internal/game/handle_killer.go:ForceKillZombieD2R for details.
	if closed, zombies := game.ForceKillZombieD2R(nil); zombies > 0 {
		fmt.Fprintf(os.Stderr, "zombie_killer: closed=%d zombies=%d at startup\n", closed, zombies)
	}

	err := config.Load()
	if err != nil {
		utils.ShowDialog("Error loading configuration", err.Error())
		log.Fatalf("Error loading configuration: %s", err.Error())
		return
	}

	// Ensure a sensible default delay for Auto Start if not configured
	if config.App.AutoStart.DelaySeconds <= 0 {
		config.App.AutoStart.DelaySeconds = 60
	}

	// Pick a random available port for the HTTP server
	serverPort, portErr := findFreePort()
	if portErr != nil {
		serverPort = 8087 // fallback
	}

	logger, err := sloggger.NewLogger(config.App.Debug.Log, config.App.LogSaveDirectory, "")
	if err != nil {
		log.Fatalf("Error starting logger: %s", err.Error())
	}
	defer sloggger.FlushAndClose()

	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("fatal error detected, application will close with the following error: %v\n Stacktrace: %s", r, debug.Stack())
			logger.Error(err.Error())
			sloggger.FlushAndClose()
			utils.ShowDialog("Application error", fmt.Sprintf("Application will close due to an unexpected error, please check the latest log file for more info!\n %s", err.Error()))
		}
	}()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	g, ctx := errgroup.WithContext(ctx)

	winproc.SetProcessDpiAware.Call() // Set DPI awareness to be able to read the correct scale and show the window correctly

	eventListener := event.NewListener(logger)

	// Centralized droplog writer registration
	dropBase := config.App.LogSaveDirectory
	if dropBase == "" {
		dropBase = "logs"
	}
	dropDir := filepath.Join(dropBase, "droplogs")
	dropWriter := droplog.NewWriter(dropDir, logger)
	eventListener.Register(dropWriter.Handle)
	manager := bot.NewSupervisorManager(logger, eventListener)
	scheduler := bot.NewScheduler(manager, logger)
	go scheduler.Start()
	srv, err := server.New(logger, manager, scheduler)
	if err != nil {
		log.Fatalf("Error starting local server: %s", err.Error())
	}
	eventListener.Register(srv.HandleRunewordHistory)
	var ngrokTunnel *ngrokremote.Tunnel
	if config.App.Ngrok.Enabled {
		if config.App.Ngrok.Authtoken == "" && os.Getenv("NGROK_AUTHTOKEN") == "" {
			logger.Warn("ngrok enabled but no authtoken set; skipping tunnel start")
		} else {
			opts := ngrokremote.Options{
				LocalAddr:     fmt.Sprintf("http://localhost:%d", serverPort),
				Authtoken:     config.App.Ngrok.Authtoken,
				Region:        config.App.Ngrok.Region,
				Domain:        config.App.Ngrok.Domain,
				BasicAuthUser: config.App.Ngrok.BasicAuthUser,
				BasicAuthPass: config.App.Ngrok.BasicAuthPass,
			}
			tunnel, err := ngrokremote.Start(ctx, opts)
			if err != nil {
				logger.Error("ngrok tunnel failed to start", slog.Any("error", err))
			} else {
				logger.Info("ngrok tunnel established", slog.String("url", tunnel.URL()))
				if config.App.Ngrok.SendURL {
					go event.Send(event.NgrokTunnel(tunnel.URL()))
				}
			}
			ngrokTunnel = tunnel
		}
	}

	g.Go(wrapWithRecover(logger, func() error {
		defer cancel()

		// Headless mode: skip webview, keep HTTP server alive for Claude mode.
		if os.Getenv("HEADLESS") == "1" {
			logger.Info("Headless mode — webview disabled, HTTP server active",
				slog.Int("port", serverPort))
			<-ctx.Done()
			return nil
		}

		displayScale := config.GetCurrentDisplayScale()

		// 1. Load dimensions from config, or use defaults
		width := config.App.WindowWidth
		if width <= 0 {
			width = 1040
		}
		height := config.App.WindowHeight
		if height <= 0 {
			height = 720
		}

		w, err := gowebview.New(&gowebview.Config{URL: fmt.Sprintf("http://localhost:%d", serverPort), WindowConfig: &gowebview.WindowConfig{
			Title: "Settings",
			Size: &gowebview.Point{
				X: int64(float64(width) * displayScale),
				Y: int64(float64(height) * displayScale),
			},
		}})
		if err != nil {
			if w != nil {
				w.Destroy()
			}
			return fmt.Errorf("error creating webview: %w", err)
		}

		// 2. Set HintNone to allow mouse resizing
		w.SetSize(&gowebview.Point{
			X: int64(float64(width) * displayScale),
			Y: int64(float64(height) * displayScale),
		}, gowebview.HintNone)

		// 3. Start Auto-Save Polling
		go func() {
			handle := w.Window() // Get native Windows handle
			user32 := syscall.NewLazyDLL("user32.dll")
			getWindowRect := user32.NewProc("GetWindowRect")
			isIconic := user32.NewProc("IsIconic")
			type RECT struct{ Left, Top, Right, Bottom int32 }

			ticker := time.NewTicker(5 * time.Second)
			defer ticker.Stop()

			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					// Check if minimized (IsIconic returns non-zero if minimized)
					minimized, _, _ := isIconic.Call(handle)
					if minimized != 0 {
						continue
					}

					var rect RECT
					ret, _, _ := getWindowRect.Call(handle, uintptr(unsafe.Pointer(&rect)))
					if ret != 0 {
						curW := int(float64(rect.Right-rect.Left) / displayScale)
						curH := int(float64(rect.Bottom-rect.Top) / displayScale)

						// Check if size is valid and has changed
						if curW > 100 && curH > 100 {
							if curW != config.App.WindowWidth || curH != config.App.WindowHeight {
								config.UpdateWindowSize(curW, curH)
							}
						}
					}
				}
			}
		}()

		// 4. Set initial window title and start rotation goroutine.
		{
			handle := w.Window()
			titlePtr, _ := syscall.UTF16PtrFromString(initialWindowTitle)
			winproc.SetWindowText.Call(handle, uintptr(unsafe.Pointer(titlePtr)))

			// Rotate title every 20-90s with stealth jitter so the window
			// signature never settles on one string. Goroutine exits when
			// the webview's parent ctx is done.
			go func(handle uintptr) {
				defer func() {
					if r := recover(); r != nil {
						_ = r // never crash the bot over a cosmetic detail
					}
				}()
				for {
					// Random sleep 20s..90s using JitterDuration for consistency
					// with the rest of the stealth layer.
					sleep := 20*time.Second + memory.JitterDuration(35*time.Second, 1.0)
					if sleep < 20*time.Second {
						sleep = 20 * time.Second
					} else if sleep > 90*time.Second {
						sleep = 90 * time.Second
					}
					select {
					case <-ctx.Done():
						return
					case <-time.After(sleep):
					}
					next := pickRandomTitle()
					if p, err := syscall.UTF16PtrFromString(next); err == nil {
						winproc.SetWindowText.Call(handle, uintptr(unsafe.Pointer(p)))
					}
				}
			}(handle)
		}

		defer w.Destroy()
		w.Run()

		return nil
	}))

	// Discord Bot initialization
	if config.App.Discord.Enabled {
		discordBot, err := discord.NewBot(
			config.App.Discord.Token,
			config.App.Discord.ChannelID,
			config.App.Discord.ItemChannelID,
			manager,
			config.App.Discord.UseWebhook,
			config.App.Discord.WebhookURL,
			config.App.Discord.ItemWebhookURL,
		)
		if err != nil {
			logger.Error("Discord could not been initialized", slog.Any("error", err))
			return
		}

		eventListener.Register(discordBot.Handle)
		if !config.App.Discord.UseWebhook {
			g.Go(wrapWithRecover(logger, func() error {
				return discordBot.Start(ctx)
			}))
		}
	}

	// Telegram Bot initialization
	if config.App.Telegram.Enabled {
		telegramBot, err := telegram.NewBot(config.App.Telegram.Token, config.App.Telegram.ChatID, logger)
		if err != nil {
			logger.Error("Telegram could not been initialized", slog.Any("error", err))
			return
		}

		eventListener.Register(telegramBot.Handle)
		g.Go(wrapWithRecover(logger, func() error {
			return telegramBot.Start(ctx)
		}))
	}

	g.Go(wrapWithRecover(logger, func() error {
		defer cancel()
		return srv.Listen(serverPort)
	}))

	g.Go(wrapWithRecover(logger, func() error {
		defer cancel()
		return eventListener.Listen(ctx)
	}))

	g.Go(wrapWithRecover(logger, func() error {
		<-ctx.Done()
		logger.Info("Shutting down...")
		cancel()
		manager.StopAll()
		scheduler.Stop()
		err = srv.Stop()
		if err != nil {
			logger.Error("error stopping local server", slog.Any("error", err))
		}
		if ngrokTunnel != nil {
			if closeErr := ngrokTunnel.Close(); closeErr != nil {
				logger.Error("error stopping ngrok tunnel", slog.Any("error", closeErr))
			}
		}

		return err
	}))

	err = g.Wait()
	if err != nil {
		cancel()
		logger.Error("Error running application", slog.Any("error", err))
		return
	}

	sloggger.FlushAndClose()
}
