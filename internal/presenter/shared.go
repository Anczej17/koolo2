package presenter

import (
	cryptorand "crypto/rand"
	"encoding/binary"
	"fmt"
	"sync/atomic"
	"unsafe"

	"golang.org/x/sys/windows"
)

// sessionShmPrefix is an 8-character random alphanumeric prefix generated
// once per process invocation. All shared-memory section names — main bot
// SHM, sniffer SHM, tracer SHM — derive from this prefix. The prefix is
// also passed to the rmod DLL via APC shellcode so the in-D2R DLL creates
// matching section names.
//
// Static signatures like "DispCache_" are easy YARA targets. Per-session
// randomization breaks that static fingerprint completely.
var sessionShmPrefix = generateSessionPrefix()

func generateSessionPrefix() string {
	const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghjkmnpqrstuvwxyz23456789"
	const prefixLen = 8
	var seed [16]byte
	if _, err := cryptorand.Read(seed[:]); err != nil {
		// Failsafe — should never happen; fall back to a process-unique
		// derivation so we never panic at init time.
		v := uint64(windows.GetCurrentProcessId())
		binary.LittleEndian.PutUint64(seed[:], v*0x9E3779B97F4A7C15)
	}
	out := make([]byte, prefixLen)
	for i := 0; i < prefixLen; i++ {
		out[i] = alphabet[seed[i]%byte(len(alphabet))]
	}
	return string(out)
}

// SessionShmPrefix returns the per-process random prefix used in all SHM
// section names. Other packages (presenter sniffer/tracer) and the injector
// (when forwarding the prefix to the rmod DLL) read this value.
func SessionShmPrefix() string {
	return sessionShmPrefix
}

// sectionName returns the named file mapping identifier for the given PID.
// Format: "{prefix}_{pid}" where prefix is per-process random.
func sectionName(pid uint32) string {
	return fmt.Sprintf("%s_%d", sessionShmPrefix, pid)
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

// readU8 reads a single byte at base+offset.
func readU8(base unsafe.Pointer, offset uintptr) byte {
	return *(*byte)(unsafe.Pointer(uintptr(base) + offset))
}

// writeU8 writes a single byte at base+offset.
func writeU8(base unsafe.Pointer, offset uintptr, val byte) {
	*(*byte)(unsafe.Pointer(uintptr(base) + offset)) = val
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
