package game

import (
	"fmt"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	systemHandleInformation       = 16
	objectNameInformation         = 1
	statusInfoLengthMismatch      = 0xC0000004
	duplicateCloseSource          = 0x1
	processQueryInformation       = 0x0400
	processDupHandle              = 0x0040
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
	base := uintptr(unsafe.Pointer(&info.Handles[0]))

	currentPid := windows.GetCurrentProcessId()

	for i := uint32(0); i < handleCount; i++ {
		entry := (*systemHandleTableEntryInfo)(unsafe.Pointer(base + uintptr(i)*handleSize))
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
		if strings.EqualFold(name, "d2r.exe") {
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
