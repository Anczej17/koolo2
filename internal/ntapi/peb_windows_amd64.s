#include "textflag.h"

// func getPEBDirect() uintptr
// Reads PEB address from GS:[0x60] (TEB.ProcessEnvironmentBlock slot).
//
// Raw-byte encoding of: MOVQ 0x60(GS), RAX
//   65        GS segment override
//   48 8B 04 25 60 00 00 00   MOV RAX, QWORD PTR GS:[0x60]
// Then return via standard Go ABI.
TEXT ·getPEBDirect(SB), NOSPLIT, $0-8
	BYTE $0x65
	BYTE $0x48
	BYTE $0x8B
	BYTE $0x04
	BYTE $0x25
	BYTE $0x60
	BYTE $0x00
	BYTE $0x00
	BYTE $0x00
	MOVQ AX, ret+0(FP)
	RET
