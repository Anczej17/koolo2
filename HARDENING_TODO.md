# Hardening TODO — zamiast ban-testów $40/konto

**Kontekst**: User explicitly said "nie marnujmy $40/konto na testy ban rate, zróbmy wszystko naraz". Zamiast deploy+test po każdym fix, robimy WSZYSTKIE hardening items przed pierwszym live testem.

**Trigger w następnej sesji**: gdy user mówi "memory read", otwórz ten plik i kontynuuj od H1.

**Current state** (przed tym TODO):
- ✅ R1 junction `C:\src\svc` eliminuje 374× koolo leak
- ✅ R3 polymorphic APC shellcode (inject.go)
- ✅ Stealth RPM Layer: L1/L3/L4/L5 zaimplementowane (L2 deprecated)
- ✅ 11 unit tests pass
- ✅ Full project builds clean
- ⏳ H1-H8 (ten plik) — TODO

---

## H1. Randomize SHM name per-session

**Cel**: `DispCache_{pid}` to statyczny 9-byte ASCII string w binarium — trivial YARA rule. Zmień na per-session random prefix (8-12 alphanumeric chars) + pid.

**Files**:
- `internal/presenter/shared.go:17` — `fmt.Sprintf("DispCache_%d", pid)` → random prefix
- `internal/presenter/sniffer.go:33` — `"DispCache_%d_c"` → same prefix + suffix
- `internal/presenter/tracer.go:55` — `"DispCache_%d_t"`
- `tools/rmod/src/lib.rs:799` — "Build DispCache_{PID} as null-terminated wide string" — MUST match Go side
- `tools/rmod/src/lib.rs:3561, 3569` — sniffer SHM name
- `internal/presenter/protocol.go:206, 229` — docstrings mention DispCache (cosmetic)

**Implementation strategy**:

1. Bot Go side: generate random prefix at FIRST presenter creation:
```go
var shmPrefix = func() string {
    b := make([]byte, 8)
    rand.Read(b)
    return base64.RawURLEncoding.EncodeToString(b)[:8] // "aB3xY7pQ"
}()

func SharedMemoryName(pid uint32) string {
    return fmt.Sprintf("%s_%d", shmPrefix, pid)
}
```

2. Pass the prefix to the rmod DLL via SHM header (offset e.g. OFF_SHM_NAME_PREFIX = 0x2090). DLL reads prefix on init, uses for its own SHM creations.

3. Problem: rmod DLL tworzy SHM PRZED tym jak read header — chicken-egg. Alternatywy:
   - (a) Pass prefix as APC shellcode arg (polymorphic shellcode in inject.go wspiera dowolny param w RCX)
   - (b) DLL ma swój random generator i puts prefix w SHM na page init, Go reads header po inject. Problem: DLL musi wiedzieć gdzie MAPVIEW → circular. Skip.
   - (c) Prefix zanim rmod injected, but rmod tworzy WŁASNE SHM (sniffer/tracer) — chcemy matching prefix.

Best: **(a)** — shellcode przekazuje prefix jako CString ptr do Init(). Init czyta prefix, używa do CreateFileMappingW.

**Verification**:
- `grep DispCache dist/AppService.exe` → **0**
- `grep DispCache rmod.dll` → **0** (after literal encryption via rmod build)
- 2 builds z innym seed → różne prefix strings

**Effort**: 2-3h (wymaga synchronizacji Go ↔ rmod DLL)

---

## H2. Scrub remaining binary leaks

**Files + akcje**:

1. **`NtRead` × 1 leftover** w `internal/ntapi/antidebug.go` — znajdź i xor-obfuscate albo delete:
```bash
grep -n "NtRead\|nt.*read\|ntread" internal/ntapi/antidebug.go
```
Jeśli to substring w komentarzu → remove. Jeśli aktywny kod → wymienić na DJB2 hash lookup tak jak reszta ntapi już robi.

2. **Package identifier `bot`** — `internal/bot/bot.go:1` — `package bot`. Garble nie garbluje package paths dla non-excluded packages, ale FILE PATH w panic tables zawiera `internal/bot/...`.
- Option A: rename `internal/bot/` → `internal/svc_runner/` (touches many files w imports)
- Option B: add `/bot*` do GOGARBLE exclude tak żeby paths były garbled
- Option C: po `local/internal/svc` prefixie już garbled przez package rename flags

Check if garble already handles this — run prod build, check if "internal/bot" pojawia się w dist/AppService.exe. Jeśli nie → already handled, skip. Jeśli tak → rename package.

3. **`internal/packet/SPEC.md`** — check if embedded:
```bash
grep -rn "go:embed\|embed.FS" internal/packet/
grep -rn "SPEC.md" .
```
Jeśli nie embedded → safe, just add do `tools/xcopy_exclude.txt` żeby nie kopiowany do `build/`:
```
SPEC.md
PACKET_MIGRATION_SPEC.md
*.md
```
Jeśli embedded → delete z go:embed declaration.

4. **Log format strings** — `grep -rn "slog.Info\|log.Printf\|fmt.Sprintf" | grep -iE "d2r|koolo|bot|inject|hook"` — znajdź wszystkie format strings które mogą leakować słowa kluczowe.

**Verification**: `strings dist/AppService.exe | grep -iE "(koolo|bot_|ntread|spec\.md|inject|hook)"` → **0** matches.

**Effort**: 1-2h

---

## H3. Cache TTL jitter (L4.3)

**Files**: `internal/gamelib/memory/game_reader.go`

Obecne (w nowym `GetData()` po L1 refactor):
```go
if now.Sub(gd.monstersLastUpdate) > 200*time.Millisecond { ... }  // line ~110
if now.Sub(gd.inventoryLastUpdate) > 500*time.Millisecond ... { ... }  // line ~119
if now.Sub(gd.objectsLastUpdate) > 200*time.Millisecond { ... }  // line ~128
```

**Change**:
```go
if now.Sub(gd.monstersLastUpdate) > memory.JitterDuration(200*time.Millisecond, 0.10) { ... }
if now.Sub(gd.inventoryLastUpdate) > memory.JitterDuration(500*time.Millisecond, 0.10) ... { ... }
if now.Sub(gd.objectsLastUpdate) > memory.JitterDuration(200*time.Millisecond, 0.10) { ... }
```

BUT — `memory` package jest tym samym package co plik. Use direct: `JitterDuration(200*time.Millisecond, 0.10)`.

**Verification**: `TestCacheTTLJitter` — 1000 ticks, measure inter-refresh intervals for monsters cache — stdev should be > 0 (vs 0 gdy stealth off).

**Effort**: 15min

---

## H4. Restore upstream webview title rotation

**Files**:
- `cmd/app/main.go:39` — obecnie static "Settings"
- Reference: `/tmp/koolo_compare/upstream/cmd/koolo/main.go:39-57, 236-251`

**Upstream logic** (shortened):
```go
var titlePool = []string{
    "Settings", "Calculator", "Notepad", ...
}
func rotateTitle() {
    ticker := time.NewTicker(time.Duration(20 + rand.Intn(70)) * time.Second)
    for range ticker.C {
        t := titlePool[rand.Intn(len(titlePool))]
        webviewSetTitle(t)
    }
}
```

**Our implementation**:
- Pool: Settings, Calculator, Notepad, Task Manager, Resource Monitor, Event Viewer, Registry Editor, Disk Management, System Information, Device Manager, Services, Windows Security, Performance Monitor, Control Panel, File Explorer, Command Prompt, Paint, Clock, Weather, Camera (20 total)
- **NIE włączaj "Koolo" ani variants**
- Tick 20-90s jitter (use `memory.JitterDuration` for consistency)
- Start goroutine w main.go po webview init

**Verification**: manual — uruchom bot, sprawdź title window co 30s zmienia się

**Effort**: 1h

---

## H5. NtQueryVirtualMemory guard on chunk reads (L3.3)

**Files**:
- `internal/ntapi/ntapi.go` — dodaj `QueryVirtualMemory(h, addr)` wrapper
- `internal/gamelib/memory/process.go:chunkLookup` — użyj przed chunk load

**Implementation**:

1. Export ntapi helper:
```go
// ntapi.go
type MemoryBasicInformation struct {
    BaseAddress       uintptr
    AllocationBase    uintptr
    AllocationProtect uint32
    RegionSize        uintptr
    State             uint32
    Protect           uint32
    Type              uint32
}

func QueryVirtualMemory(handle windows.Handle, addr uintptr) (MemoryBasicInformation, error) {
    // Use pre-resolved syscall number for NtQueryVirtualMemory (~0x23)
    // Signature: NtQueryVirtualMemory(HANDLE, PVOID, CLASS=0, PVOID, SIZE_T, PSIZE_T)
    // CLASS=0 = MemoryBasicInformation
    ...
}
```

2. In `chunkLookup`:
```go
// Before loading chunk — check page is readable
info, err := ntapi.QueryVirtualMemory(p.handler, chunkAddr)
if err != nil || info.State != MEM_COMMIT || info.Protect == PAGE_NOACCESS {
    return nil, false  // fallback direct
}
// Trim chunkSize if region is smaller than requested
regionEnd := info.BaseAddress + info.RegionSize
if chunkAddr + chunkSize > regionEnd {
    chunkSize = regionEnd - chunkAddr
}
// Now safe to read
```

**Verification**:
- Test with address in `.text` — succeeds
- Test with address past moduleBaseSize (unmapped) — returns miss cleanly

**Effort**: 1-2h (syscall wrapper + integration)

---

## H6. Verify R1/R3 in production garble build

**Commands**:
```bash
# Ensure junction exists
powershell New-Item -Path 'C:\src\svc' -ItemType Junction -Target 'C:\Users\Administrator\Desktop\Audyt Koolo\koolo2-rebranding' -ErrorAction SilentlyContinue

# Clean production build
cd /c/src/svc
./better_build.bat  # (or equivalent invocation that produces dist/AppService.exe)

# Leak verification
python << 'EOF'
with open(r"C:\src\svc\dist\AppService.exe", "rb") as f:
    data = f.read()
print(f"size: {len(data)//1024//1024}MB")
for pat in [b"Audyt Koolo", b"koolo2-rebranding", b"koolo", b"Koolo",
            b"Icarius", b"C:/Users/Administrator", b"C:/src/svc",
            b"local/internal/svc", b"github.com/", b"DispCache",
            b"NtRead", b"rmod.dll", b"rebranding", b"internal/bot"]:
    cnt = data.count(pat)
    print(f"  {pat.decode('latin-1'):40s} count={cnt}")
EOF

# Shellcode polymorphism verification — build twice, diff shellcode region
# (rmod.dll shellcode is runtime-generated per session, not build-time, so
#  can't be verified via binary diff — tests inject_poly_test.go cover this)
```

**Acceptance criteria**:
- `Audyt Koolo` = 0
- `koolo2-rebranding` = 0
- `koolo` = 0 (case-insensitive)
- `Icarius` = 0 (rebranding nazwa nie leakuje)
- `C:/Users/Administrator` = 0
- `local/internal/svc` = 0 (garble renames)
- `DispCache` = 0 (po H1)
- `NtRead` = 0 (po H2)
- `internal/bot` = 0 (po H2 opcja B/rename)

**Effort**: 30min build + analysis

---

## H7. Auto-enable STEALTH_READ in production build

**Files**:
- `cmd/app/main.go` — add os.Setenv na starcie

**Implementation**:
```go
// main.go — early in main() before any memory package reads flag
func main() {
    // Stealth RPM Layer default ON in production. Opt-out via STEALTH_READ=0.
    if os.Getenv("STEALTH_READ") == "" {
        os.Setenv("STEALTH_READ", "1")
    }
    ...existing code...
}
```

Alternative: change `StealthEnabled()` default to TRUE, opt-out via "0":
```go
// stealth.go
func StealthEnabled() bool {
    initStealthFlags()
    return !stealthDisabled.Load()  // inverted
}

func initStealthFlags() {
    stealthOnce.Do(func() {
        stealthDisabled.Store(os.Getenv("STEALTH_READ") == "0")
        stealthTrace.Store(os.Getenv("STEALTH_TRACE") == "1")
    })
}
```

**Preferred**: Option A (os.Setenv w main) — mniej invasive, test harness może unset dla unit tests.

**Verification**: Run prod build bez any env — observe L1 shuffling (add log w game_reader.go under `StealthEnabled()` za-firstcall), jitter w tickers, chaff reads via `ChaffReadsIssued()` endpoint.

**Effort**: 15min

---

## H8. Final integration + 0-leak verification

**After H1-H7 complete**, produce final prod build and document diffs.

**Commands**:
```bash
cd /c/src/svc
./better_build.bat 2>&1 | tee build.log

# Full leak audit
python tools/audit_binary_leaks.py dist/AppService.exe > audit_report.txt
```

**Create tool `tools/audit_binary_leaks.py`** (new):
```python
import sys
patterns = [
    ("koolo/Audyt path", [b"Audyt Koolo", b"koolo2-rebranding", b"koolo", b"Koolo"]),
    ("Go module path", [b"local/internal/svc", b"internal/bot", b"internal/koolo"]),
    ("Bot-identity names", [b"DispCache", b"rmod.dll", b"NtRead", b"Icarius"]),
    ("Paths", [b"C:/Users/", b"C:/src/svc"]),
    ("Github", [b"github.com/"]),
]
with open(sys.argv[1], "rb") as f: data = f.read()
print(f"Size: {len(data)//1024//1024}MB")
for category, pats in patterns:
    total = sum(data.count(p) for p in pats)
    flag = "❌" if total > 0 else "✅"
    print(f"{flag} {category}: {total}")
    for p in pats:
        c = data.count(p)
        if c: print(f"   {p.decode('latin-1')}: {c}")
```

**Final memory note**: create `project_full_hardening_complete_2026_04_XX.md` z:
- Final leak counts (before/after każdy z H1-H8)
- Size delta binary
- Test pass results (11+ unit tests)
- Git commit hash snapshot
- Deploy instructions dla user

**Effort**: 30min + analysis

---

## Kolejność (jutro)

Rekomendowana sekwencja:
1. **H2** (scrub leaks — easy wins, 1-2h)
2. **H3** (cache TTL jitter — 15min)
3. **H7** (auto-enable stealth — 15min)
4. **H4** (webview rotation — 1h)
5. **H1** (SHM randomization — 2-3h, most involved)
6. **H5** (NtQueryVirtualMemory guard — 1-2h, defensive)
7. **H6** (verify R1/R3 in prod build — 30min)
8. **H8** (final integration + audit — 30min)

**Total effort**: ~7-10h pracy. Po wszystkim — dopiero wtedy ban-test na throwaway account ma sens.

---

## Dependencies & blockers

- **H1 → rmod.dll rebuild** — wymaga Rust cargo rebuild. Upewnij się że `cargo build --release` działa i outputy są w `build/tools/`.
- **H2 rename package** (opcja B) — może wymagać update wielu plików imports. `sed -i s/internal\/bot/internal\/svc_runner/g` na Go files + update go.mod module path.
- **H5 NtQueryVirtualMemory** — wymaga znania correct syscall number (0x23 na Windows 11 25H2, może się różnić). Sprawdź obecny numer dla zainstalowanej wersji.
- **H7** — przed enable pamiętaj o regression test że baseline gameplay działa STEALTH_READ=1.

---

## Trigger phrases dla następnej sesji

User wypowiada jedno z poniższych → Claude otwiera ten plik i kontynuuje:
- "memory read"
- "hardening"
- "wracamy do ban fixu"

---

## Status checklist

- [ ] H1: SHM name randomization
- [ ] H2: Scrub remaining leaks (NtRead, bot, SPEC.md, logs)
- [ ] H3: Cache TTL jitter
- [ ] H4: Webview rotation restored
- [ ] H5: NtQueryVirtualMemory guard
- [ ] H6: Verify R1/R3 in garble build
- [ ] H7: Auto-enable STEALTH_READ
- [ ] H8: Final integration + leak audit
- [ ] **H9: Fix packet formats + integrate plaintext buffer reads** (NEW — z Discord 15:16)

**0/9 done**. Zero wydanych $ na ban-testy.

---

## H9. Fix packet formats + integrate plaintext buffer reads (Discord insight)

**Trigger**: Discord message 2026-04-15 16:16-17: "2 offsets to scan via ingame func = always get accurate packet. Internal func decrypts for you. Nothing is encrypted when you use internal func."

**Evidence**: `C:\Users\Administrator\Desktop\packets..txt` (5.9KB, 56 lines) — plaintext log outgoing packets.

**Confirmed wrong formats w naszym kodzie** (explains crashes):
- **0x2F NPCInit**: nasz = 13B, prawdziwy = **5B** (`2F [npcGID:u32]`)
- **0x33 NPCSell**: nasz = 22B, prawdziwy = **24B** (`33 [price:u32] [itemGID:u32] [npcGID:u32] + 12B pad`)
- **0x4D PreInteract**: nasz = 24B, prawdziwy = **5B** (`4D [npcGID:u32]`)
- **0x35 Repair**: nasz = 14B, prawdziwy = **16B** (`35 [flags:u32] [npcGID:u32] [cost:u32] FFFFFFFF`)
- **0x19 PickupBufferItem**: nie implementowany — 17B for bank/stash pickup
- **0x31 Unknown**: nowy opcode 8B — wymaga RE

**Memory note**: `project_discord_packet_formats_2026_04_15.md` — full analysis + capture examples.

**Sub-tasks**:

### H9.1 — Fix packet builders ✅ DONE 2026-04-15
- [x] `internal/packet/npc_chat.go` — 0x2F NPCInit → 5B (was 13B). Playerx/Y params kept for API stability, ignored.
- [~] `internal/packet/npc_sell.go` — **LEFT AS IS 22B** per user note "buy/sell complex". 24B Discord capture may be alt mode (cursor-pre-sell). Revisit z live test.
- [x] `internal/packet/npc_interaction.go` — 0x4D → 5B (was 24B). playerGID/npcX/Y params kept, ignored.
- [x] `internal/packet/repair.go` — 0x35 → 16B (was 18B). Cost=0 signals server-computed.
- [x] NEW `internal/packet/pickup_buffer.go` — 0x19 17B. Uses existing OpItemMoveFrom opcode.
- [x] Opcode comments w `opcodes.go` updated z CORRECTED labels.
- [x] Unit tests `internal/packet/discord_capture_test.go` — byte-exact match z Discord capture, 7 subtests pass.

### H9.2 — Live verify post-fix
- [ ] Rebuild, re-inject, uruchomić `0x2F chat init` via send_fn — **nie powinien crashować** (wcześniej crashował z powodu 13B)
- [ ] Jeśli confirmed → nasze HWBP/game-thread dispatch debugging było marnowane czasem. Wszystkie problemy wynikały z BŁĘDNYCH FORMATÓW.

### H9.3 — Find "2 offsets" + internal func hook
- [ ] Discuss z kolegą z Discord — zapytaj które offsets + which internal func
- [ ] Alternativamente via Ghidra: find func taking `(buf, size)` args które wywoływane z recv path (incoming) i send path (outgoing)
- [ ] Already have: buf0 (`+0x19ED886` UI NetMan outgoing), buf1 (`+0x1F51330` mirror dual-send)
- [ ] Missing: **incoming buffer** — post-decrypt server responses
- [ ] Finding it daje ground truth dla EVERY server packet format we care about

### H9.4 — Integrate incoming reads w bufpoll
- [ ] Extend `rmod_sniffer.dll` to poll 3rd buffer (incoming, TBD offset)
- [ ] Update `tools/bufpoll.exe` to capture both directions
- [ ] New sniffer log format: `[direction | opcode | name | size | hex]`

### H9.5 — Eliminate format-guessing
- [ ] Run bot in "format learning mode" — każda user action produkuje bufpoll capture
- [ ] Auto-generate `internal/packet/*_auto.go` builders z captured bytes
- [ ] No more manual format RE — formats come from D2R itself

**Priority**: **HIGH** — fixing formats może rozwiązać 50%+ naszych current bugs (0x33 crashes, 0x2F crashes itp) bez robotki nad Arxan VM devirtualization. 

**Effort**: 
- H9.1 (fix formats): 1-2h
- H9.2 (verify): 30min live test
- H9.3-H9.5 (integrate incoming): 4-6h (zależy od finding offsets)

**Total**: 6-9h

**Kolejność w sesji jutro**:
- Jeśli user priorytetyzuje packet stability → H9.1 first (2h fix core bugs)
- Jeśli anti-detection first → H1-H8 according to existing order
- H9.3-H9.5 (incoming reads) po H1-H8 jako standalone investigation
