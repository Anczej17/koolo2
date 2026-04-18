package game

import (
	"log/slog"
	"time"

	"golang.org/x/sys/windows"
)

type CrashDetector struct {
	pid         int32
	supervisor  string
	hwnd        uintptr
	logger      *slog.Logger
	restartFunc func()
	stopChan    chan struct{}
}

func NewCrashDetector(sup string, pid int32, hwnd uintptr, logger *slog.Logger, restartFunc func()) *CrashDetector {
	return &CrashDetector{
		supervisor:  sup,
		pid:         pid,
		hwnd:        hwnd,
		logger:      logger,
		restartFunc: restartFunc,
		stopChan:    make(chan struct{}),
	}
}

func (cd *CrashDetector) Start() {
	// restartFunc closures reach into supervisor/context state that might
	// be mid-teardown when the crash fires (we've seen goroutine 44 die
	// with rip=0x5a4 — nil-vtable deref). Wrap the whole goroutine in
	// recover so a panic here can't take down app.exe and leave D2R +
	// rmod dangling.
	defer func() {
		if r := recover(); r != nil {
			cd.logger.Error("Crash Detector goroutine panicked",
				slog.Int("PID", int(cd.pid)),
				slog.String("Supervisor", cd.supervisor),
				slog.Any("panic", r))
		}
	}()

	cd.logger.Info("Starting Crash Detector ...", slog.Int("PID", int(cd.pid)), slog.String("Supervisor", cd.supervisor))
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-cd.stopChan:
			cd.logger.Info("Crash Detector stopped.", slog.Int("PID", int(cd.pid)), slog.String("Supervisor", cd.supervisor))
			return
		case <-ticker.C:
			if !cd.isProcessRunning() {
				cd.logger.Error("Client crash detected ...", slog.Int("PID", int(cd.pid)), slog.String("Supervisor", cd.supervisor))
				// Pre-restart zombie sweep: if D2R died via Arxan cascade our
				// bot's handle still pins the EPROCESS. Drain external handles
				// NOW so the next OpenProcess-by-name can't latch the zombie.
				if closed, zombies := ForceKillZombieD2R(cd.logger); closed > 0 || zombies > 0 {
					cd.logger.Info("zombie sweep after crash",
						slog.String("Supervisor", cd.supervisor),
						slog.Int("closed", closed),
						slog.Int("zombies", zombies))
				}
				if cd.restartFunc != nil {
					cd.logger.Info("Attempting to restart client ...", slog.String("Supervisor", cd.supervisor))
					// Guard restartFunc panics so this goroutine logs + exits
					// cleanly instead of dropping the whole process.
					func() {
						defer func() {
							if r := recover(); r != nil {
								cd.logger.Error("restartFunc panicked",
									slog.Any("panic", r),
									slog.String("Supervisor", cd.supervisor))
							}
						}()
						cd.restartFunc()
					}()
				}
				return
			}
		}
	}
}

func (cd *CrashDetector) Stop() {
	cd.logger.Info("Stopping Crash Detector", slog.Int("PID", int(cd.pid)), slog.String("Supervisor", cd.supervisor))
	close(cd.stopChan)
}

func (cd *CrashDetector) isProcessRunning() bool {
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_INFORMATION, false, uint32(cd.pid))
	if err != nil {
		cd.logger.Debug("Failed to open process", slog.Int("PID", int(cd.pid)), slog.String("err", err.Error()))
		return false
	}
	defer windows.CloseHandle(handle)

	var exitCode uint32
	err = windows.GetExitCodeProcess(handle, &exitCode)
	if err != nil {
		cd.logger.Debug("Failed to get exit code", slog.Int("PID", int(cd.pid)), slog.String("error", err.Error()))
		return false
	}

	isRunning := exitCode == 259 // STILL_ACTIVE

	return isRunning
}
