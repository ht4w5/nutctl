package protocol

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

// KeyActionType is the Key Action page type (docs/protocol.md §4 `Dt` table).
// Values 0..15 are the table; ActionFuncV2 represents pageType >= 128;
// ActionUnknown is the explicit marker for pageTypes not in the table
// (16..127).
type KeyActionType uint8

const (
	ActionDefault       KeyActionType = 0
	ActionMouse         KeyActionType = 1
	ActionKeyboard      KeyActionType = 2
	ActionConsumer      KeyActionType = 3
	ActionSystem        KeyActionType = 4
	ActionExtraFunction KeyActionType = 5
	ActionMacro         KeyActionType = 6
	ActionCB            KeyActionType = 7
	ActionDKS           KeyActionType = 8
	ActionMT            KeyActionType = 9
	ActionTGL           KeyActionType = 10
	ActionSOCD          KeyActionType = 11
	ActionRS            KeyActionType = 12
	ActionFunc          KeyActionType = 13
	ActionEnd           KeyActionType = 14
	ActionMPT           KeyActionType = 15
	ActionFuncV2        KeyActionType = 128
	ActionUnknown       KeyActionType = 255
)

// String renders the page type the way the docs name it ("KEYBOARD",
// "FUNC_V2", "DEFAULT"); anything the table doesn't know is "UNKNOWN", the
// explicit marker — decoding never fails on a page type.
func (t KeyActionType) String() string {
	switch t {
	case ActionDefault:
		return "DEFAULT"
	case ActionMouse:
		return "MOUSE"
	case ActionKeyboard:
		return "KEYBOARD"
	case ActionConsumer:
		return "CONSUMER"
	case ActionSystem:
		return "SYSTEM"
	case ActionExtraFunction:
		return "EXTRA_FUNCTION"
	case ActionMacro:
		return "MACRO"
	case ActionCB:
		return "CB"
	case ActionDKS:
		return "DKS"
	case ActionMT:
		return "MT"
	case ActionTGL:
		return "TGL"
	case ActionSOCD:
		return "SOCD"
	case ActionRS:
		return "RS"
	case ActionFunc:
		return "FUNC"
	case ActionEnd:
		return "END"
	case ActionMPT:
		return "MPT"
	case ActionFuncV2:
		return "FUNC_V2"
	default:
		return "UNKNOWN"
	}
}

// MarshalJSON renders the page type as its label, so --json output is stable
// and readable (same pattern as ReportRate).
func (t KeyActionType) MarshalJSON() ([]byte, error) {
	return json.Marshal(t.String())
}

// KeyAction is one decoded Key Slot entry (4 wire bytes). Unknown page types
// decode to Type == ActionUnknown with Raw preserved — never an error, never
// dropped. For ActionFuncV2 the raw pageType stays visible in Raw[0].
type KeyAction struct {
	Type   KeyActionType `json:"type"`
	Params [3]byte       `json:"params"` // raw param1..3 (bytes 1..3)
	Raw    [4]byte       `json:"raw"`    // all 4 raw wire bytes
}

// Keymap is a decoded Layer: 128 Key Slots, index = Key Slot id.
type Keymap [128]KeyAction

// keyActionDoc is the State File row of one Key Slot (internal/device marshals
// Key Actions into a State File): the page-type label and hex byte strings
// with the spelling the --json output uses. Raw is the wire truth; type and
// params are derived from it on marshal, validated against it on unmarshal.
type keyActionDoc struct {
	Type   *string `json:"type,omitempty"`
	Params *string `json:"params,omitempty"`
	Raw    *string `json:"raw,omitempty"`
}

// MarshalJSON renders the Key Action as its State File row: page-type label,
// param bytes and raw wire bytes.
func (a KeyAction) MarshalJSON() ([]byte, error) {
	typ := a.Type.String()
	params := fmt.Sprintf("%02x %02x %02x", a.Params[0], a.Params[1], a.Params[2])
	raw := fmt.Sprintf("%02x %02x %02x %02x", a.Raw[0], a.Raw[1], a.Raw[2], a.Raw[3])
	return json.Marshal(keyActionDoc{Type: &typ, Params: &params, Raw: &raw})
}

// UnmarshalJSON parses a State File row. Raw is the wire truth and the only
// source of it: type and params are optional readability fields, validated
// against raw when present. A row that disagrees with itself is a loud error,
// never a silent guess.
func (a *KeyAction) UnmarshalJSON(b []byte) error {
	var doc keyActionDoc
	if err := json.Unmarshal(b, &doc); err != nil {
		return err
	}
	if doc.Raw == nil {
		return fmt.Errorf("needs raw (4 hex bytes): the raw wire bytes are the Key Action's truth")
	}
	raw, err := parseHexBytes(*doc.Raw, 4)
	if err != nil {
		return fmt.Errorf("raw %w", err)
	}
	got := KeyAction{
		Type:   keyActionType(raw[0]),
		Params: [3]byte{raw[1], raw[2], raw[3]},
		Raw:    [4]byte{raw[0], raw[1], raw[2], raw[3]},
	}
	if doc.Type != nil && *doc.Type != got.Type.String() {
		return fmt.Errorf("type %q disagrees with raw %q (which decodes as %s)",
			*doc.Type, *doc.Raw, got.Type)
	}
	if doc.Params != nil {
		params, err := parseHexBytes(*doc.Params, 3)
		if err != nil {
			return fmt.Errorf("params %w", err)
		}
		if params[0] != got.Params[0] || params[1] != got.Params[1] || params[2] != got.Params[2] {
			return fmt.Errorf("params %q disagrees with raw %q", *doc.Params, *doc.Raw)
		}
	}
	*a = got
	return nil
}

// parseHexBytes parses n space-separated hex bytes, e.g. "02 00 29 00".
func parseHexBytes(s string, n int) ([]byte, error) {
	fields := strings.Fields(s)
	if len(fields) != n {
		return nil, fmt.Errorf("%q is %d byte(s), want %d (space-separated hex bytes)", s, len(fields), n)
	}
	out := make([]byte, n)
	for i, f := range fields {
		v, err := hex.DecodeString(f)
		if err != nil || len(v) != 1 {
			return nil, fmt.Errorf("%q: %q is not a hex byte", s, f)
		}
		out[i] = v[0]
	}
	return out, nil
}

// UnmarshalJSON parses the Key Slot table of a State File block: exactly 128
// rows in slot order, failures naming the Key Slot.
func (km *Keymap) UnmarshalJSON(b []byte) error {
	var rows []json.RawMessage
	if err := json.Unmarshal(b, &rows); err != nil {
		return err
	}
	if len(rows) != len(km) {
		return fmt.Errorf("has %d rows, want %d (one per Key Slot)", len(rows), len(*km))
	}
	for i := range rows {
		if err := json.Unmarshal(rows[i], &km[i]); err != nil {
			return fmt.Errorf("Key Slot %d: %w", i, err)
		}
	}
	return nil
}

// DecodeKeymap decodes a reassembled GET_KEY / GET_FN_KEY payload
// (docs/protocol.md §4: 128 Key Slots × 4 bytes). Every slot decodes —
// unknown page types become the ActionUnknown marker with their raw bytes.
func DecodeKeymap(b []byte) (Keymap, error) {
	if len(b) < KeymapSize {
		return Keymap{}, fmt.Errorf("keymap payload too short: %d bytes, want at least %d", len(b), KeymapSize)
	}
	var km Keymap
	for i := range km {
		raw := b[i*4 : i*4+4]
		km[i] = KeyAction{
			Type:   keyActionType(raw[0]),
			Params: [3]byte{raw[1], raw[2], raw[3]},
			Raw:    [4]byte{raw[0], raw[1], raw[2], raw[3]},
		}
	}
	return km, nil
}

// EncodeKeymap encodes a Keymap into its 512-byte GET_KEY / SET_KEY payload
// (docs/protocol.md §4: 128 Key Slots x 4 bytes). Each Key Slot encodes to
// its wire bytes via wireBytes — raw bytes preserved by decode, or derived
// from Type and Params for a constructed Key Action. The whole block is
// written in one batched transfer (SET_KEY / SET_FN_KEY).
func EncodeKeymap(km Keymap) []byte {
	out := make([]byte, KeymapSize)
	for i, a := range km {
		raw := a.wireBytes()
		copy(out[i*4:], raw[:])
	}
	return out
}

// wireBytes is the 4-byte wire form of a Key Action (docs/protocol.md §4
// page-type table). Decoded Key Actions reproduce their raw bytes exactly;
// a constructed one derives them from Type and Params. FUNC_V2 and the
// explicit UNKNOWN marker carry their page byte in Raw[0].
func (a KeyAction) wireBytes() [4]byte {
	page := byte(a.Type)
	switch a.Type {
	case ActionFuncV2, ActionUnknown:
		page = a.Raw[0]
	}
	return [4]byte{page, a.Params[0], a.Params[1], a.Params[2]}
}

// keyActionType maps a wire pageType to its KeyActionType: the Dt table
// (0..15) as-is, >= 128 as FUNC_V2 (the raw pageType stays in Raw[0]), and
// anything else (16..127) as the explicit unknown marker.
func keyActionType(pageType byte) KeyActionType {
	switch {
	case pageType < 16:
		return KeyActionType(pageType)
	case pageType >= 128:
		return ActionFuncV2
	default:
		return ActionUnknown
	}
}
