package presenter

// Shared memory layout — MUST match Rust DLL's SharedBuffer struct.
// Total size: 4096 bytes (1 page).

const (
	// Grown 16384 → 131072 in P1-GID Phase A (2026-04-16): the legacy
	// command/crash/HWBP region [0..0x4000] is unchanged, but new regions
	// [0x4000..0x20000] hold per-Present PlayerUnit snapshot (see Rust
	// OFF_SNAPSHOT_* constants in tools/rmod/src/lib.rs).
	SharedBufSize = 131072
	// SharedMagic is a sentinel used to validate that the named file mapping
	// returned by OpenFileMappingW is actually ours (and not a stale one from
	// a previous process). The exact numeric value is not load-bearing — it
	// just needs to match the Rust DLL. Previously "RMOD" little-endian
	// (0x524D4F44 → "DOMR" in .rdata), which was a 4-byte static signature
	// Warden could trivially match. Replaced with an arbitrary constant that
	// doesn't spell anything and looks like a typical Windows handle cookie.
	SharedMagic   = 0x7E1A03D4
	SharedVersion = 1

	// Command types written by the Go side, read by the Rust DLL.
	CmdNop            = 0
	CmdSendPacket     = 1
	CmdSetCursor      = 2 // Phase 8C
	CmdSetKey         = 3 // Phase 8C
	CmdSniffInstall   = 4 // Install INT3 sniff hook on send_fn
	CmdSniffUninstall = 5 // Restore original send_fn byte
	CmdHwbpInstall    = 6 // Install hardware breakpoint via Dr0 on all threads
	CmdHwbpUninstall  = 7 // Clear Dr0/Dr7 on all threads
	CmdHwbpVerify     = 8 // Re-read DR0 from all threads, count still-armed
	CmdSendUIPacket   = 9  // Send packet via UI NetMan vtable[5] (identify/buy/sell/cube/gamble)
	CmdClick          = 10 // Phase 9: in-process click via D2R real_click_worker
	CmdSendDual       = 11 // memcpy to mirror buffer + send_fn (vendor/trade dual-send path)
	CmdForceClick     = 12 // click with ForceMove key held (for movement)
	CmdCallFn         = 13 // call arbitrary D2R function with up to 4 args (RENDER thread — deadlocks game-logic fns)
	CmdWriteMem       = 14 // write bytes to arbitrary D2R memory address
	CmdCallFnGT       = 15 // call D2R function on GAME THREAD via APC (no deadlock)
	CmdSendDualGT     = 16 // send packet via dual_send_wrap FROM GAME THREAD (GetTickCount64 hook)
	CmdTraceInstall   = 17 // PacketTracer: install trampoline JMP on send_fn + dual_send_wrap
	CmdTraceUninstall = 18 // PacketTracer: restore original bytes
	CmdDrProbe        = 22 // Diagnostic: probe whether SetThreadContext persists DR0 on D2R threads
	CmdHwbpReenum     = 23 // HWBP: re-arm DR0 on threads created since last install (idempotent)
	CmdSnapshotInit   = 24 // P1-GID Phase A: enable per-Present PlayerUnit snapshot into SHM
	CmdUninstallDetour = 25 // Graceful shutdown — restore Present prologue + remove VEHs
	CmdRopScan        = 26 // GID-4: scan D2R .text for ROP gadgets (ret-ending sequences), populate G_ROP_GADGETS
	CmdRopRead        = 27 // GID-5: copy D2R bytes via chained D2R gadgets (memcpy ROP). Currently gated (returns status=2 until trigger encoding verified).

	// Status values written by the Rust DLL, polled by Go.
	StatusBusy  = 0
	StatusDone  = 1
	StatusError = 2

	// Byte offsets into the shared buffer.
	offMagic           = 0x00
	offVersion         = 0x04
	offReadyFlag       = 0x08  // DLL sets to 1 once hook is installed
	offCommandFlag     = 0x0C  // Go sets to 1 to trigger command
	offStatusFlag      = 0x10  // DLL writes StatusDone/StatusError
	offCommandType     = 0x14  // CmdSendPacket / CmdSetCursor / ...
	offPacketSize      = 0x18  // Length of payload at offPacketData
	offErrorCode       = 0x1C  // Win32 error set by DLL on failure
	offFnSendPacket    = 0x20  // 8-byte ptr: game's send_packet fn
	offCursorX         = 0x28  // int32 — cursor X for Phase 8C
	offCursorY         = 0x2C  // int32 — cursor Y for Phase 8C
	offTargetKey       = 0x30  // uint8 — virtual key code
	offKeyActive       = 0x31  // uint8 — 1 = pressed, 0 = released
	offOriginalPresent = 0x34  // 8-byte ptr: saved original Present
	offDebugStep       = 0x3C  // DLL writes init progress step (debug)
	offGameThreadID    = 0x40  // u32: game thread ID for APC dispatch
	offRemoteViewAddr  = 0x48  // u64: DLL's MapViewOfFile address in D2R space
	offUINetManAddr    = 0x50  // u64: address of D2R UI NetMan global (CmdSendUIPacket)
	offFnRealClick     = 0x58  // u64: address of D2R real_click_worker (Phase 9)
	offHwndD2R         = 0x60  // u64: D2R window handle (Phase 9)
	offMirrorBufAddr   = 0x68  // u64: D2R global mirror buffer VA (dual-send)
	offForceMoveAddr   = 0x70  // u64: D2R keystate ForceMove VK entry VA
	offDualSendWrap    = 0x78  // u64: dual_send_wrap VA (game thread packet send)
	// offSessionPrefix: 8 wide chars + null term (18 bytes) holding the
	// per-session random SHM prefix. The DLL reads this on Init() so its own
	// SHM creates (sniffer, tracer) match Go-side names. Lives in the
	// fallback buffer the APC shellcode passes via RCX.
	offSessionPrefix   = 0x80  // u16[9]: 8-char wide prefix + null terminator
	offPacketData      = 0x100 // Start of variable-length payload
	maxPacketPayload   = 0xC00 - offPacketData // 2816 bytes (capped to leave room for sniff log)

	// Sniff hook ring buffer (must match Rust DLL constants)
	OffSniffHead       = 0xC00 // u32
	OffSniffTotal      = 0xC04 // u32
	OffSniffBpFired    = 0xC08 // u32 — VEH BP exception count (any BP)
	OffSniffSsFired    = 0xC0C // u32 — VEH SS our matching count
	OffSniffBpOurs     = 0xC10 // u32 — BPs at our send_fn addr
	OffSniffInstall    = 0xC14 // u32 — install_sniff_hook calls
	OffSniffUninstall  = 0xC18 // u32 — uninstall calls
	OffSniffLastRip    = 0xC1C // u32 — packed install diag OR low32(unmatched SS RIP)
	OffSniffEntries    = 0xC20
	SniffNumEntries    = 12
	SniffEntrySize     = 80
	SniffDataMax       = 76

	// Periodic re-arm worker stats (lib.rs OFF_HWBP_*).
	// These are SEPARATE from the install diag block — the worker writes here
	// instead of calling write_hwbp_diag, so the diagnostic counters are never
	// clobbered by the periodic re-arming activity.
	OffHwbpTickCount    = 0xFE0 // u32 — total ticks executed by worker
	OffHwbpNewArmed     = 0xFE4 // u32 — total threads newly armed across all ticks
	OffHwbpLastNewTid   = 0xFE8 // u32 — most recent newly-armed TID
	OffHwbpLastTickNew  = 0xFEC // u32 — newly-armed count from MOST RECENT tick
	OffHwbpWorkerAlive  = 0xFF0 // u32 — worker heartbeat counter

	// ---------------------------------------------------------------
	// Crash diagnostic VEH (0x1000 .. 0x12FF). Filled in-process by rmod.dll
	// when an unhandled SEH exception fires (AV / illegal-instruction / etc).
	// Bot polls these after detecting D2R exit to report what actually killed it.
	// ---------------------------------------------------------------
	OffCrashValid     = 0x1000 // u32 — 1 when fields below are populated
	OffCrashCount     = 0x1004 // u32 — total exceptions caught (including non-fatal)
	OffCrashCode      = 0x1008 // u32 — EXCEPTION_RECORD.ExceptionCode
	OffCrashFlags     = 0x100C // u32 — EXCEPTION_RECORD.ExceptionFlags
	OffCrashTID       = 0x1010 // u32 — thread ID that faulted
	OffCrashFaultType = 0x1014 // u32 — for AV: 0=read 1=write 8=DEP
	OffCrashRIP       = 0x1018 // u64 — instruction pointer (ExceptionAddress)
	OffCrashFaultVA   = 0x1020 // u64 — virtual address that faulted (AV only)
	OffCrashRSP       = 0x1028 // u64 — stack pointer at exception
	// Layout from 0x1030 — each block is tightly packed, no overlaps.
	OffCrashFrames    = 0x1030 // u64 * 16 = 128 bytes (0x1030-0x10AF)
	CrashFrameCount   = 16

	// Full integer register file captured from CONTEXT. Order matches winnt.h.
	OffCrashRegs      = 0x10B0 // 16 u64 = 128 bytes (0x10B0-0x112F)
	CrashRegCount     = 16     // RAX RCX RDX RBX RSP RBP RSI RDI R8..R15

	// In-process memory snapshots captured by the VEH for offline disassembly
	// (Arxan encrypts call-site code until execution; the handler fires AFTER
	// the bytes were decrypted so we copy them out before the page can be
	// re-encrypted).
	OffCrashRIPBytes      = 0x1130 // 128 bytes starting at RIP - 32 (0x1130-0x11AF)
	CrashRIPBytesPre      = 32     // bytes before RIP
	CrashRIPBytesLen      = 128    // total bytes to copy
	OffCrashFrameBytes    = 0x11B0 // 16 frames * 64 bytes = 1024 bytes (0x11B0-0x15AF)
	OffCrashFixups        = 0x15B4 // u32 — count of auto-fixups applied by VEH
	CrashFrameBytesPre    = 16
	CrashFrameBytesLen    = 64
	CrashFrameBytesStride = 64

	// ---------------------------------------------------------------
	// DR0 PERSIST PROBE (Arxan diagnostic, must match rmod lib.rs OFF_DRPROBE_*)
	//
	// Tells us whether SetThreadContext on D2R threads PERSISTS the DR0 value
	// or whether Arxan reverts it. If reverted, HWBP-based packet tracing is
	// dead-on-arrival.
	// ---------------------------------------------------------------
	OffDrProbeValid    = 0x1600 // u32 — 0=not run, 1=running, 2=done, 0xEEnn=err
	OffDrProbeTotal    = 0x1604 // u32 — total D2R threads enumerated
	OffDrProbeOk       = 0x1608 // u32 — count where DR0 readback == test value (PERSISTED)
	OffDrProbeRevert   = 0x160C // u32 — count where DR0 was reverted (Arxan stripped)
	OffDrProbeErr      = 0x1610 // u32 — count of probe errors (open/suspend/get/set fails)
	OffDrProbeNum      = 0x1614 // u32 — entries actually written to ring
	OffDrProbeEntries  = 0x1620 // start of entries — 32 B each, max 60 entries
	DrProbeEntrySize   = 32
	DrProbeMaxEntries  = 60

	// Test pattern that rmod writes to DR0 (must match rmod DRPROBE_TEST_DR0).
	DrProbeTestDR0     = 0x00007FFFBABEBEEF

	// ---------------------------------------------------------------
	// HWBP packet tracer (must match rmod lib.rs OFF_HWBP_*).
	// Lives in extended SHM region 0x2000..0x4000 (8 KB).
	// ---------------------------------------------------------------
	OffHwbpInstalled    = 0x2000 // u32 — 1 = HWBP armed
	OffHwbpTarget       = 0x2008 // u64 — target VA (default dual_send_wrap)
	OffHwbpFires        = 0x2010 // u32 — total SS exceptions matching target
	OffHwbpSsTotal      = 0x2014 // u32 — total SS exceptions seen (any RIP)
	OffHwbpLastRip      = 0x2018 // u64 — last SS RIP (debug, even if ≠ target)
	OffHwbpInstallOk    = 0x2020 // u32 — threads installed in last install
	OffHwbpInstallFail  = 0x2024 // u32 — install errors
	OffHwbpVerifyStill  = 0x2028 // u32 — threads still armed at verify
	OffHwbpVerifyLost   = 0x202C // u32 — threads where DR0 was cleared since install
	OffHwbpReenumNew    = 0x2030 // u32 — newly-armed threads in last reenum
	OffHwbpReenumTotal  = 0x2034 // u32 — total reenums executed
	OffHwbpRingHead     = 0x2038 // u32 — write index (wraps)
	OffHwbpRingTail     = 0x203C // u32 — read index (Go advances)
	OffHwbpRingTotal    = 0x2040 // u32 — total entries pushed
	OffHwbpRingDropped  = 0x2044 // u32 — entries dropped due to full ring
	OffHwbpVehAny       = 0x2048 // u32 — total VEH callbacks (any exception code)
	OffHwbpVehBp        = 0x204C // u32 — EXCEPTION_BREAKPOINT tally
	OffHwbpVehAv        = 0x2050 // u32 — EXCEPTION_ACCESS_VIOLATION tally
	OffHwbpVehOther     = 0x2054 // u32 — anything else
	OffHwbpVehLastCode  = 0x2058 // u32 — last non-SS exception code
	OffHwbpWorkerProg   = 0x2060 // u32 — worker progress code (0xC001/002/.../0xC0FF)
	OffHwbpWorkerTid    = 0x2064 // u32 — worker thread id
	OffHwbpWorkerSeen   = 0x2068 // u32 — threads seen (excl worker)
	OffHwbpWorkerOk     = 0x206C // u32 — threads armed
	OffHwbpWorkerFail   = 0x2070 // u32 — threads failed
	OffGtc64HookCount   = 0x2080 // u32 — times IAT hook trampoline fired
	OffGtc64InstallDiag = 0x2084 // u32 — progress/error codes during install
	OffHwbpRing         = 0x2100 // ring start
	HwbpEntrySize       = 256
	HwbpRingEntries     = 30

	// HWBP entry layout
	HwbpEntryOffTs        = 0x00 // u64 ts (currently fires counter, monotonic)
	HwbpEntryOffTID       = 0x08 // u32
	HwbpEntryOffReserved  = 0x0C // u32
	HwbpEntryOffRIP       = 0x10 // u64
	HwbpEntryOffRSP       = 0x18 // u64
	HwbpEntryOffRBP       = 0x20 // u64
	HwbpEntryOffRAX       = 0x28 // u64
	HwbpEntryOffRCX       = 0x30 // u64 (packet ptr)
	HwbpEntryOffRDX       = 0x38 // u64 (packet size)
	HwbpEntryOffR8        = 0x40 // u64
	HwbpEntryOffR9        = 0x48 // u64
	HwbpEntryOffR10       = 0x50 // u64
	HwbpEntryOffR11       = 0x58 // u64
	HwbpEntryOffCallstack = 0x60 // u64 × 16 = 128 B (RBP-walked)
	HwbpEntryOffPayload   = 0xE0 // u8 × 32 = 32 B (read from RCX)
	HwbpEntryPayloadMax   = 32

	// ---------------------------------------------------------------
	// In-process sniffer (second SHM: "DispCache_{pid}_c", 64KB)
	// Only present in rmod_sniffer.dll (--features sniffer).
	// ---------------------------------------------------------------
	CapShmSize       = 65536
	CapMagic         = 0xCAFE0050
	CapOffMagic      = 0x00
	CapOffEnabled    = 0x04
	CapOffHead       = 0x08
	CapOffTail       = 0x0C
	CapOffTotal      = 0x10
	CapOffDropped    = 0x14
	CapOffFrame      = 0x18
	CapOffBuf0RVA    = 0x20
	CapOffBuf1RVA    = 0x24
	CapOffBufWindow  = 0x28
	CapOffRing       = 0x400
	CapEntrySize     = 272
	CapEntryDataOff  = 16
	CapEntryDataMax  = 256
	CapRingBytes     = CapShmSize - CapOffRing
	CapMaxEntries    = CapRingBytes / CapEntrySize

	// ---------------------------------------------------------------
	// PacketTracer SHM (third SHM: "DispCache_{pid}_t", 256 KB)
	// In-process trampoline hook on send_fn + dual_send_wrap captures
	// every packet write with caller RIP, args (RCX..R9), TID, top-16
	// callstack frames (RBP-walked) and packet bytes. Used to find
	// D2R-internal handlers (e.g. who calls dual_send_wrap for 0x54
	// stash move) so we can CALL_FN_GT them on the game thread.
	// Only present in rmod_sniffer.dll (--features sniffer).
	// ---------------------------------------------------------------
	TraceShmSize          = 262144 // 256 KB
	TraceMagic            = 0xDC4A0010

	// Header (256 bytes reserved before ring start at 0x100).
	TraceOffMagic           = 0x00 // u32
	TraceOffEnabled         = 0x04 // u32 — capture on/off
	TraceOffHead            = 0x08 // u32
	TraceOffTail            = 0x0C // u32
	TraceOffTotal           = 0x10 // u32 — total entries pushed
	TraceOffDropped         = 0x14 // u32 — entries dropped (ring full)
	TraceOffSendFnVA        = 0x18 // u64 — send_fn VA hooked
	TraceOffDualSendWrapVA  = 0x20 // u64 — dual_send_wrap VA hooked
	TraceOffStubAddr        = 0x28 // u64 — RWX stub allocation
	TraceOffInstalledFlags  = 0x30 // u32 — bit0=send_fn hooked, bit1=dual hooked
	TraceOffLastErrorCode   = 0x34 // u32 — Win32 err of last install/uninstall
	TraceOffOrigBytesSendFn = 0x40 // 16 bytes — saved original send_fn[0..16]
	TraceOffOrigBytesDual   = 0x50 // 16 bytes — saved original dual_send_wrap[0..16]
	TraceOffD2RBase         = 0x60 // u64 — D2R module base (for RVA computation)
	TraceOffD2RTextEnd      = 0x68 // u64 — D2R .text end (frame filter)
	TraceOffGameThreadID    = 0x70 // u32 — known game TID (filter optionally)

	TraceOffRing            = 0x100

	// Ring entry layout (512 bytes per entry).
	TraceEntrySize          = 512
	TraceEntryOffTimestamp  = 0x00 // u64 — GetTickCount64() ms
	TraceEntryOffHookID     = 0x08 // u8 — 0=send_fn 1=dual_send_wrap
	TraceEntryOffPad1       = 0x09 // u8
	TraceEntryOffPayloadLen = 0x0A // u16 — bytes captured of packet
	TraceEntryOffTID        = 0x0C // u32 — calling thread id
	TraceEntryOffArgs       = 0x10 // u64 × 4 (RCX, RDX, R8, R9)
	TraceEntryOffCallstack  = 0x30 // u64 × 16 — top-16 frames (RBP unwound)
	TraceEntryOffPayload    = 0xB0 // 256 bytes
	TraceEntryPayloadMax    = 256

	TraceRingBytes  = TraceShmSize - TraceOffRing
	TraceMaxEntries = TraceRingBytes / TraceEntrySize

	// ---------------------------------------------------------------
	// SNAPSHOT (P1-GID Phase A). Rmod mirrors D2R PlayerUnit pointer
	// chain into these offsets every Present frame so bot reads
	// in-process — no cross-process ReadProcessMemory.
	// Rust side: tools/rmod/src/lib.rs OFF_SNAP_*.
	// ---------------------------------------------------------------
	OffSnapMagic         = 0x4000 // u32 — 'SNAP' = 0x50414E53
	OffSnapVersion       = 0x4004 // u32 — layout version (A=1)
	OffSnapTick          = 0x4008 // u64 — monotonic, bumped after each complete write
	OffSnapRegionCount   = 0x4010 // u32 — populated RegionEntry slots
	OffSnapDataBytes     = 0x4014 // u32 — bytes used in data blob
	OffSnapD2RBase       = 0x4018 // u64 — D2R.exe module base (debug)
	OffSnapFlags         = 0x4020 // u32 — bit0=enabled, bit1=main_player_found, bit2=error
	OffSnapLastErr       = 0x4024 // u32
	OffSnapLastRdtsc     = 0x4028 // u64 — debug timestamp
	OffSnapUnitTable     = 0x4030 // u64 — bot writes D2R.base+offset.UnitTable BEFORE CmdSnapshotInit
	OffSnapExpansion     = 0x4038 // u64 — bot writes D2R.base+offset.Expansion BEFORE CmdSnapshotInit
	OffSnapWaypointTable = 0x4040 // u64 — bot writes D2R.base+offset.WaypointTableOffset BEFORE CmdSnapshotInit

	// Generic static-region table (Phase B2). Bot populates before CmdSnapshotInit,
	// rmod mirrors each entry's [va, len] bytes every Present frame. Lets us add
	// new field coverage (Hover, UI, WidgetStates, FPS, etc.) without changing rmod.
	OffSnapStaticCount = 0x4048 // u32 — number of populated entries
	OffSnapStaticTable = 0x4050 // StaticRegion × SnapStaticMax, 16 B each
	SnapStaticMax      = 23
	SnapStaticEntrySz  = 16 // va:u64 | len:u32 | pad:u32

	// GID-4/5 ROP command offsets (0x3000 free band between HWBP and snapshot header).
	OffRopScanBase    = 0x3000 // u64 — scan region base VA
	OffRopScanLen     = 0x3008 // u64 — scan region length
	OffRopScanCount   = 0x3010 // u32 — out: gadgets harvested
	OffRopReadSrc     = 0x3018 // u64 — D2R VA to read from
	OffRopReadDst     = 0x3020 // u64 — SHM scratch VA to write into
	OffRopReadLen     = 0x3028 // u64 — bytes to copy
	OffRopReadStatus  = 0x3030 // u32 — out: 0=ok, 1=pool-miss, 2=exec-fail
	OffRopReady       = 0x3034 // u32 — 1 when ROP executor ready post-scan

	OffSnapRegions     = 0x4200 // RegionEntry[1024] × 16 B  (B3: grown 256→1024 for monster/object/entrance walker budget)
	SnapRegionMax      = 1024
	SnapRegionEntrySz  = 16 // va u64 | len u32 | offset u32

	OffSnapData        = 0x8200 // data blob base (0x4200 + 0x4000 region table)
	SnapDataSize       = 131072 - 0x8200

	SnapMagic          = 0x50414E53 // 'SNAP'
	SnapVersion        = 1

	SnapFlagEnabled          = 1 << 0
	SnapFlagMainPlayerFound  = 1 << 1
	SnapFlagError            = 1 << 2
)
