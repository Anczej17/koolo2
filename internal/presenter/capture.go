package presenter

import (
	"fmt"
	"time"
)

// capture.go — inline-hook packet capture (Discord 2026-04-15 approach).
//
// Complements existing PacketTracer (external polling mode) with a FAST PATH
// that uses an inline JMP hook inside rmod.dll at send_fn entry. Every packet
// the game sends is captured with zero polling overhead and zero miss.
//
// Entries live in the main SHM HWBP ring region (repurposed — HWBP is blocked
// by Arxan's SS filter, ring is unused). Layout per rmod lib.rs:
//
//   +0x00  u64 ts_ms      (GetTickCount64)
//   +0x08  u32 tid         (calling thread)
//   +0x0C  u32 size        (packet size bytes)
//   +0x10  u64 pkt_ptr     (plaintext buffer VA in D2R heap)
//   +0x18  u8[232] payload (first up-to-232 bytes of packet)

// CapHookEntry is one packet capture.
type CapHookEntry struct {
	TsMs    uint64
	TID     uint32
	Size    uint32
	PktPtr  uint64
	Payload []byte
}

// CapHookStatus is a lightweight summary of ring state.
type CapHookStatus struct {
	Fires       uint32 // total hook fires since install
	RingHead    uint32
	RingTail    uint32
	RingTotal   uint32
	RingDropped uint32
}

// CapHookInstall arms the inline hook on send_fn inside D2R.
// Returns error if command fails (STATUS_ERROR) — usually means send_fn
// addr wasn't loaded in SHM (OFF_FN_SEND_PACKET = 0).
func (p *Presenter) CapHookInstall() error { return p.sendCommand(CmdTraceInstall) }

// CapHookUninstall restores the 14 stolen bytes and stops capture.
func (p *Presenter) CapHookUninstall() error { return p.sendCommand(CmdTraceUninstall) }

// CapHookSuppressVendor toggles send suppression for captured native vendor
// transaction packets. When enabled, rmod still records outgoing 0x32/0x33
// payloads but returns from send_fn before the packet reaches the server.
func (p *Presenter) CapHookSuppressVendor(enabled bool) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.initialized || p.localView == nil {
		return nil
	}
	if readU32(p.localView, uintptr(offCapabilities))&CapVendorSendSuppress == 0 {
		return fmt.Errorf("loaded rmod does not support vendor send suppression")
	}
	clearBytes(p.localView, uintptr(offPacketData), 8)
	if enabled {
		writeU32(p.localView, uintptr(offPacketData), 1)
	}
	writeU32(p.localView, uintptr(offCommandType), CmdCapSuppressVendor)
	writeU32(p.localView, uintptr(offStatusFlag), StatusBusy)
	writeU32(p.localView, uintptr(offCommandFlag), 1)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		status := readU32(p.localView, uintptr(offStatusFlag))
		if status == StatusDone {
			return nil
		}
		if status == StatusError {
			ec := readU32(p.localView, uintptr(offErrorCode))
			return fmt.Errorf("vendor suppress command error 0x%X", ec)
		}
		time.Sleep(100 * time.Microsecond)
	}
	return fmt.Errorf("vendor suppress command timeout")
}

// CapHookStatusRead returns the current ring stats.
func (p *Presenter) CapHookStatusRead() CapHookStatus {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.initialized || p.localView == nil {
		return CapHookStatus{}
	}
	return CapHookStatus{
		Fires:       readU32(p.localView, uintptr(OffHwbpFires)),
		RingHead:    readU32(p.localView, uintptr(OffHwbpRingHead)),
		RingTail:    readU32(p.localView, uintptr(OffHwbpRingTail)),
		RingTotal:   readU32(p.localView, uintptr(OffHwbpRingTotal)),
		RingDropped: readU32(p.localView, uintptr(OffHwbpRingDropped)),
	}
}

// CapHookDrain returns all unread capture entries and advances tail.
func (p *Presenter) CapHookDrain() []CapHookEntry {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.initialized || p.localView == nil {
		return nil
	}
	head := readU32(p.localView, uintptr(OffHwbpRingHead))
	tail := readU32(p.localView, uintptr(OffHwbpRingTail))
	if head == tail {
		return nil
	}
	max := uint32(HwbpRingEntries)
	out := make([]CapHookEntry, 0, 8)
	cur := tail
	for cur != head {
		entryOff := uintptr(OffHwbpRing) + uintptr(cur)*uintptr(HwbpEntrySize)
		size := readU32(p.localView, entryOff+0x0C)
		payloadLen := size
		if payloadLen > 232 {
			payloadLen = 232
		}
		e := CapHookEntry{
			TsMs:   readU64(p.localView, entryOff+0x00),
			TID:    readU32(p.localView, entryOff+0x08),
			Size:   size,
			PktPtr: readU64(p.localView, entryOff+0x10),
		}
		if payloadLen > 0 {
			buf := make([]byte, payloadLen)
			for i := uint32(0); i < payloadLen; i++ {
				buf[i] = readU8(p.localView, entryOff+0x18+uintptr(i))
			}
			e.Payload = buf
		}
		out = append(out, e)
		cur = (cur + 1) % max
	}
	writeU32(p.localView, uintptr(OffHwbpRingTail), head)
	return out
}
