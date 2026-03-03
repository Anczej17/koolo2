# Patch Notes — Anti-Detection Suite

## Part 1: Identity Obfuscation

All externally visible references to the project name have been removed or randomized.

### Dynamic Naming (replaces hardcoded "ctfmon")
- Mod name is now **randomly generated** on first run (e.g. `svchost42`, `winctrl77`) and persisted in YAML config
- HTTP port is **randomized** in ephemeral range (49152–65535) and persisted
- Window title **rotates randomly** every 20–90 seconds through legitimate Windows app names (Visual Studio Code, Notepad, Calculator, etc.)
- No hardcoded fallback names remain in Go source — `DefaultModName()` and `DefaultHttpPort()` generate random values

### String Purge from Embedded Assets
- All `"koolo"` references removed from HTML templates, JS localStorage keys, CSS class names, and user-facing messages
- Dashboard title: "Koolo Resurrected" → "D2R Manager"
- Page titles neutralized: "Dashboard", "Settings", "Debug Screen"
- localStorage keys: `koolo:*` → `app:*`
- Example paths in UI: `G:\koolo\config\...` → `C:\Games\D2R\config\...`
- Config page: `kwader2k/koolo` references → `upstream`

### Window & UI
- Error dialogs: "Application error" / "Application will close" (no branding)
- Discord bot help: generic "Bot Commands" title

### Log Files
- Log file prefix: `svc-log-*`

### Configuration
- Config file: `config/ctfmon.yaml` (name kept for migration, but mod name is dynamic)
- All config load/save paths updated

### Build Scripts
- All user-facing messages neutralized (no project name)
- `better_build.bat` and `build.bat` updated

### HTTP User-Agent
- GitHub API requests: `Mozilla/5.0`

---

## Part 2: Timing Humanization

All fixed `time.Sleep()` calls in game-facing code replaced with humanized sleep functions.

### Functions (`internal/utils/human_sleep.go`)

| Function | Jitter | Micro-pause (15%) | Long pause (8%) | Use Case |
|----------|--------|-------------------|-----------------|----------|
| `CombatSleep(ms)` | ±15% | No | No | Attacks, skill casting |
| `HumanSleep(ms)` | ±30% | +10-50ms | No | NPC interaction, portals |
| `TownSleep(ms)` | ±30% | +10-50ms | +500-2000ms | Gambling, stash, repair |
| `Jitter(ms, pct)` | Custom | No | No | One-off duration calculations |

### Coverage
- **Combat**: 119 calls across 24 character build files
- **Interaction**: 10 calls across 3 files (NPC, entrances, portals)
- **Town**: 60 calls across 9 files (gambling, repair, stash, cube, items, inventory, equip)
- **Movement**: Teleport/walk delays, keyboard inter-key delay

### Preserved (intentionally unchanged)
- Frame-sync polling values (5ms, 10ms, 16ms, 20ms, 25ms)
- `PlayerCastDuration()` — game animation frame sync
- FCR breakpoint calculations
- Ping-aware sleeps (`utils.PingSleep`)
- Infrastructure sleeps (HTTP server, Discord, Telegram, Battle.net auth)

---

## Part 3: Shellcode Randomization

### Dynamic Shellcode Assembly (`memory_injector.go`)
- All hardcoded `[]byte{0x50, 0x48, ...}` shellcode patterns replaced with runtime-built instructions
- Each hook injection is prefixed with a **random NOP-equivalent sled** (0–3 instructions)
- 8 different NOP-equivalent encodings: `nop`, `66 nop`, multi-byte NOPs, `xchg reg,reg`, `lea reg,[reg+0]`
- **No two injections produce the same byte pattern**, even for the same hook type
- Builder functions: `buildCursorPosHook()`, `buildKeyStateHook()`, `buildSetCursorPosStub()`, `buildTrackMouseDisable()`

---

## Part 4: Build Hardening

### Garble Obfuscation
- Flags upgraded to `-literals -tiny -seed=random`
- `-literals`: encrypts all string literals at compile time, decrypts at runtime
- `-tiny`: removes additional metadata and debug info
- `GOGARBLE` preserves only packages that break under obfuscation (server, event, gowebview)

### Per-Build Entropy (`internal/buildnoise/`)
- `generate_noise.ps1` generates 4× 64-bit cryptographic random constants each build
- `noise.go` hashes entropy into a package-level variable, preventing linker dead-stripping
- Every build produces a binary with **unique hash, layout, and string table**
- Build tag separation: `noise_gen` tag for generated file, defaults fallback for dev builds

### Build ID Obfuscation
- Build variables renamed from `buildID`/`buildTime` to `_bMeta0`/`_bMeta1`
- GUID-based executable names (e.g. `a1b2c3d4-e5f6-7890-abcd-ef1234567890.exe`)

---

## Part 5: NT Syscall Layer (`internal/ntapi/`)

### Direct Syscall Trampolines (`ntapi.go`)
- Resolves syscall service numbers (SSN) from ntdll stubs at runtime
- **Halo's Gate**: if target stub is hooked (inline patch detected), scans up to 25 neighboring stubs in each direction to find a clean one and calculates target SSN by offset
- Builds **executable trampolines** in VirtualAlloc'd memory: `mov r10,rcx / mov eax,SSN / syscall / ret`
- Trampolines hardened: RWX → RX after write
- Calls **never touch hooked ntdll code** — completely bypasses usermode hooks

### Replaced API Calls
| Standard API | NT Syscall | Used In |
|---|---|---|
| `kernel32!ReadProcessMemory` | `NtReadVirtualMemory` | memory_injector.go |
| `kernel32!WriteProcessMemory` | `NtWriteVirtualMemory` | memory_injector.go |
| `kernel32!OpenProcess` | `NtOpenProcess` | memory_injector, crash_detector, manager |
| `kernel32!CloseHandle` | `NtClose` | memory_injector, crash_detector, manager |

### Graceful Fallback
- If SSN resolution fails entirely, falls back to standard Windows API
- Warning logged at startup, bot continues to function

---

## Part 6: Anti-Debug Protection (`ntapi/antidebug.go`)

### Detection Methods
1. **`IsDebuggerPresent`** — checks PEB.BeingDebugged flag
2. **`ProcessDebugPort`** — NtQueryInformationProcess class 7 (non-zero = debugger attached)
3. **`ProcessDebugObjectHandle`** — NtQueryInformationProcess class 30 (STATUS_SUCCESS = debug object exists)
4. **Timing check** — QueryPerformanceCounter measures a trivial loop; >50ms threshold indicates debugger breakpoints

### Runtime Monitor
- `StartAntiDebugMonitor()` runs checks every 30 seconds in a background goroutine
- On detection: **silent `os.Exit(0)`** — no error messages, no crash dumps, no traces

---

## Part 7: ETW & PEB Hardening

### ETW Patching (`ntapi/etw.go`)
- Patches `EtwEventWrite` in ntdll to immediately return STATUS_SUCCESS
- Patch: `xor eax,eax / ret` (3 bytes) with VirtualProtect roundtrip
- Prevents Event Tracing telemetry that anti-cheat can consume

### PEB Command Line Spoofing (`ntapi/peb.go`)
- Reads PEB via NtQueryInformationProcess ProcessBasicInformation
- Overwrites `CommandLine` UNICODE_STRING in `RTL_USER_PROCESS_PARAMETERS`
- Spoofs to `C:\Windows\System32\svchost.exe -k netsvcs`
- Allocates fresh buffer via VirtualAlloc (no corruption of original memory)

---

## Migration Notes

1. On first run, a random mod name and HTTP port are generated and saved to your YAML config
2. If upgrading from a previous version, your existing `config/ctfmon.yaml` will work as-is
3. Rebuild using `better_build.bat` — per-build noise is generated automatically
4. The anti-debug monitor starts automatically; no configuration needed
5. All NT syscall features are transparent — if they fail, standard API is used as fallback
