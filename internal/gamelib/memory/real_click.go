package memory

import "errors"

// real_click_worker — D2R's internal "user clicked at (x,y)" entry point.
//
// Reverse-engineered as the wndproc dispatcher chain's terminal worker:
//
//   WndProc → msg_classifier → mouse_dispatcher → real_click_worker
//
// ABI (decoded in logs/disasm_dispatcher.txt):
//
//   void __fastcall real_click_worker(
//       HWND hwnd,    // rcx
//       int  action,  // edx  10=DOWN  11=UP  12=DBLCLK
//       int  btn,     // r8d  1=L  2=M  4=R  8=X1  10=X2
//       int  y,       // r9d  client px
//       int  x);      // [rsp+0x28]  client px
//
// Internally boxes args into a stack struct and dispatches via vtable[1] of
// the resolved input context. Past that point everything is D2R's own state
// machine — pathfinding, walk/run gating, packet emission. We do not need to
// touch any of that.
//
// We use a known RVA fallback (same approach as send_fn). Current D2R base
// is 0x7ff7605d0000; real_click_worker is at 0x7ff7611795D0, giving RVA 0xBA95D0.
// Older dump sessions used base 0x7ff79d3d0000 where the function lived at
// 0x7ff79df795d0 — same RVA, different VA.
//
// If a future patch shifts the RVA, add a pattern scan here mirroring the
// send_packet.go scaffolding (prologue: 48 89 6C 24 10 48 89 74 24 18 ...).
const realClickWorkerKnownRVA uintptr = 0xBA95D0

// GetRealClickWorkerFn returns the absolute VA of D2R's real_click_worker
// in the current process image.
func (p *Process) GetRealClickWorkerFn() (uintptr, error) {
	if p == nil {
		return 0, errors.New("process is nil")
	}
	if p.moduleBaseAddressPtr == 0 {
		return 0, errors.New("module base address is zero")
	}
	return p.moduleBaseAddressPtr + realClickWorkerKnownRVA, nil
}
