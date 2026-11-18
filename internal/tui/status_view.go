package tui

import (
	"github.com/virtpriv/node/internal/app"
)

func observationText[T any](o app.Observation[T], value string) string {
	if !o.Known() {
		return "unavailable"
	}
	if !o.Fresh() {
		return value + " (stale)"
	}
	return value
}
