// zombie_killer: standalone utility that reaps stuck D2R processes so the VM
// does not need a reboot after an Arxan cascade. It closes every external
// handle whose target is a dead D2R process; the kernel releases the EPROCESS
// stub the moment the last reference drops.
package main

import (
	"flag"
	"fmt"
	"log/slog"
	"os"
	"time"

	"local/internal/svc/internal/game"
)

func main() {
	wait := flag.Bool("wait", false, "after closing handles, wait up to 5s and re-scan")
	quiet := flag.Bool("quiet", false, "suppress per-handle logs")
	flag.Parse()

	level := slog.LevelInfo
	if *quiet {
		level = slog.LevelError
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	closed, zombies := game.ForceKillZombieD2R(logger)
	fmt.Printf("closed=%d zombies=%d\n", closed, zombies)

	if *wait && zombies > 0 {
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			time.Sleep(250 * time.Millisecond)
			c, z := game.ForceKillZombieD2R(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})))
			if c == 0 && z == 0 {
				fmt.Println("drained clean.")
				return
			}
			if c > 0 {
				fmt.Printf("(rescan) closed=%d zombies=%d\n", c, z)
			}
		}
		fmt.Println("drain timed out — some handles may be held by SYSTEM or kernel drivers.")
	}
}
