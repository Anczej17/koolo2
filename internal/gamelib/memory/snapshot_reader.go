// Package memory — SnapshotReader (P1-GID Phase A).
//
// SnapshotReader serves memory reads from the per-Present SHM snapshot that
// rmod.dll writes from inside D2R, rather than issuing cross-process
// NtReadVirtualMemory calls from app.exe. Rmod mirrors the same pointer
// chains Go's GetRawPlayerUnits + GetPlayerUnit walks, copying each region
// into SHM and recording a RegionEntry. SnapshotReader looks up the region
// covering any requested D2R virtual address and serves the bytes from its
// own (bot-side) mapped view.
//
// See Desktop/reports/P1_GID_PLAN.md and C:/src/svc/PLAYER_UNIT_FIELDS.md.
package memory

import (
	"fmt"
	"sync/atomic"
	"time"
	"unsafe"

	"local/internal/svc/internal/presenter"
)

// Reader is the minimal interface GameReader needs to read D2R memory.
// Both *Process (RPM-backed) and *SnapshotReader (in-process SHM-backed)
// satisfy it, so GameReader can swap the underlying source at runtime
// without touching decode logic in player.go / game_reader.go / item.go.
type Reader interface {
	ReadUInt(address uintptr, size IntType) uint
	ReadBytesFromMemory(address uintptr, size uint) []byte
	ReadStringFromMemory(address uintptr, size uint) string
	ReadIntoBuffer(address uintptr, buffer []byte) error
}

// SnapshotReader reads D2R memory via a rmod-populated SHM snapshot.
//
// Construction takes an already-mapped SHM base pointer (presenter package
// owns the underlying Win32 handle). SnapshotReader does not close or unmap.
type SnapshotReader struct {
	base unsafe.Pointer // mapped SHM base (minimum SharedBufSize bytes readable)
	size uintptr
}

// NewSnapshotReader wraps a mapped SHM base. `size` must be at least
// `presenter.SharedBufSize` (131072) bytes.
func NewSnapshotReader(base unsafe.Pointer, size uintptr) *SnapshotReader {
	return &SnapshotReader{base: base, size: size}
}

// Tick returns the monotonic counter rmod bumps after each complete snapshot
// write. Zero means rmod has not yet written a full snapshot.
func (sr *SnapshotReader) Tick() uint64 {
	return atomicLoadU64(sr.base, presenter.OffSnapTick)
}

// Magic returns the header magic — should match presenter.SnapMagic once
// CmdSnapshotInit has been dispatched. Useful for sanity checks.
//
// Rmod publishes magic XORed with the per-session key the bot wrote at
// OffSnapXorKey before init (0 = plain, backward compat). We XOR back here
// so callers see the canonical SnapMagic regardless.
func (sr *SnapshotReader) Magic() uint32 {
	raw := atomicLoadU32(sr.base, presenter.OffSnapMagic)
	key := atomicLoadU32(sr.base, presenter.OffSnapXorKey)
	return raw ^ key
}

// Version returns the snapshot layout version (Phase A = 1).
//
// XORed with the per-session key like Magic — see comment on Magic().
func (sr *SnapshotReader) Version() uint32 {
	raw := atomicLoadU32(sr.base, presenter.OffSnapVersion)
	key := atomicLoadU32(sr.base, presenter.OffSnapXorKey)
	return raw ^ key
}

// Flags returns the header flag bits (SnapFlagEnabled, SnapFlagMainPlayerFound, SnapFlagError).
//
// XORed with the per-session key like Magic — see comment on Magic().
func (sr *SnapshotReader) Flags() uint32 {
	raw := atomicLoadU32(sr.base, presenter.OffSnapFlags)
	key := atomicLoadU32(sr.base, presenter.OffSnapXorKey)
	return raw ^ key
}

// RegionCount returns how many RegionEntry slots are populated in the current snapshot.
func (sr *SnapshotReader) RegionCount() uint32 {
	return atomicLoadU32(sr.base, presenter.OffSnapRegionCount)
}

// DataBytes returns how many bytes of the data blob are used.
func (sr *SnapshotReader) DataBytes() uint32 {
	return atomicLoadU32(sr.base, presenter.OffSnapDataBytes)
}

// D2RBase returns the D2R.exe module base rmod resolved on init.
//
// XORed with key:key concat (high-half = low-half = key) per init-time mask.
func (sr *SnapshotReader) D2RBase() uint64 {
	raw := atomicLoadU64(sr.base, presenter.OffSnapD2RBase)
	key := uint64(atomicLoadU32(sr.base, presenter.OffSnapXorKey))
	return raw ^ ((key << 32) | key)
}

// WaitForFirstTick blocks until `Tick() > 0` or `timeout` elapses. Per the
// P1-GID plan, callers that encounter a timeout must HARD-FAIL — do not
// silently fall back to RPM. That decision is enforced by the caller.
func (sr *SnapshotReader) WaitForFirstTick(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	// Check magic/version first — if rmod never wrote the header, caller
	// didn't send CmdSnapshotInit or rmod isn't injected at all.
	for {
		m := sr.Magic()
		if m == presenter.SnapMagic {
			break
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("snapshot: header never written (magic=0x%08x want 0x%08x) — rmod not injected or CmdSnapshotInit never sent", m, uint32(presenter.SnapMagic))
		}
		time.Sleep(5 * time.Millisecond)
	}

	v := sr.Version()
	if v != presenter.SnapVersion {
		return fmt.Errorf("snapshot: version mismatch rmod=%d bot=%d", v, presenter.SnapVersion)
	}

	for {
		if sr.Tick() > 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("snapshot: tick never advanced past 0 within %s — rmod isn't writing snapshots (Present hook not firing?)", timeout)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// regionEntry mirrors Rust's RegionEntry exactly (16 bytes).
type regionEntry struct {
	va     uint64
	length uint32
	offset uint32
}

// regionsSlice returns a slice view over the populated RegionEntry table.
// The returned slice aliases SHM — treat as read-only.
func (sr *SnapshotReader) regionsSlice() []regionEntry {
	count := sr.RegionCount()
	if count > uint32(presenter.SnapRegionMax) {
		count = uint32(presenter.SnapRegionMax)
	}
	base := unsafe.Pointer(uintptr(sr.base) + uintptr(presenter.OffSnapRegions))
	return unsafe.Slice((*regionEntry)(base), int(count))
}

// findRegion locates the region covering [va, va+size). Returns the slice
// [data_blob[off : off+size]] on success, or nil + false on miss.
//
// Hard-fail semantics: callers must NOT fall back to RPM on miss. Surface the
// miss as an error / panic up to the caller so the user sees bot startup
// fails rather than silently regressing to cross-process reads.
func (sr *SnapshotReader) findRegion(va uintptr, size uint) ([]byte, bool) {
	regions := sr.regionsSlice()
	va64 := uint64(va)
	sz64 := uint64(size)
	for i := range regions {
		r := &regions[i]
		if va64 < r.va {
			continue
		}
		if va64+sz64 > r.va+uint64(r.length) {
			continue
		}
		// Hit — compute offset inside SHM data blob.
		relOff := va64 - r.va
		absOff := uint64(presenter.OffSnapData) + uint64(r.offset) + relOff
		if absOff+sz64 > uint64(sr.size) {
			return nil, false
		}
		base := (*byte)(unsafe.Pointer(uintptr(sr.base) + uintptr(absOff)))
		return unsafe.Slice(base, int(size)), true
	}
	return nil, false
}

// ReadUInt returns the uint at D2R virtual address `va`. Hard-fail on miss.
//
// This method mirrors Process.ReadUInt's signature so SnapshotReader can be
// used as a drop-in replacement in GetPlayerUnit / GetRawPlayerUnits /
// decodeWaypointMasks — the same call sites that currently issue RPM.
func (sr *SnapshotReader) ReadUInt(va uintptr, size IntType) uint {
	buf, ok := sr.findRegion(va, uint(size))
	if !ok {
		panic(fmt.Sprintf("snapshot: miss va=0x%x size=%d — rmod didn't mirror this region (hard-fail per P1_GID_PLAN.md)", uint64(va), size))
	}
	return bytesToUint(buf, size)
}

// ReadBytesFromMemory returns `n` bytes starting at D2R VA `va`. The returned
// slice is a COPY (not aliased) so caller can hold it across snapshot ticks.
func (sr *SnapshotReader) ReadBytesFromMemory(va uintptr, n uint) []byte {
	buf, ok := sr.findRegion(va, n)
	if !ok {
		panic(fmt.Sprintf("snapshot: miss va=0x%x n=%d — rmod didn't mirror this region (hard-fail per P1_GID_PLAN.md)", uint64(va), n))
	}
	out := make([]byte, n)
	copy(out, buf)
	return out
}

// ReadIntoBuffer fills `buffer` from D2R VA `va`. Same hard-fail semantics
// as the other Read* methods. Returns nil on success, or an error wrapping
// the miss VA. Mirrors Process.ReadIntoBuffer signature so item.go can swap
// to gd.reader.* without changing call shape.
func (sr *SnapshotReader) ReadIntoBuffer(va uintptr, buffer []byte) error {
	if len(buffer) == 0 {
		return nil
	}
	buf, ok := sr.findRegion(va, uint(len(buffer)))
	if !ok {
		return fmt.Errorf("snapshot: miss va=0x%x n=%d (ReadIntoBuffer)", uint64(va), len(buffer))
	}
	copy(buffer, buf)
	return nil
}

// ReadStringFromMemory reads a UTF-16 null-terminated string at D2R VA `va`,
// matching Process.ReadStringFromMemory's semantics. If `n` is 0, a default
// upper bound is used.
func (sr *SnapshotReader) ReadStringFromMemory(va uintptr, n uint) string {
	if n == 0 {
		n = 64 // default matches character-name bound
	}
	buf, ok := sr.findRegion(va, n)
	if !ok {
		panic(fmt.Sprintf("snapshot: miss va=0x%x n=%d — rmod didn't mirror this region (hard-fail per P1_GID_PLAN.md)", uint64(va), n))
	}
	// D2R stores names as wchar_t (UTF-16 LE). Walk until NUL.
	if len(buf) < 2 {
		return ""
	}
	words := unsafe.Slice((*uint16)(unsafe.Pointer(&buf[0])), len(buf)/2)
	var length int
	for length < len(words) && words[length] != 0 {
		length++
	}
	runes := make([]rune, 0, length)
	for i := 0; i < length; i++ {
		runes = append(runes, rune(words[i]))
	}
	return string(runes)
}

// ---------------------------------------------------------------------------
// Initialisation helpers
// ---------------------------------------------------------------------------

// WriteInitOffsets publishes the D2R static offset VAs the rmod side needs
// before CmdSnapshotInit can run. Must be called with bot-side SHM write
// access (same mapping presenter owns for command dispatch).
//
// `baseShm` is the bot-side mapped SHM (unsafe.Pointer). `d2rBase` is the
// resolved D2R.exe module base. `off` is the same Offset struct Process uses.
//
// Also writes a per-boot non-zero u32 XOR key at OffSnapXorKey so rmod can
// anti-fingerprint the SNAP magic header (see protocol.go OffSnapXorKey).
func WriteInitOffsets(baseShm unsafe.Pointer, d2rBase uint64, off Offset) {
	// Per-session XOR key — derive from time + d2rBase so it's non-zero,
	// session-unique, and changes across process launches. Low bit forced to 1
	// so the key is always non-zero (zero would disable masking, which we
	// don't want for live runs).
	key := uint32(time.Now().UnixNano()) ^ uint32(d2rBase>>16) ^ 0xA5A5A5A5
	if key == 0 {
		key = 1
	}
	atomicStoreU32(baseShm, presenter.OffSnapXorKey, key)

	atomicStoreU64(baseShm, presenter.OffSnapUnitTable, d2rBase+uint64(off.UnitTable))
	atomicStoreU64(baseShm, presenter.OffSnapExpansion, d2rBase+uint64(off.Expansion))
	atomicStoreU64(baseShm, presenter.OffSnapWaypointTable, d2rBase+uint64(off.WaypointTableOffset))
}

// ---------------------------------------------------------------------------
// Atomic helpers (package-local, no dependency on presenter internals)
// ---------------------------------------------------------------------------

func atomicLoadU32(base unsafe.Pointer, offset uintptr) uint32 {
	return atomic.LoadUint32((*uint32)(unsafe.Pointer(uintptr(base) + offset)))
}
func atomicLoadU64(base unsafe.Pointer, offset uintptr) uint64 {
	return atomic.LoadUint64((*uint64)(unsafe.Pointer(uintptr(base) + offset)))
}
func atomicStoreU64(base unsafe.Pointer, offset uintptr, val uint64) {
	atomic.StoreUint64((*uint64)(unsafe.Pointer(uintptr(base) + offset)), val)
}
func atomicStoreU32(base unsafe.Pointer, offset uintptr, val uint32) {
	atomic.StoreUint32((*uint32)(unsafe.Pointer(uintptr(base) + offset)), val)
}
