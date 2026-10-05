package device

import (
	"bytes"
	"context"
	"fmt"

	"github.com/ht4w5/nutctl/internal/protocol"
)

// stateBlocks are the five blocks of a State (PLAN Phase 4), in read order:
// each one's wire bytes and its write. The bytes are the SET wire shape —
// the markers the write path derives (docs/protocol.md §4) are encoded, not
// compared as state, so block equality sees exactly what the wire would see.
var stateBlocks = []struct {
	name  string
	bytes func(State) []byte
	write func(context.Context, *protocol.Device, State) error
}{
	{"base",
		func(s State) []byte { return protocol.EncodeKeymap(s.Base) },
		func(ctx context.Context, d *protocol.Device, s State) error { return d.SetKeymap(ctx, s.Base) }},
	{"fn",
		func(s State) []byte { return protocol.EncodeKeymap(s.Fn) },
		func(ctx context.Context, d *protocol.Device, s State) error { return d.SetFnKeymap(ctx, s.Fn) }},
	{"lighting",
		func(s State) []byte { return protocol.EncodeLightingEffect(s.Lighting) },
		func(ctx context.Context, d *protocol.Device, s State) error {
			return d.SetLightingEffect(ctx, s.Lighting)
		}},
	{"per-key RGB",
		func(s State) []byte { return protocol.EncodePerKeyRGB(s.PerKey) },
		func(ctx context.Context, d *protocol.Device, s State) error { return d.SetPerKeyRGB(ctx, s.PerKey) }},
	{"settings",
		func(s State) []byte { return protocol.EncodeSettings(s.Settings) },
		func(ctx context.Context, d *protocol.Device, s State) error { return d.SetSettings(ctx, s.Settings) }},
}

// Apply writes a complete State to the Device in batched writes: each block
// is one complete transfer (docs/protocol.md §4), in read order. It is the
// writeback half of the device model (PLAN Phase 4) — shared by every path
// that writes a whole State (`nutctl load` today) — while the write gate
// that must precede it (ADR-0003) stays with the caller that speaks to the
// user.
func Apply(ctx context.Context, dev *protocol.Device, s State) error {
	for _, b := range stateBlocks {
		if err := b.write(ctx, dev, s); err != nil {
			return fmt.Errorf("write %s block: %w", b.name, err)
		}
	}
	return nil
}

// ApplyChanges writes only the blocks of want that differ from have (the
// Device's actual state): applying local edits writes exactly the edited
// blocks and never the untouched ones. It is the batched writeback of the
// TUI's edit mechanics (spec user story 19: edits are local until applied).
func ApplyChanges(ctx context.Context, dev *protocol.Device, have, want State) error {
	for _, b := range stateBlocks {
		if bytes.Equal(b.bytes(have), b.bytes(want)) {
			continue
		}
		if err := b.write(ctx, dev, want); err != nil {
			return fmt.Errorf("write %s block: %w", b.name, err)
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
	return s.writeVerified(ctx, want, func() error {
		return Apply(ctx, s.Dev, want)
	})
}

// ApplyChangesVerified applies local edits (ApplyChanges) and proves them
// the same way: read the Device back and name every difference. The
// read-back is the truth; the diffs say what did not land.
func (s *Session) ApplyChangesVerified(ctx context.Context, have, want State) (State, []string, error) {
	return s.writeVerified(ctx, want, func() error {
		return ApplyChanges(ctx, s.Dev, have, want)
	})
}

// writeVerified is the one proof shape behind every apply: write, read back,
// diff. The proof is the read-back, never the write itself.
func (s *Session) writeVerified(ctx context.Context, want State, write func() error) (State, []string, error) {
	if err := write(); err != nil {
		return State{}, nil, err
	}
	got, err := s.Read()
	if err != nil {
		return State{}, nil, err
	}
	return got, StateDiffs(want, got, s.Model), nil
}
