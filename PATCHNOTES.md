# Patch Notes — Anti-Detection & Timing Humanization

## Part 1: Identity Obfuscation ("koolo" → "ctfmon")

All externally visible references to "koolo" have been replaced with "ctfmon" (a legitimate Windows process name) or neutralized. This covers everything Warden could potentially scan on disk, in memory window titles, log files, HTTP headers, and mod folders.

### Window & UI
- Window title changed from "Koolo Resurrected" → `ctfmon`
- Error dialogs now show "Application error" / "Application will close" (no branding)
- Discord bot help embed title changed to generic "Bot Commands"

### Log Files
- Log file prefix changed from `Koolo-log-` → `svc-log-`

### Configuration
- Config file renamed: `config/koolo.yaml` → `config/ctfmon.yaml`
- Config dist file renamed: `config/koolo.yaml.dist` → `config/ctfmon.yaml.dist`
- Template config comment neutralized
- All config load/save paths updated

### D2R Mod Folder
- Mod directory changed from `mods\koolo\koolo.mpq` → `mods\ctfmon\ctfmon.mpq`
- Mod name in `modinfo.json` changed to `ctfmon`
- All references in `keybindings.go` and `manager.go` updated

### HTTP User-Agent
- GitHub API requests now use `Mozilla/5.0` instead of `koolo-updater`

### Tools & Assets
- `tools/koolo-map.exe` → `tools/ctfmon-map.exe`
- `assets/koolo.webp` → `assets/ctfmon.webp`
- README image reference updated

### Build Scripts
- `better_build.bat`: All messages neutralized, file references updated
- `build.bat`: All messages neutralized, file references updated

### Updater
- Stash messages: `koolo-updater` → `svc-updater`
- Source directory: `.koolo-src` → `.ctfmon-src`
- Restart scripts: `restart_koolo_*` → `restart_svc_*`
- YAML copy paths updated to `ctfmon.yaml`

### Preserved (intentionally unchanged)
- `go.mod` module path and all Go import paths (internal, garble-obfuscated)
- Go struct names (`Koolo`, `KooloCfg`, `SaveKooloConfig`) — obfuscated by garble at compile time
- GitHub upstream repo URLs (HTTPS only, not scanned by Warden)
- `upstreamRepo = "koolo"` constant (used only for GitHub API calls)

---

## Part 2: Timing Humanization

All fixed `time.Sleep()` calls in game-facing code have been replaced with humanized sleep functions that add randomized jitter, micro-pauses, and occasional longer breaks to mimic human behavior.

### New File: `internal/utils/human_sleep.go`

Four new functions:

| Function | Jitter | Micro-pause (15%) | Long pause (8%) | Use Case |
|----------|--------|-------------------|-----------------|----------|
| `CombatSleep(ms)` | ±15% | No | No | Attacks, skill casting |
| `HumanSleep(ms)` | ±30% | +10-50ms | No | NPC interaction, portals |
| `TownSleep(ms)` | ±30% | +10-50ms | +500-2000ms | Gambling, stash, repair |
| `Jitter(ms, pct)` | Custom | No | No | One-off duration calculations |

### Combat (119 calls across 24 files)
Replaced `time.Sleep(X * time.Millisecond)` → `utils.CombatSleep(X)` in all character build files:
- Barb builds: `berserk_barb.go`, `whirlwind_barb.go`, `warcry_barb.go`, `barb_leveling.go`, `barb_leveling_tools.go`
- Sorc builds: `blizzard_sorceress.go`, `nova_sorceress.go`, `sorceress_leveling.go`, `lightning_sorceress.go`, `hydraorb_sorceress.go`, `fireball_sorc.go`
- Paladin builds: `paladin_leveling.go`, `foh.go`, `hammerdin.go`, `Smiter (Ubers).go`
- Other builds: `amazon_leveling.go`, `assassin_leveling.go`, `javazon.go`, `mosaic.go`, `trapsin.go`, `wind_druid.go`, `druid_leveling.go`, `necromancer_leveling.go`

### Interaction (10 calls across 3 files)
Replaced with `utils.HumanSleep()`:
- `step/interact_npc.go` — NPC menu waits and retries
- `step/interact_entrance_packet.go` — Entrance transition waits
- `action/interaction.go` — Portal area sync delays

### Town (60 calls across 9 files)
Replaced with `utils.TownSleep()`:
- `gambling.go` — Shop refresh, window reopen
- `repair.go` — Repair button clicks
- `town.go` — Town portal usage
- `stash.go` — Stash tab switching
- `horadric_cube.go` — Cube transmute operations
- `cube_recipes.go` — Cube recipe execution
- `item.go` — Item identification, selling, buying
- `inventory.go` — Inventory management
- `autoequip.go` — Equipment swaps, equip delays

### Movement & Input (2 calls)
- `action/move.go` — Teleport/walk delays → `utils.CombatSleep(200)`
- `game/keyboard.go` — KeySequence inter-key delay → `utils.Jitter(200, 25)`

### Preserved (intentionally unchanged)
- Frame-sync polling values: 5ms, 10ms, 16ms, 20ms, 25ms
- `PlayerCastDuration()` — synchronized with game animation frames
- FCR breakpoint calculations (`barbFCR()`)
- Ping-aware sleeps (`utils.PingSleep`, `utils.PingMultiplier`)
- Supervisor/manager/bot infrastructure sleeps
- HTTP server, Discord, Telegram sleeps
- Battle.net auth flow sleeps
- State polling intervals (50ms state check, 40ms weapon check)

---

## Migration Notes

1. Rename your existing `config/koolo.yaml` to `config/ctfmon.yaml`
2. If you have an existing D2R mod folder at `mods\koolo\`, rename it to `mods\ctfmon\`
3. Rebuild using `better_build.bat` — the output binary will have a random GUID name as before
4. Verify: `grep -ri "koolo" build/` should return no results in the output directory
