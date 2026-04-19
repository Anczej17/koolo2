package game

import (
	"errors"
	"fmt"
	"math/rand"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/billgraziano/dpapi"
	"local/internal/svc/internal/gamelib/data/difficulty"
	"local/internal/svc/internal/config"
	"local/internal/svc/internal/secrets"
	"local/internal/svc/internal/utils"
	"local/internal/svc/internal/utils/winproc"
	"github.com/lxn/win"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// startGameMu serializes D2R launches across all supervisors.
// On GPU-P (Hyper-V GPU partitioning) setups, concurrent D2R launches
// fight for the same GPU resources and fail with "Failed to initialize graphics device".
var startGameMu sync.Mutex

type Manager struct {
	gr             *MemoryReader
	hid            *HID
	supervisorName string
}

func NewGameManager(gr *MemoryReader, hid *HID, sueprvisorName string) *Manager {
	return &Manager{gr: gr, hid: hid, supervisorName: sueprvisorName}
}

func (gm *Manager) ExitGame() error {

	const maxAttempts = 50
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if !gm.gr.InGame() {
			return nil
		}

		data := gm.gr.GetData()
		if data.OpenMenus.QuitMenu {
			fmt.Println("Quit menu detected, attempting to click exit button.")
			// The click coordinates for the quit menu button are typically around the center, slightly above
			gm.hid.Click(LeftButton, gm.gr.GameAreaSizeX/2, int(float64(gm.gr.GameAreaSizeY)/2.2))
			utils.Sleep(100) // Give it time to process the click and transition out of game
			if !gm.gr.InGame() {
				return nil
			}
		}

		fmt.Printf("Attempt %d: Trying to open menu and exit game...\n", attempt+1)
		gm.hid.PressKey(win.VK_ESCAPE)

		if !gm.gr.InGame() {
			return nil // Exited successfully just by pressing ESC (e.g., if already at main menu)
		}

		// Check again if the Quit Menu is now open after pressing ESC
		data = gm.gr.GetData()
		if data.OpenMenus.QuitMenu {
			fmt.Println("Quit menu opened after ESC, attempting to click exit button.")
			gm.hid.Click(LeftButton, gm.gr.GameAreaSizeX/2, int(float64(gm.gr.GameAreaSizeY)/2.2))
			utils.Sleep(100) // Give it time to process the click and transition out of game
			if !gm.gr.InGame() {
				return nil
			}
		}

		utils.Sleep(50) // Wait before the next attempt
	}

	return errors.New("error exiting game! Timeout after multiple attempts")
}

func (gm *Manager) NewGame() error {
	if gm.gr.InGame() {
		return errors.New("character still in a game")
	}

	for range 30 {
		if gm.gr.IsInCharacterSelectionScreen() {
			break
		} else {
			utils.Sleep(500)
		}
	}

	difficultyPosition := map[difficulty.Difficulty]struct {
		X, Y int
	}{
		difficulty.Normal:    {X: 640, Y: 311},
		difficulty.Nightmare: {X: 640, Y: 355},
		difficulty.Hell:      {X: 640, Y: 403},
	}

	cfg, _ := config.GetCharacter(gm.supervisorName)
	createX := difficultyPosition[cfg.Game.Difficulty].X
	createY := difficultyPosition[cfg.Game.Difficulty].Y
	gm.hid.Click(LeftButton, 600, 650)
	utils.Sleep(250)
	gm.hid.Click(LeftButton, createX, createY)

	for range 12 {
		if gm.gr.InGame() {
			return nil
		}
		utils.Sleep(500)
	}

	return errors.New("timeout")
}

func (gm *Manager) clearGameNameOrPasswordField() {
	for range 16 {
		gm.hid.PressKey(win.VK_BACK)
	}
}

func randomString(n int) string {
	const chars = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, n)
	for i := range b {
		b[i] = chars[rand.Intn(len(chars))]
	}
	return string(b)
}

func (gm *Manager) CreateLobbyGame(gameCounter int) (string, error) {

	// Click "Create game" tab
	gm.hid.Click(LeftButton, 845, 54)
	utils.Sleep(200)

	difficultyPosition := map[difficulty.Difficulty]struct {
		X, Y int
	}{
		difficulty.Normal:    {X: 900, Y: 252},
		difficulty.Nightmare: {X: 980, Y: 252},
		difficulty.Hell:      {X: 1065, Y: 252},
	}

	cfg, _ := config.GetCharacter(gm.supervisorName)
	difficultyPos := difficultyPosition[cfg.Game.Difficulty]
	gm.hid.Click(LeftButton, difficultyPos.X, difficultyPos.Y)
	utils.Sleep(200)

	// Build game name and password
	var gameName, gamePassword string
	if cfg.Companion.RandomGameNames {
		gameName = randomString(5)
		gamePassword = randomString(3)
	} else {
		gameName = cfg.Companion.GameNameTemplate + fmt.Sprintf("%d", gameCounter)
		gamePassword = cfg.Companion.GamePassword
	}

	// Click the game name textbox, delete text and type new game name
	gm.hid.Click(LeftButton, 1000, 116)
	gm.clearGameNameOrPasswordField()
	for _, ch := range gameName {
		gm.hid.PressKey(gm.hid.GetASCIICode(fmt.Sprintf("%c", ch)))
	}

	// Same for password
	gm.hid.Click(LeftButton, 1000, 161)
	utils.Sleep(200)
	if gamePassword != "" {
		gm.clearGameNameOrPasswordField()
		for _, ch := range gamePassword {
			gm.hid.PressKey(gm.hid.GetASCIICode(fmt.Sprintf("%c", ch)))
		}
	}
	gm.hid.PressKey(win.VK_RETURN)

	for range 15 {
		if gm.gr.InGame() {
			return gameName, nil
		}
		utils.Sleep(1000)

		panel := gm.gr.GetPanel("DismissableModal")
		if panel.PanelName != "" && panel.PanelEnabled && panel.PanelVisible {
			gm.hid.PressKey(win.VK_ESCAPE)
			utils.Sleep(1000)
			return gameName, errors.New("error creating game! Got error message")
		}
	}

	return gameName, errors.New("error creating game! Timeout")
}

func (gm *Manager) JoinOnlineGame(gameName, password string) error {

	// Click "Join game" tab
	gm.hid.Click(LeftButton, 977, 54)
	utils.Sleep(200)

	// Click the game name textbox, delete text and type new game name
	gm.hid.Click(LeftButton, 950, 100)
	utils.Sleep(200)
	gm.clearGameNameOrPasswordField()
	utils.Sleep(200)
	for _, ch := range gameName {
		gm.hid.PressKey(gm.hid.GetASCIICode(fmt.Sprintf("%c", ch)))
	}

	// Same for password
	gm.hid.Click(LeftButton, 1130, 100)
	utils.Sleep(200)
	gm.clearGameNameOrPasswordField()
	utils.Sleep(200)
	for _, ch := range password {
		gm.hid.PressKey(gm.hid.GetASCIICode(fmt.Sprintf("%c", ch)))
	}
	gm.hid.PressKey(win.VK_RETURN)

	for range 15 {
		if gm.gr.InGame() {
			return nil
		}
		utils.Sleep(1000)

		// Check if we got an error message while trying to join the game
		panel := gm.gr.GetPanel("DismissableModal")
		if panel.PanelName != "" && panel.PanelEnabled && panel.PanelVisible {
			gm.hid.PressKey(win.VK_ESCAPE)
			utils.Sleep(1000)
			return errors.New("error joining game! Got error message")
		}
	}

	return errors.New("error joining game! Timeout")
}

func (gm *Manager) InGame() bool {
	return gm.gr.InGame()
}

// isGPUErrorWindow checks if the given window is displaying the GPU initialization error dialog
// It enumerates child windows to find the static text control containing the error message
func isGPUErrorWindow(hwnd windows.HWND) bool {
	var foundError bool

	// Callback to enumerate child windows and check their text
	cb := syscall.NewCallback(func(childHwnd windows.HWND, lParam uintptr) uintptr {
		// Get text from child window (static text control in MessageBox)
		var text [512]uint16
		winproc.GetWindowText.Call(
			uintptr(childHwnd),
			uintptr(unsafe.Pointer(&text[0])),
			uintptr(len(text)),
		)
		windowText := syscall.UTF16ToString(text[:])

		// Check if it contains the GPU error message
		if strings.Contains(windowText, "Failed to initialize graphics") {
			foundError = true
			return 0 // Stop enumeration
		}
		return 1 // Continue enumeration
	})

	// Enumerate child windows of the dialog
	winproc.EnumChildWindows.Call(uintptr(hwnd), cb, 0)

	return foundError
}

// KillProcessAndReap forcibly terminates `pid` AND blocks until the kernel
// reaps the EPROCESS. Used by every D2R termination path (GPU retry loop,
// supervisor.KillClient, etc.) to make sure every kill fully releases its
// vGPU slot before the next D2R can be started. Without it, Hyper-V vGPU
// can't reclaim VRAM in time, the next D2R fails GPU init, the loop
// generates a fresh zombie per attempt, and after 5+ iterations the VM is
// starved and must be rebooted.
//
// Sequence:
//  1. OpenProcess(TERMINATE|SYNCHRONIZE) — SYNCHRONIZE makes WaitForSingleObject legal.
//  2. TerminateProcess(handle, 1) — forced exit.
//  3. WaitForSingleObject(handle, 5s) — kernel signals when EPROCESS dies.
//  4. GetExitCodeProcess polling fallback — extra 5s in case wait races.
//  5. ForceKillZombieD2R(nil) — duplicate-close any orphan handles other
//     non-protected processes hold against this dead PID, so the kernel
//     actually reaps the EPROCESS. NOT LSASS / SVCHOST per PPL limitation
//     (see feedback_zombie_lsass_svchost_reboot.md).
func KillProcessAndReap(pid uint32) {
	const STILL_ACTIVE = 259

	handle, err := windows.OpenProcess(windows.PROCESS_TERMINATE|windows.SYNCHRONIZE, false, pid)
	if err != nil {
		fmt.Printf("zombie-guard pid=%d: OpenProcess failed (%v) — process may already be dead, sweeping orphan handles\n", pid, err)
		ForceKillZombieD2R(nil)
		return
	}
	defer windows.CloseHandle(handle)

	if terr := windows.TerminateProcess(handle, 1); terr != nil {
		fmt.Printf("zombie-guard pid=%d: TerminateProcess err=%v (continuing to wait)\n", pid, terr)
	}

	waitRes, _ := windows.WaitForSingleObject(handle, 5000)
	if waitRes != windows.WAIT_OBJECT_0 {
		fmt.Printf("zombie-guard pid=%d: WaitForSingleObject result=0x%x (timeout=0x102) — falling back to exit-code polling\n", pid, waitRes)
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			var exit uint32
			if gerr := windows.GetExitCodeProcess(handle, &exit); gerr == nil && exit != STILL_ACTIVE {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
	}

	if closed, zombies := ForceKillZombieD2R(nil); closed > 0 || zombies > 0 {
		fmt.Printf("zombie-guard pid=%d: post-terminate sweep closed=%d zombies=%d\n", pid, closed, zombies)
	}
}

// closeWindowAndTerminateProcess closes the GPU error dialog (best-effort) and
// then hard-kills + reaps the D2R process via KillProcessAndReap.
func closeWindowAndTerminateProcess(hwnd windows.HWND, pid uint32) {
	const WM_CLOSE = 0x0010
	if hwnd != 0 {
		win.SendMessage(win.HWND(hwnd), WM_CLOSE, 0, 0)
		time.Sleep(500 * time.Millisecond)
	}
	KillProcessAndReap(pid)
}

func StartGame(username string, password string, authmethod string, authToken string, realm string, arguments string, useCustomSettings bool) (uint32, win.HWND, error) {
	// GPU-P / Hyper-V VMs sometimes sit in a starved state for minutes —
	// keep trying so giving up doesn't strand sibling supervisors. But cap
	// retries (and total time) so a genuinely broken env doesn't generate
	// a fresh D2R zombie per attempt forever — past the cap the operator
	// gets a clear error and can reboot the VM. With closeWindowAndTerminate
	// now blocking on actual reap + zombie sweep, 30 attempts × ~30 s cap
	// = ~15 min worst case before surrender.
	const maxGPURetries = 30

	// First check for other instances of the game and kill the handles, otherwise we will not be able to start the game
	err := KillAllClientHandles()
	if err != nil {
		return 0, 0, err
	}

	// Pre-flight: sweep any dead D2R EPROCESS still pinned by orphan handles
	// from a previous failed launch / app.exe crash. Without this, a stale
	// zombie keeps its vGPU slot and makes the upcoming cmd.Start fail GPU
	// init for the same reason that produced the zombie.
	if closed, zombies := ForceKillZombieD2R(nil); closed > 0 || zombies > 0 {
		fmt.Printf("StartGame pre-flight zombie sweep: closed=%d zombies=%d\n", closed, zombies)
	}

	// Depending on the authentication method set base arguments
	var baseArgs []string

	if authmethod == "TokenAuth" {
		baseArgs = []string{"-uid", "osi"}
	} else if authmethod == "UsernamePassword" {
		baseArgs = []string{"-username", username, "-password", password, "-address", realm}
	} else if authmethod == "None" {
		baseArgs = []string{}
	} else {
		// Default to no auth method
		baseArgs = []string{}
	}

	// Parse the provided additional arguments
	additionalArguments := strings.Fields(arguments)

	// Let's use the mod directory for storing the settings, so we stop overwriting the default config
	if useCustomSettings {
		modName := "custom"
		found := false
		for i, arg := range additionalArguments {
			if arg == "-mod" {
				modName = additionalArguments[i+1]
				found = true
				break
			}
		}
		if !found {
			additionalArguments = append(additionalArguments, "-mod", modName)
		}

		// If there is no real mod, let's create a fake mod called "custom" so we can store our own config
		if modName == "custom" {
			err = config.InstallMod()
			if err != nil {
				return 0, 0, err
			}
		}

		// Replace game mod settings with the custom ones
		err = config.ReplaceGameSettings(modName)
		if err != nil {
			return 0, 0, err
		}
	}

	// Add them to the full argument list
	fullArgs := append(baseArgs, additionalArguments...)

	if authmethod == "TokenAuth" {
		// Entropy buffer
		entropy := []byte{0xc8, 0x76, 0xf4, 0xae, 0x4c, 0x95, 0x2e, 0xfe, 0xf2, 0xfa, 0x0f, 0x54, 0x19, 0xc0, 0x9c, 0x43}
		tokenBytes := []byte(authToken)

		encryptedToken, err := dpapi.EncryptBytesEntropy(tokenBytes, entropy)
		if err != nil {
			return 0, 0, fmt.Errorf("failed to encrypt auth token: %v", err)
		}

		// Create or Open the OSI registry folder
		key, _, err := registry.CreateKey(registry.CURRENT_USER, secrets.BlizzardLaunchOptionsKey, registry.ALL_ACCESS)
		if err != nil {
			return 0, 0, fmt.Errorf("failed to open registry key: %v", err)
		}
		defer key.Close()

		region := "EU"
		switch realm {
		case secrets.RealmEU:
			region = "EU"
		case secrets.RealmUS:
			region = "US"
		case secrets.RealmKR:
			region = "KR"
		default:
			region = "EU"
		}

		// Update the region registry
		err = key.SetStringValue("REGION", region)
		if err != nil {
			return 0, 0, fmt.Errorf("failed to set REGION registry value: %v", err)
		}

		err = key.SetBinaryValue("WEB_TOKEN", encryptedToken)
		if err != nil {
			return 0, 0, fmt.Errorf("failed to set WEB_TOKEN registry value: %v", err)
		}

		// If we got to here we've successfully updated the auth token :)
	}

	// Serialize D2R launches across all supervisors.
	// On GPU-P / Hyper-V setups, concurrent GPU init causes "Failed to initialize graphics device".
	startGameMu.Lock()
	defer startGameMu.Unlock()

	// Start the game with retry logic for GPU initialization errors
	for attempt := 0; attempt < maxGPURetries; attempt++ {
		cmd := exec.Command(config.App.AppPath+"\\"+utils.GameExeName(), fullArgs...)
		err = cmd.Start()
		if err != nil {
			return 0, 0, err
		}

		var foundHwnd windows.HWND
		cb := syscall.NewCallback(func(hwnd windows.HWND, lParam uintptr) uintptr {
			var pid uint32
			windows.GetWindowThreadProcessId(hwnd, &pid)
			if pid == uint32(cmd.Process.Pid) {
				foundHwnd = hwnd
				return 0
			}
			return 1
		})
		// Wait for D2R to create a window, with timeout and sleep to avoid CPU spin
		windowDeadline := time.Now().Add(60 * time.Second)
		for {
			windows.EnumWindows(cb, unsafe.Pointer(&cmd.Process.Pid))
			if foundHwnd != 0 {
				// Small delay and read again, to be sure we are capturing the right hwnd
				time.Sleep(time.Second)
				windows.EnumWindows(cb, unsafe.Pointer(&cmd.Process.Pid))
				break
			}
			if time.Now().After(windowDeadline) {
				// D2R started but never created a window — terminate AND wait
				// for actual reap before retry. cmd.Process.Kill alone returns
				// before the kernel reaps EPROCESS; the next cmd.Start would
				// then race the dying D2R for vGPU and add another zombie.
				if cmd.Process != nil {
					KillProcessAndReap(uint32(cmd.Process.Pid))
					cmd.Process.Release()
				}
				fmt.Printf("D2R window not found after 60s (attempt %d/%d), retrying...\n", attempt+1, maxGPURetries)
				time.Sleep(2 * time.Second)
				continue
			}
			time.Sleep(500 * time.Millisecond)
		}

		// Check if the window is a GPU error dialog
		if isGPUErrorWindow(foundHwnd) {
			// Progressive backoff: short waits early, longer waits later.
			// Measured on the GPU-P VM: first 3-5 attempts fail instantly
			// (GPU still pinned by previous D2R), then it clears. Longer
			// delays past attempt 8 give Hyper-V vGPU time to reclaim
			// resources when multi-instance pressure builds. Past 30 we
			// sit at 30 s — any faster just wastes CPU on a genuinely
			// starved GPU.
			gpuRetryDelay := 3 * time.Second
			if attempt >= 4 { gpuRetryDelay = 5 * time.Second }
			if attempt >= 8 { gpuRetryDelay = 10 * time.Second }
			if attempt >= 16 { gpuRetryDelay = 20 * time.Second }
			if attempt >= 30 { gpuRetryDelay = 30 * time.Second }
			fmt.Printf("GPU initialization error detected (attempt %d/%d), retrying in %v...\n", attempt+1, maxGPURetries, gpuRetryDelay)
			closeWindowAndTerminateProcess(foundHwnd, uint32(cmd.Process.Pid))
			time.Sleep(gpuRetryDelay)
			continue // Retry
		}

		// Success — close the handle for the new process so the user can open another instance
		err = KillAllClientHandles()
		if err != nil {
			return 0, 0, err
		}

		// Hold the mutex a little longer to let GPU settle before the next supervisor starts
		time.Sleep(3 * time.Second)

		return uint32(cmd.Process.Pid), win.HWND(foundHwnd), nil
	}

	return 0, 0, errors.New("GPU initialization failed after maximum retries")
}
