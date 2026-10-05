package device

import (
	"context"
	"fmt"

	"github.com/ht4w5/nutctl/internal/protocol"
)

// Apply writes a complete State to the Device in batched writes: each block
// is one complete transfer (docs/protocol.md §4), in read order. It is the
// writeback half of the device model (PLAN Phase 4) — shared by every path
// that writes a whole State (`nutctl load` today, the TUI's apply later) —
// while the write gate that must precede it (ADR-0003) stays with the caller
// that speaks to the user.
func Apply(ctx context.Context, dev *protocol.Device, s State) error {
	for _, w := range []struct {
		block string
		write func() error
	}{
		{"base", func() error { return dev.SetKeymap(ctx, s.Base) }},
		{"fn", func() error { return dev.SetFnKeymap(ctx, s.Fn) }},
		{"lighting", func() error { return dev.SetLightingEffect(ctx, s.Lighting) }},
		{"per-key RGB", func() error { return dev.SetPerKeyRGB(ctx, s.PerKey) }},
		{"settings", func() error { return dev.SetSettings(ctx, s.Settings) }},
	} {
		if err := w.write(); err != nil {
			return fmt.Errorf("write %s block: %w", w.block, err)
		}
	}
	return nil
}

// ApplyVerified writes a complete State and then proves it landed by reading
// the Device back (spec user story 22): the read-back diff is the proof,
// never the write itself. It returns the Device's actual state (the new
// truth for every caller) and every difference between what was wanted and
// what the Device reports.
func (s *Session) ApplyVerified(ctx context.Context, want State) (State, []string, error) {
	if err := Apply(ctx, s.Dev, want); err != nil {
		return State{}, nil, err
	}
	got, err := s.Read()
	if err != nil {
		return State{}, nil, err
	}
	return got, StateDiffs(want, got, s.Model), nil
}
