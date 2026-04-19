package memory

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"syscall"

	"golang.org/x/sys/windows"
)

// bakedOffsetsHashSize is how many bytes of D2R.exe we SHA256 to identify a
// build. The first 1 MB is more than enough to disambiguate Blizzard releases
// while staying cheap (single ReadFile, single SHA256 pass at startup).
const bakedOffsetsHashSize = 1 << 20

// bakedOffsets maps SHA256(D2R.exe[0:1MB]) hex to an Offset struct verified
// against that exact build. Adding an entry skips runtime pattern resolution
// for that hash — eliminates the AOB-scan signature class for known builds.
//
// Workflow when D2R updates:
//  1. Run `tools/offset_resolver` against the new D2R.exe.
//  2. App.exe at next startup logs the new hash via the "D2R build hash"
//     line in stderr (LogD2RBuildHash). Copy that hash here.
//  3. Add an entry: bakedOffsets[hash] = Offset{UnitTable: 0x..., ...}.
//  4. Removing a known-broken build is just deleting its entry — bot then
//     falls back to the legacy hardcoded `calculateOffsets` body.
//
// Mirrors GID's per-build precomputed Rdata blob strategy
// (see Desktop/reports/COMPARATIVE_GID_VS_ICARIUS.md sec 5 K3).
var bakedOffsets = map[string]Offset{
	// D2R 3.0.92198 — verified 2026-04-19 normal-mode + Claude-mode runs
	// (offset_resolver: 15/35 patterns aob-primary matched current offset.go;
	// remaining 17 misses are stale patterns in offset_resolver/patterns.go,
	// not stale offsets — verified by full-game run 2026-04-19 01:11 in
	// project_normal_mode_working_2026_04_19.md).
	"7723df1e10d798058d6e50910c63923ebd04a1798f9560d4043aab6cafa04e77": {
		UnitTable:                   0x1EA73D0,
		UI:                          0x1EB70CA,
		Hover:                       0x1DFB080,
		Expansion:                   0x1DFA4E8,
		RosterOffset:                0x1EBD6E8,
		PanelManagerContainerOffset: 0x1E11E40,
		WidgetStatesOffset:          0x1EDF700,
		WaypointTableOffset:         0x1D59440,
		FPS:                         0x1D59414,
		KeyBindingsOffset:           0x19D25B4,
		KeyBindingsSkillsOffset:     0x1DFB190,
		QuestInfo:                   0x1EC3D58,
		TZ:                          0x25B1B80,
		TZOffline:                   0x25B2300,
		Ping:                        0x1DFA4E8,
		LegacyGraphics:              0x1EC3FC6,
		CharData:                    0x1DFE678,
		SelectedCharName:            0x1D50215,
		LastGameName:                0x25FA4E0,
		LastGamePassword:            0x25FA538,
	},
}

// d2rImagePathByPID opens a transient PROCESS_QUERY_LIMITED_INFORMATION
// handle to `pid` (the rights flag every modern Windows account already has
// for any running process) and returns the executable path via
// QueryFullProcessImageNameW. We don't reuse Process.handler because that
// handle is opened with rotated access masks (PROCESS_VM_READ + optional
// QueryLimitedInfo) and may not include the rights this query needs.
func d2rImagePathByPID(pid uint32) (string, error) {
	const PROCESS_QUERY_LIMITED_INFORMATION = 0x1000
	h, err := windows.OpenProcess(PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return "", fmt.Errorf("OpenProcess(QueryLimited) pid=%d: %w", pid, err)
	}
	defer windows.CloseHandle(h)
	var size uint32 = windows.MAX_PATH
	buf := make([]uint16, size)
	if err := windows.QueryFullProcessImageName(h, 0, &buf[0], &size); err != nil {
		return "", fmt.Errorf("QueryFullProcessImageName pid=%d: %w", pid, err)
	}
	return syscall.UTF16ToString(buf[:size]), nil
}

// D2RBuildHash returns SHA256 hex of the first `bakedOffsetsHashSize` bytes
// of D2R.exe for `pid`. We hash the file on disk (not the in-memory image)
// so Arxan's runtime page rewrites and per-session ASLR don't churn the hash
// across launches of the same Blizzard build.
func D2RBuildHash(pid uint32) (string, error) {
	path, err := d2rImagePathByPID(pid)
	if err != nil {
		return "", err
	}
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open D2R.exe at %s: %w", path, err)
	}
	defer f.Close()
	buf := make([]byte, bakedOffsetsHashSize)
	read, _ := f.Read(buf)
	if read == 0 {
		return "", fmt.Errorf("read D2R.exe at %s returned 0 bytes", path)
	}
	sum := sha256.Sum256(buf[:read])
	return hex.EncodeToString(sum[:]), nil
}

// LookupBakedOffsets resolves the Offset struct for the running D2R build,
// returning (offsets, hash, true) on a known build or (_, hash, false) when
// the hash isn't in our table. Caller falls back to the legacy hardcoded
// calculateOffsets path on miss.
//
// Returns ("",false) only if hashing itself failed (process gone, file unreadable).
func LookupBakedOffsets(pid uint32) (Offset, string, bool) {
	hash, err := D2RBuildHash(pid)
	if err != nil {
		return Offset{}, "", false
	}
	o, ok := bakedOffsets[hash]
	return o, hash, ok
}
