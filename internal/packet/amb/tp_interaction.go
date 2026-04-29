package amb

import (
	"local/internal/svc/internal/gamelib/data"
)

// NewTpInteraction creates a portal interaction packet using UnitInteractEx (0x41)
// This is a wrapper that uses the unified UnitInteractEx packet with proper object state
func NewTpInteraction(object data.Object) *UnitInteractEx {
	return NewPortalInteraction(object)
}
