package protocol

import (
	"encoding/json"
	"testing"
)

// Unknown page types (16..127) decode to the explicit ActionUnknown marker
// with the raw bytes preserved — never an error, never a dropped slot.
func TestDecodeKeymapUnknownPageTypeKeepsRaw(t *testing.T) {
	payload := make([]byte, KeymapSize)
	payload[2*4] = 42
	payload[2*4+1] = 1
	payload[2*4+2] = 2
	payload[2*4+3] = 3

	keymap, err := DecodeKeymap(payload)
	if err != nil {
		t.Fatalf("DecodeKeymap: %v", err)
	}
	want := KeyAction{Type: ActionUnknown, Params: [3]byte{1, 2, 3}, Raw: [4]byte{42, 1, 2, 3}}
	if keymap[2] != want {
		t.Errorf("slot 2 = %+v, want %+v", keymap[2], want)
	}
	// The marker must not disturb its neighbours: every slot decodes.
	if keymap[1] != (KeyAction{Type: ActionDefault}) {
		t.Errorf("slot 1 = %+v, want DEFAULT", keymap[1])
	}
	if keymap[3] != (KeyAction{Type: ActionDefault}) {
		t.Errorf("slot 3 = %+v, want DEFAULT", keymap[3])
	}
}

// pageType >= 128 is FUNC_V2; the raw pageType stays visible in Raw[0].
// Everything outside the Dt table (0..15) that is not >= 128 is the marker.
func TestDecodeKeymapPageTypeTable(t *testing.T) {
	payload := make([]byte, KeymapSize)
	cases := []struct {
		pageType byte
		want     KeyActionType
	}{
		{0, ActionDefault},
		{15, ActionMPT},
		{16, ActionUnknown},
		{42, ActionUnknown},
		{127, ActionUnknown},
		{128, ActionFuncV2},
		{0x82, ActionFuncV2},
		{255, ActionFuncV2},
	}
	for i, c := range cases {
		payload[i*4] = c.pageType
		payload[i*4+1] = 1
		payload[i*4+2] = 2
		payload[i*4+3] = 3
	}

	keymap, err := DecodeKeymap(payload)
	if err != nil {
		t.Fatalf("DecodeKeymap: %v", err)
	}
	for i, c := range cases {
		if keymap[i].Type != c.want {
			t.Errorf("slot %d (pageType %d): Type = %v, want %v", i, c.pageType, keymap[i].Type, c.want)
		}
		if keymap[i].Raw[0] != c.pageType {
			t.Errorf("slot %d (pageType %d): Raw[0] = %d, want the raw pageType preserved", i, c.pageType, keymap[i].Raw[0])
		}
		if keymap[i].Params != [3]byte{1, 2, 3} {
			t.Errorf("slot %d: Params = %v, want [1 2 3]", i, keymap[i].Params)
		}
	}
	// No slot is dropped: the far end of the table still decodes.
	payload[127*4] = 2
	keymap, err = DecodeKeymap(payload)
	if err != nil {
		t.Fatalf("DecodeKeymap: %v", err)
	}
	if keymap[127].Type != ActionKeyboard {
		t.Errorf("slot 127: Type = %v, want KEYBOARD", keymap[127].Type)
	}
}

func TestDecodeKeymapRejectsShortPayloads(t *testing.T) {
	if _, err := DecodeKeymap(make([]byte, KeymapSize-1)); err == nil {
		t.Error("DecodeKeymap accepted 511 bytes, want error")
	}
}

func TestKeyActionTypeLabels(t *testing.T) {
	for _, tt := range []struct {
		typ  KeyActionType
		want string
	}{
		{ActionDefault, "DEFAULT"},
		{ActionMouse, "MOUSE"},
		{ActionKeyboard, "KEYBOARD"},
		{ActionConsumer, "CONSUMER"},
		{ActionSystem, "SYSTEM"},
		{ActionExtraFunction, "EXTRA_FUNCTION"},
		{ActionMacro, "MACRO"},
		{ActionCB, "CB"},
		{ActionDKS, "DKS"},
		{ActionMT, "MT"},
		{ActionTGL, "TGL"},
		{ActionSOCD, "SOCD"},
		{ActionRS, "RS"},
		{ActionFunc, "FUNC"},
		{ActionEnd, "END"},
		{ActionMPT, "MPT"},
		{ActionFuncV2, "FUNC_V2"},
		{ActionUnknown, "UNKNOWN"},
	} {
		if got := tt.typ.String(); got != tt.want {
			t.Errorf("KeyActionType(%d).String() = %q, want %q", uint8(tt.typ), got, tt.want)
		}
		b, err := json.Marshal(tt.typ)
		if err != nil {
			t.Fatalf("Marshal(%v): %v", tt.typ, err)
		}
		if want := `"` + tt.want + `"`; string(b) != want {
			t.Errorf("Marshal(KeyActionType(%d)) = %s, want %s", uint8(tt.typ), b, want)
		}
	}
}
