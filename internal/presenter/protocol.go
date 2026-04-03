package presenter

// Shared memory layout — MUST match Rust DLL's SharedBuffer struct.
// Total size: 4096 bytes (1 page).

const (
	SharedBufSize = 4096
	SharedMagic   = 0x524D4F44 // "RMOD"
	SharedVersion = 1

	// Command types written by the Go side, read by the Rust DLL.
	CmdNop        = 0
	CmdSendPacket = 1
	CmdSetCursor  = 2 // Phase 8C
	CmdSetKey     = 3 // Phase 8C

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
	offPacketData      = 0x100 // Start of variable-length payload
	maxPacketPayload   = SharedBufSize - offPacketData // 3840 bytes
)
