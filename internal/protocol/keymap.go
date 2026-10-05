package protocol

import (
	"encoding/json"
	"fmt"
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
