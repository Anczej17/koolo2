package game

import (
	"errors"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/billgraziano/dpapi"
	"github.com/lxn/win"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
	"local/internal/svc/internal/config"
	"local/internal/svc/internal/gamelib/data/difficulty"
	"local/internal/svc/internal/secrets"
	"local/internal/svc/internal/utils"
	"local/internal/svc/internal/utils/winproc"
)

// startGameMu serializes D2R launches across all supervisors.
// On GPU-P (Hyper-V GPU partitioning) setups, concurrent D2R launches
// fight for the same GPU resources and fail with "Failed to initialize graphics device".
var startGameMu sync.Mutex

type Manager struct {
	gr             *MemoryReader
	hid            *HID
	gi             *MemoryInjector
	packetSender   *PacketSender
	supervisorName string
}

func NewGameManager(gr *MemoryReader, hid *HID, gi *MemoryInjector, packetSender *PacketSender, sueprvisorName string) *Manager {
	return &Manager{gr: gr, hid: hid, gi: gi, packetSender: packetSender, supervisorName: sueprvisorName}
}

func (gm *Manager) postEscape() error {
	if gm.hid != nil && gm.hid.IsDisabled() {
		if gm.gr != nil && gm.gr.HWND != 0 {
			PostWindowKey(gm.gr.HWND, byte(win.VK_ESCAPE))
			return nil
		}
		if gm.packetSender != nil {
			return gm.packetSender.PostKeyInProcess(byte(win.VK_ESCAPE))
		}
		return errors.New("HID disabled and no ESC route available")
	}
	gm.hid.PressKey(win.VK_ESCAPE)
	return nil
}

func PostWindowKey(hwnd win.HWND, key byte) {
	win.PostMessage(hwnd, win.WM_KEYDOWN, uintptr(key), keyLParam(key, true))
	utils.Sleep(60)
	win.PostMessage(hwnd, win.WM_KEYUP, uintptr(key), keyLParam(key, false))
}

func PostWindowKeySequence(hwnd win.HWND, keys ...byte) {
	for _, key := range keys {
		PostWindowKey(hwnd, key)
		utils.Sleep(250)
	}
}

func keyLParam(key byte, down bool) uintptr {
	ret, _, _ := winproc.MapVirtualKey.Call(uintptr(key), 0)
	scanCode := int(ret)
	previousKeyState := 0
	transitionState := 0
	if !down {
		previousKeyState = 1
		transitionState = 1
	}
	return uintptr((1 & 0xFFFF) | (scanCode << 16) | (previousKeyState << 30) | (transitionState << 31))
}

func (gm *Manager) clickAt(x, y int) error {
	if gm.hid != nil && gm.hid.IsDisabled() {
		if gm.gr != nil && gm.gr.HWND != 0 {
			postWindowClick(gm.gr, gm.gi, x, y, MouseLeft)
			return nil
		}
		if gm.packetSender != nil {
			return gm.packetSender.ClickAt(int32(x), int32(y), MouseLeft)
		}
		return errors.New("HID disabled and no click route available")
	}
	gm.hid.Click(LeftButton, x, y)
	return nil
}

func postWindowClick(gr *MemoryReader, gi *MemoryInjector, x, y int, btn byte) {
	gr.updateWindowPositionData()
	if gi != nil {
		_ = gi.CursorPos(gr.WindowLeftX+x, gr.WindowTopY+y)
	}

	screenLParam := calculateLparam(gr.WindowLeftX+x, gr.WindowTopY+y)
	clientLParam := calculateLparam(x, y)
	down := uint32(win.WM_LBUTTONDOWN)
	up := uint32(win.WM_LBUTTONUP)
	mk := uintptr(win.MK_LBUTTON)
	if btn == MouseRight {
		down = win.WM_RBUTTONDOWN
		up = win.WM_RBUTTONUP
		mk = uintptr(win.MK_RBUTTON)
	}

	win.SendMessage(gr.HWND, win.WM_NCHITTEST, 0, screenLParam)
	win.SendMessage(gr.HWND, win.WM_SETCURSOR, 0x000105A8, 0x2010001)
	win.PostMessage(gr.HWND, win.WM_MOUSEMOVE, 0, clientLParam)
	win.SendMessage(gr.HWND, down, mk, clientLParam)
	utils.Sleep(80)
	win.SendMessage(gr.HWND, up, 0, clientLParam)
}

func (gm *Manager) ExitGame() error {

	const maxAttempts = 50
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if !gm.gr.InGame() {
			return nil
		}

		data := gm.gr.GetData()
		if data.OpenMenus.QuitMenu {
			fmt.Println("Quit menu detected, selecting Save and Exit.")
			if err := gm.selectSaveAndExit(); err != nil {
				return err
			}
			if gm.waitUntilOutOfGame(3 * time.Second) {
				return nil
			}
		}

		fmt.Printf("Attempt %d: Trying to open menu and exit game...\n", attempt+1)
		if err := gm.postEscape(); err != nil {
			return fmt.Errorf("open quit menu: %w", err)
		}

		if !gm.gr.InGame() {
			return nil // Exited successfully just by pressing ESC (e.g., if already at main menu)
		}

		// Check again if the Quit Menu is now open after pressing ESC
		data = gm.gr.GetData()
		if data.OpenMenus.QuitMenu {
			fmt.Println("Quit menu opened after ESC, selecting Save and Exit.")
			if err := gm.selectSaveAndExit(); err != nil {
				return err
			}
			if gm.waitUntilOutOfGame(3 * time.Second) {
				return nil
			}
		}

		utils.Sleep(50) // Wait before the next attempt
	}

	return errors.New("error exiting game! Timeout after multiple attempts")
}

func (gm *Manager) selectSaveAndExit() error {
	if gm.hid != nil && gm.hid.IsDisabled() {
		if gm.gr != nil && gm.gr.HWND != 0 {
			PostWindowKeySequence(gm.gr.HWND, byte(win.VK_UP), byte(win.VK_RETURN))
			return nil
		}
		if gm.packetSender != nil {
			if err := gm.packetSender.PostKeyInProcess(byte(win.VK_UP)); err != nil {
				return fmt.Errorf("select save and exit via UP: %w", err)
			}
			utils.Sleep(250)
			if err := gm.packetSender.PostKeyInProcess(byte(win.VK_RETURN)); err != nil {
				return fmt.Errorf("select save and exit via ENTER: %w", err)
			}
			return nil
		}
		return errors.New("HID disabled and no Save and Exit key route available")
	}
	gm.hid.KeySequence(byte(win.VK_UP), byte(win.VK_RETURN))
	return nil
}

func (gm *Manager) waitUntilOutOfGame(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !gm.gr.InGame() {
			return true
		}
		utils.Sleep(100)
	}
	return !gm.gr.InGame()
}

func (gm *Manager) saveAndExitButtonY() int {
	if gm.gr == nil || gm.gr.GameAreaSizeY <= 0 {
		return 316
	}
	y := gm.gr.GameAreaSizeY/2 - 44
	if y < 0 {
		return gm.gr.GameAreaSizeY / 2
	}
	return y
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
	var titleBuf [512]uint16
	winproc.GetWindowText.Call(
		uintptr(hwnd),
		uintptr(unsafe.Pointer(&titleBuf[0])),
		uintptr(len(titleBuf)),
	)
	windowTitle := syscall.UTF16ToString(titleBuf[:])
	if strings.EqualFold(strings.TrimSpace(windowTitle), "Error") {
		return true
	}

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
//
// KillProcessAndReap forcibly terminates `pid` AND blocks until the kernel
// reaps the EPROCESS. Returns true when the process is verifiably dead at
// return time, false when termination was blocked (Arxan PROCESS_TERMINATE
// access-deny is the canonical failure case — process stays alive and a
// retry loop calling cmd.Start will accumulate zombies; callers should
// abort their retry on false rather than spinning forever).
func KillProcessAndReap(pid uint32) bool {
	const STILL_ACTIVE = 259

	handle, err := windows.OpenProcess(windows.PROCESS_TERMINATE|windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		fmt.Printf("zombie-guard pid=%d: OpenProcess failed (%v) — process may already be dead, sweeping orphan handles\n", pid, err)
		ForceKillZombieD2R(nil)
		return true // can't open => can't be alive => treat as reaped
	}
	defer windows.CloseHandle(handle)

	terminateOK := true
	if terr := windows.TerminateProcess(handle, 1); terr != nil {
		fmt.Printf("zombie-guard pid=%d: TerminateProcess err=%v (continuing to wait)\n", pid, terr)
		terminateOK = false
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

	// Return value reflects whether the process is verifiably dead at return:
	//   - TerminateProcess succeeded   => true (assumed reaped)
	//   - TerminateProcess refused but final GetExitCodeProcess shows the
	//     process exited (any code != STILL_ACTIVE) => true (D2R died on its
	//     own — GPU init self-exit etc., NOT a zombie)
	//   - TerminateProcess refused AND process still STILL_ACTIVE => false
	//     (zombie cascade incoming — caller should abort retry)
	if !terminateOK {
		var finalExit uint32 = STILL_ACTIVE
		_ = windows.GetExitCodeProcess(handle, &finalExit)
		if finalExit == STILL_ACTIVE {
			fmt.Printf("zombie-guard pid=%d: PROCESS STILL ALIVE after terminate refused + wait + sweep (Arxan block?) — caller should abort retry to avoid VM saturation\n", pid)
			return false
		}
		fmt.Printf("zombie-guard pid=%d: TerminateProcess refused but process self-exited code=0x%x — not a zombie, retry OK\n", pid, finalExit)
	}
	return true
}

// closeWindowAndTerminateProcess closes the GPU error dialog (best-effort) and
// then hard-kills + reaps the D2R process via KillProcessAndReap. Returns true
// if the process is verifiably dead at return; false (Arxan blocked terminate)
// signals the GPU retry caller to abort instead of spawning another D2R.
func closeWindowAndTerminateProcess(hwnd windows.HWND, pid uint32) bool {
	const WM_CLOSE = 0x0010
	if hwnd != 0 {
		win.SendMessage(win.HWND(hwnd), WM_CLOSE, 0, 0)
		time.Sleep(500 * time.Millisecond)
	}
	return KillProcessAndReap(pid)
}

func StartGame(username string, password string, authmethod string, authToken string, realm string, arguments string, useCustomSettings bool) (uint32, win.HWND, error) {
	// GPU-P / Hyper-V VMs sometimes sit in a starved state for minutes —
	// keep trying so giving up doesn't strand sibling supervisors. But cap
	// retries (and total time) so a genuinely broken env doesn't generate
	// a fresh D2R zombie per attempt forever — past the cap the operator
	// gets a clear error and can reboot the VM. With closeWindowAndTerminate
	// now blocking on actual reap + zombie sweep, 30 attempts × ~30 s cap
	// = ~15 min worst case before surrender.
	maxGPURetries := 30
	if raw := os.Getenv("D2R_MAX_GPU_RETRIES"); raw != "" {
		if parsed, parseErr := strconv.Atoi(raw); parseErr == nil && parsed > 0 {
			maxGPURetries = parsed
		}
	}

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

	// Consecutive zombie-guard failures (process refused to terminate even
	// after TerminateProcess + WaitForSingleObject + sweep). Arxan is the
	// canonical cause. If we see this N times in a row we MUST abort the
	// retry — each failed attempt leaves an undying D2R pinning vGPU, and
	// the next cmd.Start drives the cascade that exhausts the VM.
	consecutiveReapFail := 0
	const maxConsecutiveReapFail = 3

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
					if !KillProcessAndReap(uint32(cmd.Process.Pid)) {
						consecutiveReapFail++
						if consecutiveReapFail >= maxConsecutiveReapFail {
							cmd.Process.Release()
							return 0, 0, fmt.Errorf("zombie-guard: %d consecutive D2R instances refused to terminate (Arxan PROCESS_TERMINATE block?) — aborting retry to prevent VM starvation; REBOOT VM", consecutiveReapFail)
						}
					} else {
						consecutiveReapFail = 0
					}
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
			if attempt >= 4 {
				gpuRetryDelay = 5 * time.Second
			}
			if attempt >= 8 {
				gpuRetryDelay = 10 * time.Second
			}
			if attempt >= 16 {
				gpuRetryDelay = 20 * time.Second
			}
			if attempt >= 30 {
				gpuRetryDelay = 30 * time.Second
			}
			fmt.Printf("GPU initialization error detected (attempt %d/%d), retrying in %v...\n", attempt+1, maxGPURetries, gpuRetryDelay)
			if !closeWindowAndTerminateProcess(foundHwnd, uint32(cmd.Process.Pid)) {
				consecutiveReapFail++
				if consecutiveReapFail >= maxConsecutiveReapFail {
					return 0, 0, fmt.Errorf("zombie-guard: %d consecutive D2R instances refused to terminate after GPU init failure (Arxan PROCESS_TERMINATE block?) — aborting retry to prevent VM starvation; REBOOT VM", consecutiveReapFail)
				}
			} else {
				consecutiveReapFail = 0
			}
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
