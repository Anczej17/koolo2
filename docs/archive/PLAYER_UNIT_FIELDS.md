# PlayerUnit Snapshot Field Map — Phase A

Deliverable A.0 of Phase A in `Desktop/reports/P1_GID_PLAN.md`.

**Purpose**: catalogue every D2R memory read that backs `GetRawPlayerUnits` + `GetPlayerUnit` (`internal/gamelib/memory/player.go`). This is the contract for what rmod must mirror into SHM, and what the Go-side `SnapshotReader` must serve.

**Design principle**: rmod replicates Go's pointer-walk logic inside D2R's address space and copies the raw byte regions into SHM. Go-side `SnapshotReader` exposes `ReadUInt(va, size)` / `ReadBytes(va, n)` / `ReadString(va, n)` that satisfies lookups from a region table rather than issuing `ReadProcessMemory`. **Decode stays in Go, no RPM happens.**

## Region table format

```rust
#[repr(C)]
struct RegionEntry {
    va: u64,      // original D2R virtual address this buffer was copied from
    len: u32,     // bytes (buffer size)
    offset: u32,  // byte offset inside SharedBuffer.snapshot_data
}
```

Rmod writes a table of N regions plus the data blob. Bot looks up by `va ∈ [entry.va, entry.va+entry.len)`. Binary-searchable if sorted by VA, linear scan acceptable at N<200.

## Base & derived statics

| Name | VA | Source | Read size |
|---|---|---|---|
| `module_base` (D2R.exe) | from PEB walk (Phase C) or `GetModuleHandleW(NULL)` (Phase A) | — | — |
| `offset.UnitTable` | module_base + 0x1EA73D0 | offset.go:30 | walk 128 pointer slots (×8 B) |
| `offset.Expansion` | module_base + 0x1DFA4E8 | offset.go:33 | 8 B ptr + 0x10 B at target |
| `offset.WaypointTableOffset` | module_base + 0x1D59440 | offset.go:37 | 8 B ptr + waypoint struct/data |
| `offset.WidgetStatesOffset` (activeSlot) | module_base + 0x1EDF700 | offset.go:36 | part of Phase B, not A scope |

## Per-PlayerUnit pointer chain

Starting from unit-table slot:

```
UnitTable[i] (u64 ptr) ─▶ playerUnit (struct @ ~0x200 B)
   │
   ├── +0x08         unit_id (u32)
   ├── +0x0C         mode (u32)
   ├── +0x10 (ptr)  ─▶ pUnitData (struct @ ~0x100 B)
   │                       └── +0x00 (ptr) ─▶ playerName (WCHAR[16] ≤ 32 B)
   ├── +0x17C        class (u32)
   ├── +0x1AE        is_corpse (u8)
   ├── +0x38 (ptr)  ─▶ pathAddress (struct @ ~0x100 B)
   │                       ├── +0x02  x_pos (u16)
   │                       ├── +0x06  y_pos (u16)
   │                       └── +0x20 (ptr) ─▶ room1 (struct @ ~0x40 B)
   │                                              └── +0x18 (ptr) ─▶ room2 (struct @ ~0x100 B)
   │                                                                      └── +0x90 (ptr) ─▶ levelPtr (struct @ ~0x200 B)
   │                                                                                             └── +0x1F8  level_no (u32) = Area ID
   ├── +0x88 (ptr)  ─▶ statsListExPtr (struct @ ~0xB20 B — needs +0xAF0+32 for state bits)
   │                       ├── +0x30 (linked-list head) ─▶ baseStats chain (N×~0x20 B nodes)
   │                       ├── +0xA8 (linked-list head) ─▶ stats chain (N×~0x20 B nodes)
   │                       └── +0xAF0..+0xAF0+0x20  state_flags[8] u32
   ├── +0x90 (ptr)  ─▶ inventoryAddr (struct @ ~0x100 B)
   │                       ├── +0x30  is_main_player_nonlod (u16)
   │                       └── +0x70  is_main_player_lod (u16)
   ├── +0x100 (ptr) ─▶ skillListPtr (struct @ ~0x20 B)
   │                       ├── +0x00 (linked-list head) ─▶ skills chain (N×~0x60 B nodes, each has +0x00 txt ptr)
   │                       ├── +0x08 (ptr) ─▶ leftSkillTxtPtr (8 B deref to ID u16)
   │                       └── +0x10 (ptr) ─▶ rightSkillTxtPtr (8 B deref to ID u16)
   └── +0x158 (ptr)  ─▶ next playerUnit (continue walk)
```

## Region inventory per PlayerUnit (Phase A)

Fixed regions (always copied):

| Region | Origin VA (relative) | Length | Purpose |
|---|---|---|---|
| R1 UnitTable slots | module_base + 0x1EA73D0 | 128 × 8 B = 0x400 | slot ptrs, Go walks |
| R2 expCharPtr target | `*(module_base + 0x1DFA4E8)` | 0x20 | read expansion flag at +0x5C (reality: 0x5C+2 bytes; 0x20 generous) |
| R3 waypoint struct | `*(module_base + 0x1D59440)` | 0x100 | `decodeWaypointMasks` first 7 bytes + slack |
| R4 waypoint data | `*(waypoint_struct + 0x10)` | 0x200 | decoded bits source |

Per main playerUnit (identified by `is_main_player>0`):

| Region | Origin VA | Length | Purpose |
|---|---|---|---|
| P1 playerUnit struct | `*(UnitTable[i])` | 0x200 | covers all fixed offsets (0x08..0x1B0) |
| P2 pUnitData (name struct) | `*(playerUnit + 0x10)` | 0x40 | first ptr → name bytes |
| P3 playerName | `*(pUnitData + 0x00)` | 0x40 | UTF-16 name (≤32 B content) |
| P4 pathAddress | `*(playerUnit + 0x38)` | 0x100 | xpos/ypos + room chain ptr |
| P5 room1 | `*(pathAddress + 0x20)` | 0x40 | +0x18 ptr to room2 |
| P6 room2 | `*(room1 + 0x18)` | 0x100 | +0x90 ptr to level |
| P7 levelPtr | `*(room2 + 0x90)` | 0x200 | +0x1F8 level_no |
| P8 inventoryAddr | `*(playerUnit + 0x90)` | 0x80 | +0x30 / +0x70 main-player flags |
| P9 statsListExPtr | `*(playerUnit + 0x88)` | 0xB20 | state flags + linked-list heads |
| P10 skillListPtr | `*(playerUnit + 0x100)` | 0x20 | linked-list heads + left/right ptrs |
| P11 leftSkillTxtPtr deref | `*(*(skillListPtr + 0x08))` | 0x10 | first 2 bytes = skill ID |
| P12 rightSkillTxtPtr deref | `*(*(skillListPtr + 0x10))` | 0x10 | first 2 bytes = skill ID |

Variable-length reads (rmod mirrors exact Go read pattern):

### baseStats & fullStats — flat arrays (NOT linked lists)

Go's `getStatsList(listHeaderVA)` (game_reader.go:271):
1. Read `[listHeaderVA, 0x10]` → first 0x10 bytes contain `head_ptr:u64` + `count:u64`
2. If `count > 0`: read `[head_ptr, count*10]` → array, each entry 8 B (layer u16 + enum u16 + value u32)

So per list = **2 regions**: the header (already covered by P9 `statsListExPtr[0..0xB20]`) and the array body.

| Region | Origin VA | Length |
|---|---|---|
| P9a base_stats_array | `*(statsListExPtr + 0x30)` | `count * 10` (max ~256 entries → 0xA00) |
| P9b stats_array | `*(statsListExPtr + 0xA8)` | `count * 10` (max ~256 entries → 0xA00) |

### skills — linked list walk

Go's `getSkills(skillListPtr)` (player.go:179):
1. head = `*(skillListPtr)` (already in P10)
2. While head != 0: read `[head, 0x60]` for lvl(+0x40), qty(+0x48), charges(+0x50), next(+0x08), txtPtr(+0x00); read `[*txtPtr, 0x10]` for skill ID (first u16).

Per skill = **2 regions** (node + txt). For ~30-60 skills typical = 60-120 regions.

| Region (per node i) | Origin VA | Length |
|---|---|---|
| Sn.i skill_node_i | walked | 0x60 |
| St.i skill_txt_i | `*(skill_node_i + 0x00)` | 0x10 |

With 256 RegionEntry slots, budget = 15 fixed + main playerUnit ~10 + 2×2 stats lists + 2×60 skills = ~150. Fits.

## Total Phase A region budget

Fixed + main playerUnit: ~0x1C20 bytes = ~7 KB
Linked-list walks: ~0x8000 bytes = ~32 KB (upper bound; typical player has 20-40 stats + 30-60 skills → ~5 KB)

SnapshotBlock hot section: **48 KB** covers worst case with slack. Fits 128 KB SHM easily.

## SharedBuffer layout after Phase A

| Range | Size | Contents |
|---|---|---|
| 0x00000 – 0x03FFF | 16 KB | existing command slots (unchanged layout) |
| 0x04000 – 0x04FFF | 4 KB | snapshot header (magic, version, tick, region_count, etc.) + RegionEntry[256] table |
| 0x05000 – 0x0FFFF | 44 KB | snapshot data blob (regions dumped here) |
| 0x10000 – 0x1FFFF | 64 KB | reserved for Phase B tiers (monsters/inventory/etc.) |

Total: 128 KB. `#[repr(C)] struct SharedBuffer` total size `= 131072`. Assert must update.

## Snapshot header (at 0x04000)

```rust
#[repr(C)]
struct SnapshotHeader {
    magic: u32,            // 'SNAP' = 0x50414E53
    version: u32,          // layout version (A=1)
    tick_counter: u64,     // atomic, bumps per complete write
    region_count: u32,     // how many RegionEntry slots populated
    data_bytes: u32,       // how many bytes in data blob used
    last_write_rdtsc: u64, // for debug/staleness
    d2r_module_base: u64,  // for rel-VA translation
    flags: u32,            // bit0 = snapshot_enabled, bit1 = main_player_found
    _reserved: [u32; 27],  // pad to 256 B
}
```

## Notes for Go `SnapshotReader`

- `Open(shmName)` — maps read-only (PAGE_READONLY on bot side)
- `WaitForFirstTick(timeout = 3 s)` — polls `tick_counter > 0`, **hard-fail** per user directive
- `ReadUInt(va, sz)`:
  - Lookup: find region where `va ∈ [entry.va, entry.va+entry.len)`
  - If none: `panic("snapshot miss: va=0x%x sz=%d — rmod didn't mirror this region")` → hard-fail (per user directive)
  - Else: return appropriate bytes from `data_blob[entry.offset + (va - entry.va) : ...]` as uint{32,64}
- `ReadBytesFromMemory(va, n)` — same lookup, return `[]byte` slice copy (not direct ref — safer against concurrent rmod rewrite)
- `ReadStringFromMemory(va, maxLen)` — reads as UTF-16 `wchar_t` up to maxLen (D2R uses wide strings) or null-terminated; the existing Go impl handles this

## Not in Phase A

Deferred to Phase B:
- Hover (needs `offset.Hover` chain)
- Monsters, Objects, Corpses, Roster
- Inventory items (stash tabs)
- Menus / OpenMenus / ActiveWeaponSlot (WidgetStates)
- IsIngame (widget state)
- KeyBindings, Quests, FPS/Ping, TerrorZones, LastGameName/Pass, LegacyGraphics, HasMerc

Deferred to Phase C:
- XOR-obfuscate these offsets in rmod
- Runtime-codegen variants of the deref sequences
- PEB walk replacing `GetModuleHandleW`

## Verification checklist for A.0

- [x] All PlayerUnit fields from `data.PlayerUnit` have a read path documented
- [x] All reads in `GetRawPlayerUnits` + `GetPlayerUnit` + `decodeWaypointMasks` + `GetStates` + `getStatsList` + `getSkills` covered
- [x] Region table can hold all needed regions (~30 entries main case)
- [x] SHM layout doesn't collide with existing command slots
