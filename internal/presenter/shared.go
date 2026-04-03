package presenter

import (
	"fmt"
	"sync/atomic"
	"unsafe"

	"golang.org/x/sys/windows"
)

// sectionName returns the named file mapping identifier for the given PID.
// Uses a neutral naming convention that blends with system services.
func sectionName(pid uint32) string {
	return fmt.Sprintf("SvcRt_%d", pid)
}

// createSharedMemory creates a named file mapping backed by the page file,
// maps it into the current process, and initialises the magic/version header.
// Returns the section handle, the mapped base pointer, and any error.
func createSharedMemory(pid uint32) (windows.Handle, unsafe.Pointer, error) {
	name, err := windows.UTF16PtrFromString(sectionName(pid))
	if err != nil {
		return 0, nil, fmt.Errorf("section name: %w", err)
	}

	// Build a null-DACL security descriptor so D2R (possibly different
	// integrity level) can open the mapping without access denied.
	sd, err := windows.NewSecurityDescriptor()
	if err != nil {
		return 0, nil, fmt.Errorf("new sd: %w", err)
	}
	if err := sd.SetDACL(nil, true, false); err != nil {
		return 0, nil, fmt.Errorf("set dacl: %w", err)
	}
	sa := windows.SecurityAttributes{
		Length:             uint32(unsafe.Sizeof(windows.SecurityAttributes{})),
		SecurityDescriptor: sd,
		InheritHandle:      0,
	}

	// INVALID_HANDLE_VALUE → page-file-backed section.
	hSection, err := windows.CreateFileMapping(
		windows.InvalidHandle,
		&sa,
		windows.PAGE_READWRITE,
		0,
		SharedBufSize,
		name,
	)
	if err != nil {
		return 0, nil, fmt.Errorf("create mapping: %w", err)
	}

	base, err := windows.MapViewOfFile(
		hSection,
		windows.FILE_MAP_READ|windows.FILE_MAP_WRITE,
		0, 0,
		uintptr(SharedBufSize),
	)
	if err != nil {
		windows.CloseHandle(hSection)
		return 0, nil, fmt.Errorf("map view: %w", err)
	}

	// Convert in a single expression so go vet does not flag a stale uintptr.
	ptr := *(*unsafe.Pointer)(unsafe.Pointer(&base))

	// Zero the entire buffer before writing header fields.
	clearBytes(ptr, 0, SharedBufSize)

	writeU32(ptr, offMagic, SharedMagic)
	writeU32(ptr, offVersion, SharedVersion)

	return hSection, ptr, nil
}

// openSharedMemory opens an existing named file mapping (e.g. for reconnection)
// and maps it into the calling process.
func openSharedMemory(pid uint32) (windows.Handle, unsafe.Pointer, error) {
	name, err := windows.UTF16PtrFromString(sectionName(pid))
	if err != nil {
		return 0, nil, fmt.Errorf("section name: %w", err)
	}

	hSection, err := openFileMapping(
		windows.FILE_MAP_READ|windows.FILE_MAP_WRITE,
		false,
		name,
	)
	if err != nil {
		return 0, nil, fmt.Errorf("open mapping: %w", err)
	}

	base, err := windows.MapViewOfFile(
		hSection,
		windows.FILE_MAP_READ|windows.FILE_MAP_WRITE,
		0, 0,
		uintptr(SharedBufSize),
	)
	if err != nil {
		windows.CloseHandle(hSection)
		return 0, nil, fmt.Errorf("map view: %w", err)
	}

	return hSection, *(*unsafe.Pointer)(unsafe.Pointer(&base)), nil
}

// closeSharedMemory unmaps the view and closes the section handle.
func closeSharedMemory(handle windows.Handle, ptr unsafe.Pointer) error {
	var firstErr error
	if ptr != nil {
		if err := windows.UnmapViewOfFile(uintptr(ptr)); err != nil {
			firstErr = fmt.Errorf("unmap: %w", err)
		}
	}
	if handle != 0 {
		if err := windows.CloseHandle(handle); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("close handle: %w", err)
		}
	}
	return firstErr
}

// --------------------------------------------------------------------------
// Atomic helpers for shared memory fields
// --------------------------------------------------------------------------

// readU32 performs an atomic load of a uint32 at base+offset.
func readU32(base unsafe.Pointer, offset uintptr) uint32 {
	addr := (*atomic.Uint32)(unsafe.Pointer(uintptr(base) + offset))
	return addr.Load()
}

// writeU32 performs an atomic store of a uint32 at base+offset.
func writeU32(base unsafe.Pointer, offset uintptr, val uint32) {
	addr := (*atomic.Uint32)(unsafe.Pointer(uintptr(base) + offset))
	addr.Store(val)
}

// readU64 reads a uint64 at base+offset (non-atomic; caller serialises).
func readU64(base unsafe.Pointer, offset uintptr) uint64 {
	return *(*uint64)(unsafe.Pointer(uintptr(base) + offset))
}

// writeU64 writes a uint64 at base+offset (non-atomic; caller serialises).
func writeU64(base unsafe.Pointer, offset uintptr, val uint64) {
	*(*uint64)(unsafe.Pointer(uintptr(base) + offset)) = val
}

// writeBytes copies data into the shared buffer at base+offset.
func writeBytes(base unsafe.Pointer, offset uintptr, data []byte) {
	dst := unsafe.Slice((*byte)(unsafe.Pointer(uintptr(base)+offset)), len(data))
	copy(dst, data)
}

// clearBytes zeroes n bytes starting at base+offset.
func clearBytes(base unsafe.Pointer, offset uintptr, n int) {
	dst := unsafe.Slice((*byte)(unsafe.Pointer(uintptr(base)+offset)), n)
	for i := range dst {
		dst[i] = 0
	}
}

// --------------------------------------------------------------------------
// OpenFileMapping — not in x/sys/windows, loaded manually.
// --------------------------------------------------------------------------

var (
	modkernel32        = windows.NewLazySystemDLL("kernel32.dll")
	procOpenFileMapping = modkernel32.NewProc("OpenFileMappingW")
)

func openFileMapping(access uint32, inherit bool, name *uint16) (windows.Handle, error) {
	var inheritVal uintptr
	if inherit {
		inheritVal = 1
	}
	r0, _, e1 := procOpenFileMapping.Call(
		uintptr(access),
		inheritVal,
		uintptr(unsafe.Pointer(name)),
	)
	h := windows.Handle(r0)
	if h == 0 {
		return 0, e1
	}
	return h, nil
}
