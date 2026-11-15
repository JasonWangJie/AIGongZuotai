package studio

import "context"

// Optional actual-source evidence. Receipt reuse deliberately returns no
// HTTP success observation; it cannot heal a failed upstream/CDN circuit.
type MediaDeliveryObservedProvider interface {
	ImportDeliveryItemObserved(context.Context, string, MediaDeliveryItem) (Artifact, *MediaScheduleObservation, error)
}
