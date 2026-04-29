package bot

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// resolvePresenterDLL centralizes runtime module selection so Claude mode can
// safely default to plain rmod.dll while keeping sniffer opt-in for diagnosis.
func resolvePresenterDLL(claudeMode bool) (name, absPath, reason string, err error) {
	preferred := "rmod.dll"
	reason = "default presenter DLL"

	if env := strings.TrimSpace(os.Getenv("PRESENTER_DLL")); env != "" {
		preferred = env
		reason = "PRESENTER_DLL override"
	} else if claudeMode {
		if os.Getenv("CLAUDE_USE_SNIFFER") == "1" {
			preferred = "rmod_sniffer.dll"
			reason = "CLAUDE_USE_SNIFFER=1"
		} else {
			reason = "Claude mode defaults to plain rmod.dll; opt into sniffer with CLAUDE_USE_SNIFFER=1"
		}
	}

	preferredPath := filepath.Join("tools", preferred)
	if abs, aerr := filepath.Abs(preferredPath); aerr == nil {
		preferredPath = abs
	}
	if _, serr := os.Stat(preferredPath); serr == nil {
		return preferred, preferredPath, reason, nil
	} else if preferred != "rmod.dll" {
		fallback := filepath.Join("tools", "rmod.dll")
		if abs, aerr := filepath.Abs(fallback); aerr == nil {
			fallback = abs
		}
		if _, ferr := os.Stat(fallback); ferr == nil {
			return "rmod.dll", fallback, fmt.Sprintf("%s; fallback to rmod.dll because %s is missing", reason, preferred), nil
		}
	}

	return preferred, preferredPath, reason, fmt.Errorf("presenter DLL %s missing at %s", preferred, preferredPath)
}
