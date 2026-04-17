package memory

import (
	"encoding/binary"
	"testing"
	"unsafe"

	"local/internal/svc/internal/presenter"
)

// newFakeSnapshot builds a SHM-shaped byte buffer and a SnapshotReader over
// it. The buffer is exactly SharedBufSize so all offsets match production.
func newFakeSnapshot(t *testing.T) ([]byte, *SnapshotReader) {
	t.Helper()
	buf := make([]byte, presenter.SharedBufSize)
	base := unsafe.Pointer(&buf[0])
	sr := NewSnapshotReader(base, uintptr(len(buf)))
	return buf, sr
}

// writeHeader populates the minimal header fields so WaitForFirstTick /
// Magic / Version / Tick all see consistent values.
func writeHeader(buf []byte, tick uint64, regionCount uint32, dataBytes uint32) {
	binary.LittleEndian.PutUint32(buf[presenter.OffSnapMagic:], uint32(presenter.SnapMagic))
	binary.LittleEndian.PutUint32(buf[presenter.OffSnapVersion:], uint32(presenter.SnapVersion))
	binary.LittleEndian.PutUint64(buf[presenter.OffSnapTick:], tick)
	binary.LittleEndian.PutUint32(buf[presenter.OffSnapRegionCount:], regionCount)
	binary.LittleEndian.PutUint32(buf[presenter.OffSnapDataBytes:], dataBytes)
	binary.LittleEndian.PutUint32(buf[presenter.OffSnapFlags:], uint32(presenter.SnapFlagEnabled))
}

// writeRegion places one RegionEntry at slot `idx` and copies `data` into
// the data blob at offset `blobOff`.
func writeRegion(buf []byte, idx int, va uint64, data []byte, blobOff uint32) {
	entryOff := presenter.OffSnapRegions + idx*presenter.SnapRegionEntrySz
	binary.LittleEndian.PutUint64(buf[entryOff:], va)
	binary.LittleEndian.PutUint32(buf[entryOff+8:], uint32(len(data)))
	binary.LittleEndian.PutUint32(buf[entryOff+12:], blobOff)
	copy(buf[presenter.OffSnapData+int(blobOff):], data)
}

func TestSnapshotHeaderRoundtrip(t *testing.T) {
	buf, sr := newFakeSnapshot(t)
	writeHeader(buf, 42, 0, 0)

	if got := sr.Magic(); got != uint32(presenter.SnapMagic) {
		t.Fatalf("Magic got 0x%08x want 0x%08x", got, presenter.SnapMagic)
	}
	if got := sr.Version(); got != uint32(presenter.SnapVersion) {
		t.Fatalf("Version got %d want %d", got, presenter.SnapVersion)
	}
	if got := sr.Tick(); got != 42 {
		t.Fatalf("Tick got %d want 42", got)
	}
	if got := sr.Flags() & uint32(presenter.SnapFlagEnabled); got == 0 {
		t.Fatalf("Flags missing SnapFlagEnabled")
	}
}

func TestSnapshotReadUInt_Uint32(t *testing.T) {
	buf, sr := newFakeSnapshot(t)
	// One region covering D2R VA 0x0A000000, 16 bytes, blob offset 0.
	region := make([]byte, 16)
	binary.LittleEndian.PutUint32(region[0:], 0xCAFEBABE)
	binary.LittleEndian.PutUint32(region[4:], 0xDEADBEEF)
	writeRegion(buf, 0, 0x0A000000, region, 0)
	writeHeader(buf, 1, 1, 16)

	got := sr.ReadUInt(0x0A000000, Uint32)
	if uint32(got) != 0xCAFEBABE {
		t.Fatalf("ReadUInt@0 got 0x%x want 0xCAFEBABE", got)
	}
	got = sr.ReadUInt(0x0A000004, Uint32)
	if uint32(got) != 0xDEADBEEF {
		t.Fatalf("ReadUInt@4 got 0x%x want 0xDEADBEEF", got)
	}
}

func TestSnapshotReadUInt_AllSizes(t *testing.T) {
	buf, sr := newFakeSnapshot(t)
	region := []byte{0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88}
	writeRegion(buf, 0, 0x1000, region, 0)
	writeHeader(buf, 1, 1, 8)

	if got := sr.ReadUInt(0x1000, Uint8); uint8(got) != 0x11 {
		t.Fatalf("u8 got 0x%x", got)
	}
	if got := sr.ReadUInt(0x1000, Uint16); uint16(got) != 0x2211 {
		t.Fatalf("u16 got 0x%x", got)
	}
	if got := sr.ReadUInt(0x1000, Uint32); uint32(got) != 0x44332211 {
		t.Fatalf("u32 got 0x%x", got)
	}
	if got := sr.ReadUInt(0x1000, Uint64); uint64(got) != 0x8877665544332211 {
		t.Fatalf("u64 got 0x%x", got)
	}
}

func TestSnapshotReadBytes_Copy(t *testing.T) {
	buf, sr := newFakeSnapshot(t)
	src := []byte{1, 2, 3, 4, 5, 6, 7, 8}
	writeRegion(buf, 0, 0x2000, src, 0)
	writeHeader(buf, 1, 1, uint32(len(src)))

	out := sr.ReadBytesFromMemory(0x2000, uint(len(src)))
	for i, v := range src {
		if out[i] != v {
			t.Fatalf("ReadBytes[%d] got %d want %d", i, out[i], v)
		}
	}
	// Mutating out must NOT affect SHM — ReadBytes returns a copy.
	out[0] = 0xFF
	re := sr.ReadBytesFromMemory(0x2000, 1)
	if re[0] != 1 {
		t.Fatalf("ReadBytes didn't copy: mutation bled through (got %d)", re[0])
	}
}

func TestSnapshotReadString_UTF16(t *testing.T) {
	buf, sr := newFakeSnapshot(t)
	// "Barbarian" as UTF-16 LE, null-terminated, padded to 32 bytes.
	region := make([]byte, 32)
	runes := []uint16{'B', 'a', 'r', 'b', 'a', 'r', 'i', 'a', 'n', 0}
	for i, r := range runes {
		binary.LittleEndian.PutUint16(region[i*2:], r)
	}
	writeRegion(buf, 0, 0x3000, region, 0)
	writeHeader(buf, 1, 1, 32)

	got := sr.ReadStringFromMemory(0x3000, 32)
	if got != "Barbarian" {
		t.Fatalf("ReadString got %q want %q", got, "Barbarian")
	}
}

func TestSnapshotMiss_HardFail(t *testing.T) {
	_, sr := newFakeSnapshot(t)
	// No regions at all — any read must panic (hard-fail per plan).
	defer func() {
		r := recover()
		if r == nil {
			t.Fatalf("expected panic on snapshot miss, got nil")
		}
	}()
	sr.ReadUInt(0xDEAD0000, Uint32)
}

func TestSnapshotWaitForFirstTick_Timeout(t *testing.T) {
	_, sr := newFakeSnapshot(t)
	// Empty buffer — no header written. WaitForFirstTick must return error.
	err := sr.WaitForFirstTick(25 * 1_000_000) // 25 ms — just enough to loop a few times
	if err == nil {
		t.Fatalf("expected timeout error, got nil")
	}
}

func TestSnapshotMultipleRegions(t *testing.T) {
	buf, sr := newFakeSnapshot(t)
	// Region 0 covers 0x100..0x108, region 1 covers 0x1000..0x1010.
	writeRegion(buf, 0, 0x100, []byte{1, 2, 3, 4, 5, 6, 7, 8}, 0)
	writeRegion(buf, 1, 0x1000, []byte{0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88, 0x99, 0xAA, 0xBB, 0xCC, 0xDD, 0xEE, 0xFF, 0x00}, 16)
	writeHeader(buf, 1, 2, 32)

	if got := sr.ReadUInt(0x104, Uint32); uint32(got) != 0x08070605 {
		t.Fatalf("region0 mid-read got 0x%x", got)
	}
	if got := sr.ReadUInt(0x1008, Uint32); uint32(got) != 0xCCBBAA99 {
		t.Fatalf("region1 mid-read got 0x%x", got)
	}
}

func TestSnapshotRegionCoveringGuard(t *testing.T) {
	buf, sr := newFakeSnapshot(t)
	// Region 0 covers [0x100, 0x108). Reading 4 bytes at 0x106 would extend
	// to 0x10A → outside region → hard-fail.
	writeRegion(buf, 0, 0x100, []byte{1, 2, 3, 4, 5, 6, 7, 8}, 0)
	writeHeader(buf, 1, 1, 8)

	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("expected panic when read extends beyond region")
		}
	}()
	_ = sr.ReadUInt(0x106, Uint32)
}
