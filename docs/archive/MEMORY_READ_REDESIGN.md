# R4 — Redesign interakcji z pamięcią D2R (propozycja)

**Kontekst**: Discord consensus — "if you want to make it past 10 days you need to rework how memory is interacted with the game". Obecne 4-5k RPM/s przez `kernel32!ReadProcessMemory` (lub nasz ntapi bypass) = fingerprint. Open handle do D2R z `PROCESS_VM_READ` widoczny w `NtQuerySystemInformation(SystemHandleInformation)` = single-query detect.

Ten dokument ocenia sześć podejść i rekomenduje jedno.

---

## Macierz detection vs effort

| # | Podejście | Detection surface | Effort (sprints) | Nowość (nie w upstream) |
|---|-----------|-------------------|------------------|-------------------------|
| A | **Shared memory via one-shot shellcode** | **Niskie** (brak persistent handle, brak RPM) | 1-2 | ★ Tak |
| B | Signed kernel driver + IOCTL | Bardzo niskie | 4+ | ★ Tak |
| C | Pre-injection (AppInit/launcher hijack) | Medium (file-system artifact widoczny) | 2-3 | ★ Tak |
| D | Parent-process adoption (Battle.net child) | Low | 3-4 | ★ Tak |
| E | Hyper-V host read VM guest | Zero surface w D2R | 5+ (wymaga VM setup) | ★ Tak |
| F | **Status quo (ntapi RPM)** | Wysokie (4-5k syscalls/s + open handle) | 0 | Obecne |

---

## A — Shared Memory Snapshot (REKOMENDACJA)

### Architektura

```
┌─────────────┐          ┌───────────┐           ┌─────────────┐
│  bot (Go)   │  open    │  shared   │   write   │   D2R       │
│             │◄─────────┤ section   ├──────────►│ (helper     │
│ reads ONLY  │          │ 128 KB    │  60 Hz    │  thread)    │
│ from section│          │           │           │             │
└─────────────┘          └───────────┘           └─────────────┘
     ▲                                                 ▲
     │                                                 │
     └── no handle to D2R ────┬────────────────────────┘
                              │
                              └── one-shot inject przy starcie,
                                  helper thread robi RPM→section,
                                  DLL usunęty po boot'ie
```

### Mechanizm

**Jednorazowo przy starcie bota**:
1. Nasza DLL/shellcode wstrzykiwana przez APC do D2R (POzOSTAWIAMY tę część — albo R3-zahardenowana polimorficzna)
2. DLL w D2R:
   - Tworzy `NtCreateSection` nazwaną sekcję (~128KB)
   - Mapuje ją w OWN address space + otrzymuje VA
   - Spawnuje jeden helper thread który: co ~16ms kopiuje interesujące D2R-struct VA do sekcji
   - Zwraca do bota (przez istniejący SHM `DispCache_{pid}`) ADRES sekcji i JUŻ NIE WYCHODZI
   - Po 2s sam-wygasza `MEM_DECOMMIT` całej swojej alokacji + `UnmapViewOfFile` + usuwa `G_SHM` handle
3. Bot mapuje TĘ SAMĄ sekcję w SWOIM procesie (`OpenFileMappingW` + `MapViewOfFile`)
4. **OD TEJ PORY**: bot zamknął wszystkie handle do D2R, nie robi żadnego RPM. Czyta byte-by-byte z mapowanej sekcji — zero syscall do D2R.

### Co kopiuje helper thread

Layout sekcji = exact copy naszej `memory.Data` struct:
- PlayerUnit (position, stats, skills, states, hp/mana) — ~500B
- Inventory (z equipment + stash) — ~4KB
- Monster list (up to 256 entries) — ~16KB
- Object list — ~4KB
- Roster (party members) — ~1KB
- Quest info — 96B
- Widget states — 64B
- Ping, game tick, area, in-town flag — 32B

Total ~30KB payload, ~128KB sekcja daje slack na wzrost.

Helper czyta bezpośrednio z D2R's statics (`DAT_141ec3fcc` etc) ** ** BEZ ** syscall'i bo jest W PROCESIE D2R.

### Detection surface (kontrast do status quo)

| Artefakt | Status quo | Approach A |
|----------|-----------|-----------|
| Handle do D2R w systemie | **TAK**, persistent | **NIE** (close po init) |
| RPM requests/sec do D2R PID | 4-5k/s | **0/s** |
| External syscalls wskazujące na D2R | 4-5k/s NtReadVirtualMemory | Zero |
| NT section mapping | Brak | Jeden `DispCache` (nazwa randomize'ana) |
| VAD walk: external alloc w D2R | Brak | Jeden MEM_PRIVATE region (one-shot, ale ZOSTAJE) |
| Helper thread w D2R | Brak | **1 thread** (widoczny w ThreadTable) — RYZYKO |

### Ryzyka

1. **Helper thread jest persistent signature** — Warden enumeruje threads D2R, widzi jeden bez ModuleBase match. Mitigacja:
   - Ukryj thread przez `NtSetInformationThread(ThreadHideFromDebugger)` — Arxan już to robi dla siebie
   - Wstrzyknij TLS callback który ping'uje z istniejącego D2R thread (Present hook już mamy — reuse go!)
2. **Section name enumerable** — randomize prefix per session (już w R3 plan), użyj GUID
3. **Helper thread RPMs zwiększają D2R CPU** — benchmark: 60 Hz × 30KB = 1.8 MB/s kopia w jednym procesie = trivial
4. **D2R crash = bot crash** — niepowiązane, ale section mapping powinno być recoverable

### Zmiany w kodzie (sprint 1)

- `tools/rmod/src/lib.rs`:
  - Dodaj funkcję `game_state_snapshot()` która kopiuje wszystkie offsety D2R do sekcji
  - Install na Present hook (już tam jesteśmy) — 60Hz natural
  - Section create w `init_from_shm`
- `internal/gamelib/memory/process.go`:
  - New mode `SectionReader` który implementuje interfejs `reader` ale czyta z sekcji zamiast RPM
  - Fallback na RPM jeśli sekcja brak
- Close D2R handle ASAP po init
- `internal/presenter/`:
  - After inject success, rozkaż DLL self-destruct (zostaw tylko: helper thread + section)
  - Usuń PE header, sections rdata/text, zachowaj tylko RWX page ze stub'em kopiującym

### Sprint 1 plan (~3-5 dni)

1. Dzień 1: rmod snapshot function + section layout
2. Dzień 2: SectionReader w Go
3. Dzień 3: integration + close-handle-on-init
4. Dzień 4: Present-hook scheduler (zamiast dedykowanego threada)
5. Dzień 5: benchmark + detection surface validation

---

## B — Signed Kernel Driver

### Architektura

Mini-driver (~20KB) robi `MmCopyVirtualMemory` od strony kernela do user-mode bota. D2R nie widzi NIC (syscall odbywa się cross-process via kernel). Zero handle, zero userland RPM.

### Problem

**Wymaga cert signature**. Windows 10+ blokuje unsigned drivers. Opcje:
- **Zapłać EV cert** ($$$ + KYC)
- **Test signing mode** (bcdedit /set testsigning on) — user'owi się wyświetla "Test Mode" watermark
- **Vuln driver BYOVD** (Bring Your Own Vulnerable Driver) — Microsoft vulnerable-driver blocklist łapie zbyt popularne; detection risk w UI warning "blocked vulnerable driver"
- **Kernel shim via Windows Defender exploitation** — rocznie zmienia się

### Verdict

**Pominąć**. Effort 4+ tygodni, cost $$$ lub banned drivery. Niewart dla naszej skali.

---

## C — Pre-injection (AppInit / IFEO / launcher hijack)

### Wariant AppInit_DLLs

- Klasyczna Windows feature: `HKLM\SOFTWARE\Microsoft\Windows NT\CurrentVersion\Windows\AppInit_DLLs` — lista DLL ładowanych do każdego procesu
- **Problem**: Wymaga SecureBoot disabled + admin reg write = detection trivial

### Wariant IFEO Debugger

- `Image File Execution Options\D2R.exe\Debugger = ourStub.exe`
- Windows uruchamia nasz stub zamiast D2R, my launchujemy D2R pod nami
- Detection: reg key trivial to scan; Blizzard launcher może sprawdzić
- Plus: staje się **parent-process adoption** (przyda się do D)

### Wariant Launcher hijack

- Zamień `D2RLauncher.exe` albo wstrzyknij w proces Battle.net
- **Problem**: Battle.net integrity check; update overwrite'uje; file tampering widoczne

### Verdict

**Parent-process adoption via IFEO** ma sens jako addon do A — robimy własny launcher który spawnuje D2R z nas jako parent, zamiast `StartGame()` z bota który staje się parent. Mniej overhead niż A sam, ale nie zastąpi A.

---

## D — Parent-Process Adoption

Oddzielna aplikacja "launcher" w stylu Blizzard App uruchamia D2R + naszego bota. Bot otwiera D2R via `PROCESS_DUP_HANDLE` od launcher'a zamiast `OpenProcess`. D2R widzi jako parent launcher, nie bot.

### Plus

Po wstrzyknięciu od launcher'a, bot może być uruchomiony później i dziedziczyć handle przez `DuplicateHandle(launcher, ourPid)`. Warden analizuje "kto mnie otworzył" — parent Battle.net-stylowy proces to mniejszy red flag niż `app.exe`.

### Minus

Zwiększa complexity deployment. Rozwiązuje "kto otwiera D2R" ale nie RPM frequency.

### Verdict

**Komplementarne do A**. A usuwa RPM, D usuwa open-by-bot signature. Razem = gold standard. D samo bez A = mało warty.

---

## E — Hyper-V Host Reads

Best-in-class detection evasion. Uruchom D2R w Hyper-V/VBS guest, bot na host. Hyper-V API pozwala czytać guest RAM bezpośrednio. D2R widzi jako "normal Windows" — no external handle.

### Minus

- Wymaga VBS/Hyper-V enabled (nie każdy może)
- GPU passthrough jest hell
- Wysoki overhead (10-20% FPS drop)
- Ban accounts prawdopodobnie wykryją VM (Blizzard już ban'uje Azure IP ranges, VM detection przez VMBus string itp)
- Setup 1-2 tygodnie dla user'a

### Verdict

**Out of scope** dla masowego use. Relevant tylko dla power-users.

---

## F — Status Quo (ntapi RPM)

Obecna architektura: ntapi indirect syscalls, 4-5k NtReadVirtualMemory/s na otwartym handle `PROCESS_VM_READ`.

**Problem**: Warden ma dokładnie ten check — `NtQuerySystemInformation(SystemHandleInformation)` + filter process handles pointing into D2R. Każdy bot widzi się po paru sekundach.

Doesn't matter jak dobrze scrubujemy syscalle — handle sam w sobie to fingerprint.

---

## Rekomendacja końcowa

### Pierwszy sprint: A — Shared Memory Snapshot

**Dlaczego**:
- Największy impact per effort (usuwa DWIE fingerprints: RPM frequency + open handle)
- Nie wymaga nowej infrastruktury (używamy istniejącego rmod.dll + Present hook + SHM)
- Reverse'owalne przy problemie (fallback na RPM)
- Discord wisdom wprost mówi co jest główny fingerprint = rework memory interaction — A dokładnie to robi
- Kosztuje 3-5 dni, nie tygodnie

### Drugi sprint (opcjonalnie): D — Parent Adoption

Po A, jeśli bany dalej występują w dłuższym oknie (>30 dni), dodaj launcher layer. Adoption usuwa ostatnią cross-process signature.

### NIE robić teraz

- **B** (kernel driver) — zbyt kosztowne, wymaga KYC cert
- **C** (AppInit/IFEO) — detection artifacts w rejestrze trivial
- **E** (Hyper-V) — user experience nieakceptowalny

---

## Kryteria sukcesu (A)

Po implementacji, benchmark:

1. `NtQuerySystemInformation(SystemHandleInformation)` + filter D2R → bot's handle count **= 0** (teraz 1+)
2. ETW `KernelProcess/ReadMemory` events z bot → D2R **= 0/s** (teraz ~4-5k/s)
3. Bot CPU, D2R CPU, FPS — w granicach 5% obecnego baseline
4. 10/10 game-state reads returnują tę samą zawartość co obecny RPM path (correctness regression test)
5. Ban survival test: 7+ dni ciągłej pracy bez wykrycia na throwaway account

### Deadline propozycja: **2026-04-22** (tydzień od dziś)

---

## Appendix — one-liner comparison

| Metryka | F (obecne) | A (propozycja) |
|---------|-----------|----------------|
| Handle do D2R po init | 1+ persistent | **0** |
| RPM to D2R per sec | 4000-5000 | **0** |
| Syscall traces to D2R | 4000-5000/s | **0/s** |
| Named section w D2R | `DispCache_{pid}` | `{randomGuid}` |
| Threads w D2R od nas | 0 (modulo Phase 8 already) | 0 (reuse Present hook) |
| Warden detection time | dni (discord consensus) | tygodnie-miesiące (hypothesis) |
| Implementation effort | — | 3-5 dni |
