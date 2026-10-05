package device

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/ht4w5/nutctl/internal/protocol"
)

// A State File (CONTEXT.md) is a plain file holding a complete snapshot of
// one Device's state, read or written only when the user explicitly saves or
// loads it (ADR-0005: no preset management, no background file writes). The
// Device itself is the source of truth.
//
// The format is pretty-printed JSON with the envelope
// {"model", "firmware", "schema", "state"} (the spec's State File format):
//
//	{
//	  "model": "NUT87",
//	  "firmware": "1.20",
//	  "schema": 1,
//	  "state": {
//	    "base":  [ … 128 Key Slot rows … ],
//	    "fn":    [ … 128 Key Slot rows … ],
//	    "lighting":   { … the Lighting Effect … },
//	    "perKeyRgb":  [ … 128 colors … ],
//	    "settings":   { … the Settings block … }
//	  }
//	}
//
// Only Device state is stored. The wire markers the SET format forces are
// deliberately absent — the Lighting Effect's driverSetting (0xFF on write),
// its check code (0xAA 0x55 on write) and each Per-Key RGB entry's ledId
// byte (the entry index on write) are derived on write and never round-trip
// through a file, which is what makes save → load → save yield identical
// State Files whatever the Device reports at those positions.

// SchemaCurrent is the State File schema version this build reads and writes.
const SchemaCurrent = 1

// State is the complete snapshot a State File holds: both Layers, the
// Lighting Effect, Per-Key RGB and Settings. (Macros are deferred to v0.1 —
// the spec's Feature slicing.)
type State struct {
	Base     protocol.Keymap         `json:"base"`
	Fn       protocol.Keymap         `json:"fn"`
	Lighting protocol.LightingEffect `json:"lighting"`
	PerKey   protocol.PerKeyRGB      `json:"perKeyRgb"`
	Settings protocol.Settings       `json:"settings"`
}

// StateFile is the envelope around one State snapshot: which Model it was
// saved from, on which firmware, under which schema version.
type StateFile struct {
	Model    string
	Firmware string
	Schema   int
	State    State
}

// stateFileOut is the on-disk envelope as written.
type stateFileOut struct {
	Model    string `json:"model"`
	Firmware string `json:"firmware"`
	Schema   int    `json:"schema"`
	State    State  `json:"state"`
}

// SaveStateFile writes the State File to path — pretty-printed JSON, 2-space
// indent, trailing newline. It is the one write the save/golden-read paths
// share: the golden read of ADR-0003 is just a State File.
func SaveStateFile(path string, f StateFile) error {
	out, err := json.MarshalIndent(stateFileOut{
		Model:    f.Model,
		Firmware: f.Firmware,
		Schema:   f.Schema,
		State:    f.State,
	}, "", "  ")
	if err != nil {
		return fmt.Errorf("encode State File %s: %w", path, err)
	}
	if err := os.WriteFile(path, append(out, '\n'), 0o644); err != nil {
		return fmt.Errorf("write State File %s: %w", path, err)
	}
	return nil
}

// LoadStateFile reads and validates the State File at path. Every envelope
// field and every state block must be present and decodable; the schema must
// be one this build understands. Nothing is checked against the connected
// Device here — that is Check's job.
func LoadStateFile(path string) (StateFile, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return StateFile{}, err
	}
	return parseStateFile(path, raw)
}

// stateFileJSON is the on-disk envelope as parsed: state stays raw so every
// block's presence can be checked before it is decoded.
type stateFileJSON struct {
	Model    string          `json:"model"`
	Firmware string          `json:"firmware"`
	Schema   *int            `json:"schema"`
	State    json.RawMessage `json:"state"`
}

// stateJSON is the on-disk shape of the state block.
type stateJSON struct {
	Base      json.RawMessage `json:"base"`
	Fn        json.RawMessage `json:"fn"`
	Lighting  json.RawMessage `json:"lighting"`
	PerKeyRGB json.RawMessage `json:"perKeyRgb"`
	Settings  json.RawMessage `json:"settings"`
}

func parseStateFile(path string, raw []byte) (StateFile, error) {
	var doc stateFileJSON
	if err := json.Unmarshal(raw, &doc); err != nil {
		return StateFile{}, fmt.Errorf("State File %s does not decode: %w", path, err)
	}
	if doc.Model == "" {
		return StateFile{}, fmt.Errorf("State File %s: no model field", path)
	}
	if doc.Firmware == "" {
		return StateFile{}, fmt.Errorf("State File %s: no firmware field", path)
	}
	if doc.Schema == nil {
		return StateFile{}, fmt.Errorf("State File %s: no schema field (this build writes schema %d)", path, SchemaCurrent)
	}
	if *doc.Schema > SchemaCurrent {
		return StateFile{}, fmt.Errorf(
			"State File %s: schema %d was written by a newer nutctl; this build understands schema %d",
			path, *doc.Schema, SchemaCurrent)
	}
	if *doc.Schema != SchemaCurrent {
		return StateFile{}, fmt.Errorf("State File %s: unknown schema %d (this build understands schema %d)",
			path, *doc.Schema, SchemaCurrent)
	}
	if len(doc.State) == 0 {
		return StateFile{}, fmt.Errorf("State File %s: no state block", path)
	}

	var blocks stateJSON
	if err := json.Unmarshal(doc.State, &blocks); err != nil {
		return StateFile{}, fmt.Errorf("State File %s: state does not decode: %w", path, err)
	}
	f := StateFile{Model: doc.Model, Firmware: doc.Firmware, Schema: *doc.Schema}
	for _, b := range []struct {
		name string
		raw  json.RawMessage
		into any
	}{
		{"base", blocks.Base, &f.State.Base},
		{"fn", blocks.Fn, &f.State.Fn},
		{"lighting", blocks.Lighting, &f.State.Lighting},
		{"perKeyRgb", blocks.PerKeyRGB, &f.State.PerKey},
		{"settings", blocks.Settings, &f.State.Settings},
	} {
		if len(b.raw) == 0 {
			return StateFile{}, fmt.Errorf("State File %s: state.%s is missing", path, b.name)
		}
		if err := json.Unmarshal(b.raw, b.into); err != nil {
			return StateFile{}, fmt.Errorf("State File %s: state.%s: %w", path, b.name, err)
		}
	}
	return f, nil
}

// Check validates the State File against the connected Device. A State File
// saved from another Model is refused — Key Slot ids are Model-specific and a
// friend's file must never scramble this Device (spec user story 25). A
// firmware mismatch is a warning, never a refusal (user story 26): it is
// returned for the caller to print while the load proceeds.
func (f StateFile) Check(m Model, firmware string) (warning string, err error) {
	if f.Model != m.Name {
		return "", fmt.Errorf(
			"wrong Model: this State File is for a %s, this Device is a %s — refusing to load (Key Slot ids are Model-specific)",
			f.Model, m.Name)
	}
	if f.Firmware != firmware {
		return fmt.Sprintf(
			"warning: State File was saved from firmware %s, this Device reports %s — proceeding anyway",
			f.Firmware, firmware), nil
	}
	return "", nil
}
