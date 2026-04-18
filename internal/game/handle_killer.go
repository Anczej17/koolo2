package game

import (
	"fmt"
	"log/slog"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
	"local/internal/svc/internal/utils"
)

const (
	systemHandleInformation       = 16
	objectNameInformation         = 1
	statusInfoLengthMismatch      = 0xC0000004
	duplicateCloseSource          = 0x1
	processQueryInformation       = 0x0400
	processDupHandle              = 0x0040
	stillActiveExitCode           = 259
)

type systemHandleTableEntryInfo struct {
	UniqueProcessId       uint16
	CreatorBackTraceIndex uint16
	ObjectTypeIndex       uint8
	HandleAttributes      uint8
	HandleValue           uint16
	Object                uintptr
	GrantedAccess         uint32
}

type systemHandleInformationStruct struct {
	NumberOfHandles uint32
	Handles         [1]systemHandleTableEntryInfo
}

type unicodeString struct {
	Length        uint16
	MaximumLength uint16
	Buffer        *uint16
}

type objectNameInformationStruct struct {
	Name unicodeString
}

var (
	ntdll                      = windows.NewLazyDLL("ntdll.dll")
	ntQuerySystemInformation   = ntdll.NewProc("NtQuerySystemInformation")
	ntQueryObject              = ntdll.NewProc("NtQueryObject")
)

func KillAllClientHandles() error {
	// Find all D2R PIDs
	d2rPids, err := findD2RPids()
	if err != nil {
		return fmt.Errorf("failed to enumerate D2R processes: %w", err)
	}
	if len(d2rPids) == 0 {
		return nil
	}

	// Get all system handles
	buf := make([]byte, 1024*1024) // start with 1MB
	for {
		var returnLength uint32
		r, _, _ := ntQuerySystemInformation.Call(
			systemHandleInformation,
			uintptr(unsafe.Pointer(&buf[0])),
			uintptr(len(buf)),
			uintptr(unsafe.Pointer(&returnLength)),
		)
		if r == statusInfoLengthMismatch {
			buf = make([]byte, len(buf)*2)
			continue
		}
		if r != 0 {
			return fmt.Errorf("NtQuerySystemInformation failed: 0x%X", r)
		}
		break
	}

	info := (*systemHandleInformationStruct)(unsafe.Pointer(&buf[0]))
	handleCount := info.NumberOfHandles
	handleSize := unsafe.Sizeof(systemHandleTableEntryInfo{})
	base := unsafe.Pointer(&info.Handles[0])

	currentPid := windows.GetCurrentProcessId()

	for i := uint32(0); i < handleCount; i++ {
		entry := (*systemHandleTableEntryInfo)(unsafe.Add(base, uintptr(i)*handleSize))
		pid := uint32(entry.UniqueProcessId)

		if !d2rPids[pid] || pid == currentPid {
			continue
		}

		// Open the target process to duplicate its handle
		proc, err := windows.OpenProcess(processQueryInformation|processDupHandle, false, pid)
		if err != nil {
			continue
		}

		// Duplicate the handle into our process to query its name
		var dup windows.Handle
		err = windows.DuplicateHandle(
			proc,
			windows.Handle(entry.HandleValue),
			windows.CurrentProcess(),
			&dup,
			0, false, windows.DUPLICATE_SAME_ACCESS,
		)
		if err != nil {
			windows.CloseHandle(proc)
			continue
		}

		name := queryObjectName(dup)
		windows.CloseHandle(dup)

		if strings.Contains(name, "Check For Other Instances") {
			// Close the handle in the target process
			var dummy windows.Handle
			_ = windows.DuplicateHandle(
				proc,
				windows.Handle(entry.HandleValue),
				0,
				&dummy,
				0, false, duplicateCloseSource,
			)
		}

		windows.CloseHandle(proc)
	}

	return nil
}

// ForceKillZombieD2R locates D2R processes whose threads are dead (exit code !=
// STILL_ACTIVE) yet still appear in the kernel process table, and closes every
// external handle pointing at them. The kernel reaps the zombie the moment the
// last reference is dropped. Returns (handlesClosed, zombiesFound).
//
// The zombie state is the aftermath of an Arxan cascade: D2R terminates, but
// our bot (or a prior crashed app.exe) still holds an OpenProcess handle. Even
// `taskkill /F` refuses — "no running instance" — because the threads are
// already dead; only the EPROCESS stub lingers. Without this function the
// only recovery is a VM reboot.
func ForceKillZombieD2R(logger *slog.Logger) (int, int) {
	zombies := findDeadD2RPids()
	if len(zombies) == 0 {
		return 0, 0
	}
	if logger != nil {
		pids := make([]uint32, 0, len(zombies))
		for p := range zombies {
			pids = append(pids, p)
		}
		logger.Info("zombie killer: dead D2R PIDs found",
			slog.Int("count", len(zombies)),
			slog.Any("pids", pids))
	}

	handles, err := enumAllSystemHandles()
	if err != nil {
		if logger != nil {
			logger.Error("zombie killer: enum failed", slog.Any("err", err))
		}
		return 0, len(zombies)
	}

	byPid := make(map[uint32][]uint16)
	selfPid := windows.GetCurrentProcessId()
	for i := range handles {
		h := &handles[i]
		src := uint32(h.UniqueProcessId)
		if src == selfPid || src == 0 || src == 4 /* System */ {
			continue
		}
		byPid[src] = append(byPid[src], h.HandleValue)
	}

	closed := 0
	for srcPid, hs := range byPid {
		proc, err := windows.OpenProcess(processQueryInformation|processDupHandle, false, srcPid)
		if err != nil {
			continue
		}
		for _, hv := range hs {
			var dup windows.Handle
			if derr := windows.DuplicateHandle(proc, windows.Handle(hv),
				windows.CurrentProcess(), &dup,
				0, false, windows.DUPLICATE_SAME_ACCESS); derr != nil {
				continue
			}
			targetPid, perr := windows.GetProcessId(dup)
			windows.CloseHandle(dup)
			if perr != nil || targetPid == 0 {
				continue
			}
			if !zombies[targetPid] {
				continue
			}
			var dummy windows.Handle
			_ = windows.DuplicateHandle(proc, windows.Handle(hv),
				0, &dummy, 0, false, duplicateCloseSource)
			closed++
			if logger != nil {
				logger.Info("zombie killer: closed orphan handle",
					slog.Uint64("holder_pid", uint64(srcPid)),
					slog.Uint64("zombie_pid", uint64(targetPid)))
			}
		}
		windows.CloseHandle(proc)
	}

	if logger != nil {
		logger.Info("zombie killer: done",
			slog.Int("handles_closed", closed),
			slog.Int("zombies_targeted", len(zombies)))
	}
	return closed, len(zombies)
}

func findDeadD2RPids() map[uint32]bool {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil
	}
	defer windows.CloseHandle(snapshot)

	dead := make(map[uint32]bool)
	var pe windows.ProcessEntry32
	pe.Size = uint32(unsafe.Sizeof(pe))
	if err := windows.Process32First(snapshot, &pe); err != nil {
		return dead
	}
	gameName := utils.GameExeName()
	for {
		name := windows.UTF16ToString(pe.ExeFile[:])
		if strings.EqualFold(name, gameName) {
			h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pe.ProcessID)
			if err == nil {
				var exitCode uint32
				if gerr := windows.GetExitCodeProcess(h, &exitCode); gerr == nil {
					if exitCode != stillActiveExitCode {
						dead[pe.ProcessID] = true
					}
				}
				windows.CloseHandle(h)
			} else {
				// OpenProcess failing on a listed PID usually means the
				// process is partially torn down — classic zombie signature.
				dead[pe.ProcessID] = true
			}
		}
		if err := windows.Process32Next(snapshot, &pe); err != nil {
			break
		}
	}
	return dead
}

func enumAllSystemHandles() ([]systemHandleTableEntryInfo, error) {
	buf := make([]byte, 2*1024*1024)
	var retLen uint32
	for {
		r, _, _ := ntQuerySystemInformation.Call(
			systemHandleInformation,
			uintptr(unsafe.Pointer(&buf[0])),
			uintptr(len(buf)),
			uintptr(unsafe.Pointer(&retLen)),
		)
		if r == statusInfoLengthMismatch {
			if len(buf) >= 128*1024*1024 {
				return nil, fmt.Errorf("handle buffer exceeded 128MB")
			}
			buf = make([]byte, len(buf)*2)
			continue
		}
		if r != 0 {
			return nil, fmt.Errorf("NtQuerySystemInformation: 0x%X", r)
		}
		break
	}
	info := (*systemHandleInformationStruct)(unsafe.Pointer(&buf[0]))
	count := info.NumberOfHandles
	size := unsafe.Sizeof(systemHandleTableEntryInfo{})
	base := unsafe.Pointer(&info.Handles[0])
	out := make([]systemHandleTableEntryInfo, count)
	for i := uint32(0); i < count; i++ {
		out[i] = *(*systemHandleTableEntryInfo)(unsafe.Add(base, uintptr(i)*size))
	}
	return out, nil
}

func findD2RPids() (map[uint32]bool, error) {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(snapshot)

	pids := make(map[uint32]bool)
	var pe windows.ProcessEntry32
	pe.Size = uint32(unsafe.Sizeof(pe))

	err = windows.Process32First(snapshot, &pe)
	if err != nil {
		return pids, nil
	}

	for {
		name := windows.UTF16ToString(pe.ExeFile[:])
		if strings.EqualFold(name, utils.GameExeName()) {
			pids[pe.ProcessID] = true
		}
		err = windows.Process32Next(snapshot, &pe)
		if err != nil {
			break
		}
	}

	return pids, nil
}

func queryObjectName(h windows.Handle) string {
	buf := make([]byte, 1024)
	var returnLength uint32
	r, _, _ := ntQueryObject.Call(
		uintptr(h),
		objectNameInformation,
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(len(buf)),
		uintptr(unsafe.Pointer(&returnLength)),
	)
	if r != 0 {
		return ""
	}

	oni := (*objectNameInformationStruct)(unsafe.Pointer(&buf[0]))
	if oni.Name.Length == 0 || oni.Name.Buffer == nil {
		return ""
	}

	return windows.UTF16PtrToString(oni.Name.Buffer)
}
