package packet

// Opcode-length lookup table.
//
// Source: buf=1 (mirror buffer) captures from logs/live_captures_2026_04_15/.
// The mirror buffer is zero-padded after the actual packet payload, so the
// trailing-zero boundary equals the true send length. buf=0 (UI NetMan) is
// NOT used here because it accumulates packet history across frames and
// cannot be trimmed reliably.
//
// Used by:
//   - The rmod sniffer (mirrored as a const array in tools/rmod/src/lib.rs)
//     to trim each ring-buffer entry to its real length instead of logging
//     the full 256-byte capture window.
//   - TrimPacket() below, called by Go-side sniffer consumers to normalize
//     packet content before persisting to JSONL.
//
// If a new opcode appears without an entry here, sniffer falls back to
// "trim trailing zeros only" and emits a `warn_unknown_opcode` tag.

// OpcodeLenInfo describes the observed length range for one opcode.
type OpcodeLenInfo struct {
	Min      uint16 // smallest actual length seen
	Max      uint16 // largest actual length seen
	Variable bool   // true when length depends on payload context (Min != Max or heuristic)
}

// OpcodeLengths — authoritative map, buf=1 derived.
// When a row is commented "builder", the value comes from the current Go
// builder rather than a live capture (no sample of that opcode in corpus).
var OpcodeLengths = map[byte]OpcodeLenInfo{
	// Movement
	0x03: {Min: 9, Max: 34, Variable: true},   // run: position-stream delta
	0x04: {Min: 18, Max: 18, Variable: false}, // move-to-entity

	// Skills (builder-sourced; not in 04-15 corpus)
	0x05: {Min: 5, Max: 5, Variable: false},
	0x06: {Min: 9, Max: 9, Variable: false},
	0x0C: {Min: 5, Max: 5, Variable: false},
	0x0D: {Min: 9, Max: 9, Variable: false},

	// Entity interact
	0x13: {Min: 9, Max: 9, Variable: false},

	// Inventory
	0x16: {Min: 5, Max: 5, Variable: false},   // pickup item (builder)
	0x17: {Min: 5, Max: 9, Variable: true},    // item drop — corpus-absent, leave wide
	0x18: {Min: 22, Max: 22, Variable: false}, // buffer → inv
	0x19: {Min: 10, Max: 22, Variable: true},  // inv → buffer (corpus shows 10 and 22)

	// Item right-click
	0x26: {Min: 17, Max: 17, Variable: false}, // identify tome (builder)
	0x27: {Min: 15, Max: 17, Variable: true},  // gold transfer — amount encoding

	// NPC chat — builder uses koolo-safe 5B (13B buf=1 form crashes D2R: that
	// shape is D2R's INTERNAL mirror write, not an externally-submittable
	// packet). Max covers the post-context variants for sniffer trim only.
	0x2F: {Min: 5, Max: 18, Variable: true}, // 5B safe, 13/18B ctx-dep
	0x30: {Min: 5, Max: 22, Variable: true}, // 5B safe, 22B post-trade

	// Merchants
	0x32: {Min: 22, Max: 22, Variable: false}, // NPC buy subform
	0x33: {Min: 22, Max: 23, Variable: true},  // builder 22B; buf=1 sample 23B (1B residue or real trailer — ambiguous, 1 sample)
	0x34: {Min: 2, Max: 5, Variable: true},    // ack (2B) vs cain-id-all (5B)
	0x35: {Min: 16, Max: 16, Variable: false}, // repair all

	// NPC services — 0x38 9B: 6B buf=1 form crashed D2R (Test27 04-19). 9B
	// is the koolo-original safe shape; 6B is D2R's post-process mirror view.
	0x38: {Min: 6, Max: 9, Variable: true},    // buf=1 6B informational; 9B wire-safe
	0x3A: {Min: 4, Max: 4, Variable: false},   // allocate stat (builder)
	0x3B: {Min: 4, Max: 4, Variable: false},   // learn skill (builder)
	0x3C: {Min: 9, Max: 15, Variable: true},   // 9B koolo-minimal; 15B adds cursor coords

	// World / travel
	0x40: {Min: 5, Max: 5, Variable: false},   // entrance (builder)
	0x41: {Min: 13, Max: 13, Variable: false}, // TP interact
	0x43: {Min: 13, Max: 13, Variable: false}, // TP confirm
	0x46: {Min: 8, Max: 12, Variable: true},   // cube op variant A — corpus-absent, leave wide
	0x49: {Min: 8, Max: 8, Variable: false},   // waypoint (builder)
	0x4B: {Min: 13, Max: 13, Variable: false}, // TP dest select (live 13B, NOT 5B)
	0x4D: {Min: 5, Max: 29, Variable: true},   // entity result — 5B clean, 29B w/ context

	// Character
	0x50: {Min: 28, Max: 30, Variable: true}, // weapon swap
	0x52: {Min: 18, Max: 28, Variable: true}, // merc hire/revive — leave range, corpus thin
	0x54: {Min: 20, Max: 20, Variable: false}, // cube transmute
	0x5C: {Min: 9, Max: 9, Variable: false},   // cain id per-item (sniffed)
}

// MaxLen returns the largest observed packet length for opcode, or 0 if
// unknown. Use this as an upper bound when slicing a polling buffer.
func MaxLen(opcode byte) uint16 {
	if info, ok := OpcodeLengths[opcode]; ok {
		return info.Max
	}
	return 0
}

// TrimPacket returns buf sliced to the probable real packet length.
//
// Rules:
//   - Known opcode: start from Max, strip trailing 0x00 until index == Min,
//     then stop. Guarantees we never cut into a legitimate short-form packet.
//   - Unknown opcode: strip trailing 0x00 only (conservative — caller should
//     log a warn_unknown_opcode for visibility).
//
// The input is expected to be taken from buf=1 (mirror buffer). Passing a
// buf=0 slice will produce meaningless results since that buffer retains
// history from prior sends.
func TrimPacket(buf []byte) []byte {
	if len(buf) == 0 {
		return buf
	}

	info, known := OpcodeLengths[buf[0]]
	if !known {
		n := len(buf)
		for n > 1 && buf[n-1] == 0 {
			n--
		}
		return buf[:n]
	}

	n := int(info.Max)
	if n > len(buf) {
		n = len(buf)
	}
	minN := int(info.Min)
	if minN > n {
		minN = n
	}
	for n > minN && buf[n-1] == 0 {
		n--
	}
	return buf[:n]
}
