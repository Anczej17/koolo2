package main

import (
	"encoding/binary"
	"fmt"
	"strings"
)

// Pattern is a parsed IDA-style AOB signature.
//
// Source format: space-separated tokens where each token is either a two-digit
// hex byte ("48", "8B", "05") or a wildcard ("??" or "?"). Wildcards match any
// byte during scanning.
type Pattern struct {
	Bytes []byte
	Mask  []bool // true = wildcard (any byte), false = exact match
	Raw   string
}

// ParsePattern turns an IDA-style signature into a Pattern.
// Returns an error if any token is not "??" or a valid hex byte.
func ParsePattern(sig string) (Pattern, error) {
	tokens := strings.Fields(sig)
	if len(tokens) == 0 {
		return Pattern{}, fmt.Errorf("empty pattern")
	}
	p := Pattern{
		Bytes: make([]byte, len(tokens)),
		Mask:  make([]bool, len(tokens)),
		Raw:   sig,
	}
	for i, tok := range tokens {
		if tok == "??" || tok == "?" {
			p.Mask[i] = true
			continue
		}
		if len(tok) != 2 {
			return Pattern{}, fmt.Errorf("bad token %q at index %d (expected 2 hex digits or ??)", tok, i)
		}
		b, err := hexByte(tok)
		if err != nil {
			return Pattern{}, fmt.Errorf("bad hex %q at index %d: %w", tok, i, err)
		}
		p.Bytes[i] = b
	}
	return p, nil
}

func hexByte(s string) (byte, error) {
	hi, err := hexNibble(s[0])
	if err != nil {
		return 0, err
	}
	lo, err := hexNibble(s[1])
	if err != nil {
		return 0, err
	}
	return (hi << 4) | lo, nil
}

func hexNibble(c byte) (byte, error) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', nil
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, nil
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, nil
	}
	return 0, fmt.Errorf("not a hex digit: %q", c)
}

// Scan finds every match of the pattern in buf. Returns the byte offsets of
// each match (relative to buf[0], not a VA).
func Scan(buf []byte, p Pattern) []int {
	var hits []int
	n := len(buf) - len(p.Bytes)
	if n < 0 {
		return nil
	}
	// Find the first non-wildcard anchor byte to skip impossible positions quickly.
	anchorIdx := -1
	var anchorByte byte
	for i, wild := range p.Mask {
		if !wild {
			anchorIdx = i
			anchorByte = p.Bytes[i]
			break
		}
	}
	if anchorIdx < 0 {
		// All wildcards — every position matches. Pathological, reject.
		return nil
	}
	for i := 0; i <= n; i++ {
		if buf[i+anchorIdx] != anchorByte {
			continue
		}
		match := true
		for j := 0; j < len(p.Bytes); j++ {
			if p.Mask[j] {
				continue
			}
			if buf[i+j] != p.Bytes[j] {
				match = false
				break
			}
		}
		if match {
			hits = append(hits, i)
		}
	}
	return hits
}

// ResolveRip reads a RIP-relative 4-byte signed displacement at
// buf[matchOff+operandOff..+4], and returns the target RVA:
//
//	target_rva = match_rva + operand_off + 4 + disp32
//
// match_rva is the offset within the module (not a VA). This matches how x86-64
// `lea rax,[rip+disp32]` encodes the target: RIP at the instruction's END (so
// the base for the displacement is operand_off + 4 from the instruction start).
func ResolveRip(buf []byte, matchOff, operandOff int) (int64, error) {
	if matchOff+operandOff+4 > len(buf) {
		return 0, fmt.Errorf("rip resolve out of buffer: matchOff=%d operandOff=%d", matchOff, operandOff)
	}
	disp := int32(binary.LittleEndian.Uint32(buf[matchOff+operandOff : matchOff+operandOff+4]))
	return int64(matchOff) + int64(operandOff) + 4 + int64(disp), nil
}
