package memory

import (
	"sync/atomic"
	"time"

	"local/internal/svc/internal/ntapi"
)

// chaff.go — Stealth Layer 5b: decoy RPM reads.
//
// While STEALTH_READ=1 a background goroutine issues occasional (3-8s)
// short bursts of reads to random VALID addresses inside D2R's .text
// range. The reads' return values are discarded; they exist purely to
// pollute the RPM trace that Warden-class scanners capture. Fixed-address
// checklist fingerprints (read [base+0x1E40058]; read [base+0x1EC3FCC];
// read [base+...]) become much harder to lift when 20-30% of the reads
// are uniformly-distributed across .text.
//
// Invariants:
//   * NEVER issues RPM when stealth is disabled — goroutine doesn't start.
//   * ONLY reads inside `[moduleBase, moduleBase + moduleBaseSize)` — i.e.
//     D2R's PE image range — to avoid AVs on unmapped regions.
//   * NEVER reads the critical offsets the real reader uses (chaff must
//     not perturb anything observable — we stick to pseudo-random offsets
//     and rely on D2R's large .text / .rdata to make collisions rare).
//   * Shutdown via atomic stop flag; set to 1 before dropping references.

type chaffState struct {
	stop       atomic.Bool
	reads      atomic.Uint64
	nextJitter atomic.Int64 // nanoseconds
}

var chaff chaffState

// StartChaffReader kicks off the background decoy-read goroutine for
// process p. Safe to call multiple times — later calls become no-ops
// while the goroutine is running.
func StartChaffReader(p *Process) {
	if !StealthEnabled() || p == nil || p.moduleBaseSize == 0 {
		return
	}
	if !chaff.stop.CompareAndSwap(true, false) && chaff.reads.Load() == 0 {
		// First-ever start: stop flag was default-false → race; set to false
		// explicitly and proceed. Subsequent calls while goroutine alive see
		// stop=false and skip.
		chaff.stop.Store(false)
	}
	go chaffLoop(p)
}

// StopChaffReader signals the goroutine to exit. Idempotent.
func StopChaffReader() {
	chaff.stop.Store(true)
}

// ChaffReadsIssued returns the count of decoy RPMs issued since start.
// Diagnostic only.
func ChaffReadsIssued() uint64 { return chaff.reads.Load() }

func chaffLoop(p *Process) {
	// Initial random delay so multiple bot instances don't sync on the
	// same wall-clock pulse.
	initialDelay := time.Duration(3_000+cryptRandN(5_000)) * time.Millisecond
	time.Sleep(initialDelay)

	for !chaff.stop.Load() {
		if !StealthEnabled() {
			return
		}

		// Burst: issue 4-12 chaff reads in quick succession to mimic a
		// natural read cluster, then sleep 3-8s until next burst.
		burstCount := 4 + cryptRandN(9)
		for i := 0; i < burstCount; i++ {
			if chaff.stop.Load() {
				return
			}
			chaffOne(p)
			// Tiny intra-burst jitter (1-10ms) so reads aren't back-to-back.
			time.Sleep(time.Duration(1+cryptRandN(10)) * time.Millisecond)
		}

		// Next burst in 3-8 seconds.
		next := time.Duration(3_000+cryptRandN(5_000)) * time.Millisecond
		chaff.nextJitter.Store(next.Nanoseconds())
		time.Sleep(next)
	}
}

func chaffOne(p *Process) {
	// Pick a random offset inside the module's image range. We stay within
	// [0, moduleBaseSize) so reads are always at a valid D2R VA.
	offset := uintptr(cryptRand64() % uint64(p.moduleBaseSize))
	addr := p.moduleBaseAddressPtr + offset

	// Randomize read size: 4, 8, 16, 32, 64 bytes. All tiny — avoids
	// crossing-page issues and keeps syscall overhead low.
	sizes := []uint{4, 8, 16, 32, 64}
	size := sizes[cryptRandN(len(sizes))]

	buf := make([]byte, size)
	// We use ntapi (indirect syscall) to match the same read path as
	// legitimate reads — otherwise chaff would be distinguishable from
	// real reads by its syscall origin. Result is discarded.
	_ = ntapi.ReadProcessMemory(p.handler, addr, &buf[0], uintptr(size))
	chaff.reads.Add(1)
}
