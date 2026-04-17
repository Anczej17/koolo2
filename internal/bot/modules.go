package bot

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"local/internal/svc/internal/gamelib/memory"
)

// ModuleEntry is a single loaded module in the D2R process.
type ModuleEntry struct {
	Name string
	Base uintptr
	End  uintptr
}

var (
	modulesMu      sync.RWMutex
	modulesByPID   = make(map[uint32][]ModuleEntry) // sorted by Base ascending
	modulesLastPID uint32
)

// RefreshD2RModules enumerates loaded modules in the D2R process and caches
// them so ResolveAddress can map raw VAs to "module+0xRVA" strings.
func RefreshD2RModules(pid uint32) error {
	mods, err := memory.GetProcessModules(pid)
	if err != nil {
		return fmt.Errorf("enum modules pid=%d: %w", pid, err)
	}
	entries := make([]ModuleEntry, 0, len(mods))
	for _, m := range mods {
		entries = append(entries, ModuleEntry{
			Name: strings.ToLower(filepath.Base(m.ModuleName)),
			Base: m.ModuleBaseAddress,
			End:  m.ModuleBaseAddress + uintptr(m.ModuleBaseSize),
		})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Base < entries[j].Base })
	modulesMu.Lock()
	modulesByPID[pid] = entries
	modulesLastPID = pid
	modulesMu.Unlock()
	return nil
}

// ResolveAddress maps a raw virtual address to "module+0xRVA". Falls back to
// "0xVA" if no module covers the address. Uses the most recently refreshed
// module list (regardless of PID), which is fine in single-supervisor mode.
func ResolveAddress(va uintptr) string {
	if va == 0 {
		return "0x0"
	}
	modulesMu.RLock()
	entries := modulesByPID[modulesLastPID]
	modulesMu.RUnlock()
	// Binary search for the module whose [Base, End) covers va.
	if len(entries) > 0 {
		lo, hi := 0, len(entries)-1
		for lo <= hi {
			mid := (lo + hi) / 2
			e := entries[mid]
			if va < e.Base {
				hi = mid - 1
			} else if va >= e.End {
				lo = mid + 1
			} else {
				rva := uint64(va - e.Base)
				return fmt.Sprintf("%s+0x%X", e.Name, rva)
			}
		}
	}
	return fmt.Sprintf("0x%X", uint64(va))
}
