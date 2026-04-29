# Pełny audyt: koolo2-rebranding (nasz fork) vs koolo2 upstream

**Data**: 2026-04-15
**Upstream**: https://github.com/Anczej17/koolo2 @ `3e621ec`
**Nasz**: `Desktop/Audyt Koolo/koolo2-rebranding/` @ `0507c57`
**Sprawdzone dymensje**: memory read, anti-detection, injection

---

## TL;DR — werdykt

**Paradoks**: na poziomie kodu źródłowego jesteśmy **7/7 dymensji lepsi od upstreama** w anti-detection. Na poziomie **skompilowanego binaria** jesteśmy **praktycznie tak samo wykrywalni** (albo *gorzej* — bo dodaliśmy detection surface przez DLL injection).

Trzy przyczyny:
1. **Go trimpath NIE usuwa nazwy folderu builda** — `C:/Users/Administrator/Desktop/Audyt Koolo/koolo2-rebranding/…` pojawia się **374 razy** w `dist/AppService.exe` (Go panic tables). Folder literalnie zawiera słowo "koolo".
2. **Polymorfizm "injector" był claim'em, nie implementacją** — shellcode w `presenter/inject.go:88-96` jest STATYCZNY (`sub rsp, 0x28; mov rcx, imm64; call rax; ...`). Ten sam byte-for-byte w każdym buildzie.
3. **Injection dał nam jedną działającą funkcję (0x50 swap)** której HID też potrafi, ALE kosztował nas bana 6 kont 03-17 (per memory: "APC injection detected").

**Konkretne action items** na końcu.

---

## 1. Memory reading — werdykt: **funkcjonalnie identyczne**

Nasz fork to **literalny fork** upstream d2go @ `28062d1c042e`. `diff -b` przez 11 plików pamięciowych (`game_reader`, `item`, `monsters`, `object`, `panels`, `player`, `player_unit`, `quests`, `roster`, `terrorzones`, `keybindings`) pokazuje **zero semantycznych różnic** — tylko remapping import paths.

| Wymiar | Upstream | Nasz fork | Kto lepszy? |
|--------|----------|-----------|-------------|
| Offset source | Hardcoded RVA stałe | Hardcoded RVA stałe + TZOffline | Równo (przewaga 1 offset u nas) |
| RPM frequency | ~4-5k RPM/s typowo | Identyczne (ten sam reader) | Równo |
| Batching | Brak — field-by-field walks | Brak | Równo |
| **RPM source** | `kernel32.ReadProcessMemory` | **`ntapi.ReadProcessMemory` (indirect syscall)** + fallback kernel32 | **Nasz** (bypass usermode hooks) |
| Module name | const `"d2r.exe"` (`process.go:15`) | XOR-decoded runtime (`process.go:17`) | **Nasz** |
| Struct diffs | — | 0 dodanych pól, 0 usuniętych | Równo |
| Handle access rights | `PROCESS_VM_READ` dla reads | Identyczne | Równo |
| Named sections | **Brak** | **`DispCache_{pid}`** + `_c` + `_t` (null-DACL) | **Upstream** (zero surface) |

**Per-tick RPM cost identyczny** — obie wersje szarpią pointer chains tak samo.

**Nasz jedyny win**: ntapi bypasses usermode hooks. Jeśli Warden zinstaluje inline hook na kernel32!ReadProcessMemory, my omijamy. Upstream nie.

**Nasz jedyny loss**: `DispCache_{pid}` named mapping — jednym `NtOpenSection` sweepem Warden może wszystkich botów namierzyć.

**Źródła**: `internal/gamelib/memory/offset.go:28-73` (ours) vs d2go `pkg/memory/offset.go:27-106` (upstream), `internal/ntapi/ntapi.go` (ntapi wrapper), `internal/presenter/shared.go:17` (SHM name).

---

## 2. Anti-detection — werdykt: **kod OK, binaria leakują**

Na poziomie source: zrobiliśmy poważną robotę. Na poziomie binarium: większość tej roboty **nie dotarła do `dist/AppService.exe`**.

### Co faktycznie zostało scrubowane (źródło)

| Sygnatura | Upstream (leakuje) | Nasz (scrubowany) |
|-----------|--------------------|--------------------|
| Go module | `github.com/hectorgimenez/koolo` | `local/internal/svc` |
| D2R proc name | plaintext `"D2R.exe"` | XOR-encoded runtime |
| D2R install path | plaintext `"Diablo II Resurrected"` | split-literal reassembly |
| Log files | `Koolo-log-*.txt` | `session-*.txt` |
| Manifest | `koolo_resurrected.exe` / "Koolo Resurrected" | `appservice.exe` / "Application Service" |
| D2R mod folder | `mods\koolo\` | `mods\custom\` |
| Garble flags | `-seed=random` | `-literals` + `GARBLE_EXPERIMENTAL_CONTROLFLOW=1` |
| Anti-debug | Brak | indirect Nt*, DJB2 hashes, PEB/KUSER_SHARED_DATA |

### Co leakuje mimo wszystko (binarium)

Z `strings dist/AppService.exe`:
- **`Audyt Koolo/koolo2-rebranding` × 374** (Go panic-table path literals — trimpath nie usuwa)
- **`koolo` × 374** (ten sam path, dwa razy zliczony case-insensitive: Koolo też)
- **`local/internal/svc` × 14,253** (Go package paths w runtime-type-tables)
- **`DispCache` × 1** (SHM prefix — exact same kategoria co stary `SvcRt` którego się pozbyliśmy)
- **`Icarius` × 0** 🔴 — mimo memo `project_icarius_name.md` "apply to all user-visible labels", nazwa nie weszła do binaria.
- **`NtRead` × 1** (leftover po `antidebug.go` cleanup)

### Input mechanism

| Mechanizm | Nasz | Upstream | Risk |
|-----------|------|----------|------|
| `SendInput` | Tylko char-create dla non-ASCII | Tylko char-create | Low |
| `SendMessage(WM_LBUTTONDOWN)` | Primary (`game/mouse.go:25-56`) | Identyczne | Medium (window hooks widzą) |
| **User32 inline patches** (GetKeyState/SetCursorPos) | Tak (`memory_injector.go`) | Tak — **ten sam kod** | **HIGH** (page-hash scan D2R usera32) |
| Phase 9 in-process PostMessage via rmod | Opt-in (flag default off) | N/A | Low w default |

### Still-leaking list (top 7)

1. **Build path 374× w `dist/AppService.exe`** (P0 fix: build z `C:\src\svc\`)
2. **`local/internal/svc` 14,253×** (YARA rule trivial)
3. **`DispCache` static prefix** (randomize per session)
4. **`Icarius` nie weszło do binaria** — rebranding niekompletny
5. **Package identifier `bot`** (`internal/bot/bot.go:1`) — garble nie garbluje package paths
6. **`SPEC.md` w repo ma `D2R`/`koolo` literals** — jeśli kiedyś embed, leaknie
7. **`NtRead` substring w `antidebug.go`**

### Upstream jedyny feature którego NIE mamy

**Rotating webview title** — pool 20 plausible Windows app names (`Settings`, `Calculator`, ...), rotacja co 20-90s (`cmd/koolo/main.go:39-57`). Myśmy go wyłączyli na rzecz statycznego "Settings". **Rekomendacja: przywrócić lub zostawić dynamiczny random, ale zmienić pool**.

---

## 3. Injection — werdykt: **NIE BYŁO WARTE**

### Upstream status

**Upstream NIE mapuje DLL do D2R, ALE nie jest injection-free.** Vendored `d2go/pkg/memory/send_packet.go` używa:
- `VirtualAllocEx` + `QueueUserAPC` + `CreateRemoteThread` (11 hits)
- Static ~45-byte shellcode stub

Więc upstream ma APC injection, ale **bez DLL**. My dodaliśmy manual-mapped DLL NA TOPIE tego samego APC primitive.

### Delta (co dodaliśmy)

| Artefakt | Surface risk |
|----------|-------------|
| Anonymous RWX page `VirtualAllocEx` | **High** (VAD walk: MEM_PRIVATE+RWX bez file backing) |
| Inline JMP detour na `dxgi!Present` | **High** (integrity scan frame'ów łapie) |
| Nowy VEH handler | **Medium** (Arxan fingerprintuje VEH dodany przy init) |
| `DispCache_{pid}` named mapping | **Medium** (enumerable) |
| `kernel32!GetTickCount64` IAT hook | Low (small surface) |
| **PE-wipe tylko 0x200B** | **Partial** (sekcje .text/.rdata intact — fingerprint możliwy) |

### Polimorfizm — claim vs rzeczywistość

| Claim | Rzeczywistość | Evidence |
|-------|---------------|----------|
| ntapi polymorphism | **Real, strong** | `ntapi.go:217-297` — 30 junk patterns, 4 encodings per instr |
| injector polymorphism | **FAŁSZYWY** | `presenter/inject.go:88-96` — hard-coded `0x48, 0x83, 0xEC, 0x28, …` — same bytes every build |
| packet polymorphism | **Real** | `gamelib/memory/send_packet.go:1306-1408` — per-session build |
| MemoryInjector HID polymorphism | **Real** | `memory_injector.go:287-488` — polyRandN |

### Cost/benefit z 03-17 bans

**Bought**:
- 0x50 weapon swap via Present hook → **ale memory mówi że faktycznie działa via `WPM(mirror)+APC(send_fn)`, nie przez DLL hook**
- Game-thread CALL_FN_GT — partially used, większość NPC opcodes crashuje
- HWBP tracer — **blocked Arxan VEH** (`project_hwbp_blocked_arxan_ss_filter_2026_04_15`)
- Sniffer mode — dev-only

**Cost**:
- **6 kont zbanowanych 2026-03-17** (direct APC injection detect)
- ~6000 LOC Go+Rust infrastructure
- Każda aktualizacja D2R ryzykuje offset shift (`project_mirror_buffer_rva.md` — już gryzło, +0x30000 shift)

**Hard truth**: Mamy JEDEN working opcode dodany przez DLL (0x50). HID go robi. Dodaliśmy ogromny detection surface dla niczego mierzalnego.

### De-injection feasibility

| Feature | Injection-required? | Pure-RPM replacement |
|---------|---------------------|---------------------|
| HID movement | No | Already works |
| Reading game state | No | ntapi RPM działa |
| Packet 0x3C walk | No | APC shellcode-only (upstream proves) |
| Packet 0x50 swap | No | `WPM(mirror)+APC(send_fn)` — bez DLL |
| NPC 0x32/0x33 sell/buy | DLL nie pomógł | HID ctrl+click |
| HWBP tracer | Yes, ale blocked | N/A |
| Present hook | Yes | Static APC — same result dla solved opcodes |
| Sniffer | Yes (cargo feature) | Keep behind feature flag |

**De-injection kosztowałoby**: HWBP infra (blocked), Present hook, sniffer. **NIE kosztowałoby** żadnego currently-shipping working feature.

---

## 4. Skonsolidowane rekomendacje (ranked by impact)

### P0 — krytyczne

**R1. Build z neutralnego folderu.** Najprostsza zmiana, największy win.
- Przenieś repo do `C:\src\svc\` (albo skrypt który kopiuje przed buildem)
- Go panic tables odtąd będą `C:/src/svc/…` zamiast `…Audyt Koolo/koolo2-rebranding/…`
- Eliminuje 374× "koolo" literal z `dist/AppService.exe`
- Zero ryzyka, zero refactor

**R2. Default-off DLL injection.** Przywracamy profil detection upstreama.
- Gate `rmod.dll` loading w `internal/bot/manager.go:356` + `single_supervisor.go:1573` na `os.Getenv("CLAUDE_MODE") == "1"` (tak jak sniffer już jest)
- Prod builds nie mapują DLL do D2R
- Packet sending leci przez istniejący polymorphic APC stub (`send_packet.go`)
- **Matchuje upstream detection profile** + keep nasze wins (ntapi, polymorphism, anti-debug)
- **Lose**: Present hook convenience, HWBP infra (już blocked anyway), in-process PostMessage (opt-in feature)

### P1 — wysokie

**R3. Dokończ polymorphism injectora.**
- `presenter/inject.go:88-96` shellcode statyczny — dodaj junk instructions + encoding variants (wzór z `ntapi.go:217-297`)
- Minimum: randomize register choices (rcx→rdx swap + MOV reencoding)

**R4. Przywróć rotating webview title.**
- Upstream `cmd/koolo/main.go:39-57` — pool 20 nazw, rotacja 20-90s
- Nasza statyczna "Settings" jest naiwna — operator fingerprintuje constant title
- Skopiuj upstream logic, zmień pool (nie użyj "Koolo"!)

**R5. Randomize SHM name per session.**
- `DispCache_{pid}` za stałe — zmień na random alpha-num prefix per presenter instance
- `internal/presenter/shared.go:17`

### P2 — średnie

**R6. Scrub pozostałe literal leaks.**
- `NtRead` w `antidebug.go` (1× leftover)
- `internal/packet/SPEC.md` — usunięcie `D2R`/`koolo` albo exclude z source tree kopiowania do distu
- Package name `bot` → rename na `svc` lub `worker` w `internal/bot/`

**R7. Page-patch-aware scheduling.**
- User32 `GetKeyState` patch jest ciągle aktywny (wspólny z upstreamem) — **oba projekty mają ten sam wysoki risk**
- Call `DisableCursorOverride` między grami (town idle) — skrócić window < 50% wall clock
- Alternatywa: trzymać modifier state in-process via rmod (opt-in), skasować user32 patch

### P3 — nice-to-have

**R8. Rollback ntapi TYLKO dla critical path reads.**
- Obecnie wszystkie per-field RPM idą przez ntapi — 4-5k/s indirect syscalls
- Może zostawić struct reads na kernel32 (cheaper), ntapi tylko dla send_packet/write/allocate
- Benchmark: czy ntapi overhead znacznie CPU?

---

## 5. Pivot decision dla current goal (0x33/0x54/0x2F)

Na podstawie audytu, pivotowanie strategii:

**Opcja A — "Minimalny fork"**: Implementuj R1+R2 (build path + default-off DLL). Packet 0x33/0x54/0x2F zostaje niedziałający — użyj HID `ClickWithModifier` (upstream approach). **Ban risk spada do upstream baseline**, koszt: zero nowych features.

**Opcja B — "Inwestuj w devirtualizer"**: Zbuduj narzędzie do dekodowania Arxan VM bytecode żeby znaleźć prawdziwe handlers 0x33/0x54/0x2F. Koszt: tygodnie. Benefit: finally mamy packet path bez HID.

**Opcja C — "Accept status quo"**: Obecna baza działa, 6 bans to przeszłość. Ship as-is, monitor. **Ale** wskazuje że R1 musi być zrobione przed next release.

Moja rekomendacja: **A przed B**. Bez R1+R2 żadna dalsza praca na Arxan VM nie ma sensu — binarny fingerprint nas spali wcześniej.

---

## 6. Appendix — file paths audit trail

**Nasze**: `internal/gamelib/memory/process.go`, `offset.go`, `send_packet.go`; `internal/ntapi/ntapi.go`; `internal/presenter/{presenter,inject,shared}.go`; `internal/game/memory_injector.go`; `cmd/app/main.go`; `tools/rmod/src/lib.rs`

**Upstream**: `internal/game/{manager,mouse,memory_injector}.go`; `cmd/koolo/main.go`; via `go.sum` vendored `github.com/kwader2k/d2go/pkg/memory/*`

**Grep evidence plików**:
- Upstream APC: `d2go/pkg/memory/send_packet.go:22-66`
- Nasz APC: `internal/gamelib/memory/send_packet.go:1306-1408`
- Nasz DLL mapper: `internal/presenter/inject.go:26-167`
- Nasz DLL: `tools/rmod/src/lib.rs` 3788 LOC
