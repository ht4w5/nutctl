package protocol

import (
	"bytes"
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

// The write path (ticket 03): a Keymap encodes back to the exact wire bytes
// the Device reported — the recorded GET_KEY block is the independent source
// of truth, and raw bytes are the wire truth of a Key Slot (decode preserves
// them, encode reproduces them, whatever page type they carry).
func TestEncodeKeymapReproducesRecordedBlock(t *testing.T) {
	x := loadFixture(t, "get_key", "nut87")
	payload := Reassemble(x.Responses, KeymapSize)
	keymap, err := DecodeKeymap(payload)
	if err != nil {
		t.Fatalf("DecodeKeymap: %v", err)
	}
	if got := EncodeKeymap(keymap); !bytes.Equal(got, payload) {
		for i := 0; i < KeymapSize; i += 4 {
			if !bytes.Equal(got[i:i+4], payload[i:i+4]) {
				t.Errorf("slot %d: encoded %X, want %X", i/4, got[i:i+4], payload[i:i+4])
			}
		}
	}
}

// A Key Action constructed in memory (no Raw) encodes from its Type and
// Params — the page-type table of docs/protocol.md §4. FUNC_V2 and the
// explicit UNKNOWN marker carry their page byte in Raw[0] and encode from
// there.
func TestEncodeKeymapFromTypeAndParams(t *testing.T) {
	var km Keymap
	km[0] = KeyAction{Type: ActionKeyboard, Params: [3]byte{0, 0x29, 0}}
	km[1] = KeyAction{Type: ActionConsumer, Params: [3]byte{0xE9, 0, 0}}
	km[2] = KeyAction{Type: ActionFunc, Params: [3]byte{1, 2, 3}}
	km[3] = KeyAction{Type: ActionFuncV2, Params: [3]byte{4, 5, 6}, Raw: [4]byte{0x82, 4, 5, 6}}
	km[4] = KeyAction{Type: ActionUnknown, Params: [3]byte{7, 8, 9}, Raw: [4]byte{42, 7, 8, 9}}

	got := EncodeKeymap(km)
	for slot, want := range [][]byte{
		{2, 0, 0x29, 0},
		{3, 0xE9, 0, 0},
		{13, 1, 2, 3},
		{0x82, 4, 5, 6},
		{42, 7, 8, 9},
	} {
		if !bytes.Equal(got[slot*4:slot*4+4], want) {
			t.Errorf("slot %d: encoded %X, want %X", slot, got[slot*4:slot*4+4], want)
		}
	}
}

// State File JSON of a Key Action (ticket 03): a readable row with the page
// type label and hex byte strings, carrying the raw wire bytes — the wire
// truth of a Key Slot. Raw is required and authoritative; type and params are
// optional readability fields, validated against it when present. The format
// is what internal/device's State File marshals.
func TestKeyActionStateJSON(t *testing.T) {
	a := KeyAction{Type: ActionKeyboard, Params: [3]byte{0, 0x29, 0}, Raw: [4]byte{2, 0, 0x29, 0}}
	b, err := json.Marshal(a)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if want := `{"type":"KEYBOARD","params":"00 29 00","raw":"02 00 29 00"}`; string(b) != want {
		t.Errorf("Marshal = %s, want %s", b, want)
	}

	for _, doc := range []string{
		`{"type":"KEYBOARD","params":"00 29 00","raw":"02 00 29 00"}`, // all three, consistent
		`{"raw":"02 00 29 00"}`, // raw only: type/params derived
	} {
		var got KeyAction
		if err := json.Unmarshal([]byte(doc), &got); err != nil {
			t.Fatalf("Unmarshal(%s): %v", doc, err)
		}
		if got != a {
			t.Errorf("Unmarshal(%s) = %+v, want %+v", doc, got, a)
		}
	}

	for _, bad := range []string{
		`{"type":"KEYBOARD","params":"00 39 00","raw":"02 00 29 00"}`, // params disagree with raw
		`{"type":"MOUSE","raw":"02 00 29 00"}`,                        // type disagrees with raw
		`{"type":"WHAT","raw":"02 00 29 00"}`,                         // unknown page-type label
		`{"raw":"02 00 29"}`,                                          // raw must be 4 bytes
		`{"type":"KEYBOARD","params":"00 29 00"}`,                     // raw is the truth and required
		`{}`,
	} {
		var got KeyAction
		if err := json.Unmarshal([]byte(bad), &got); err == nil {
			t.Errorf("Unmarshal(%s) accepted a corrupt Key Action row, want error", bad)
		}
	}
}

// An unknown page type round-trips through JSON with its raw page byte —
// the explicit UNKNOWN marker is never lost to a State File.
func TestKeyActionStateJSONUnknownPageType(t *testing.T) {
	a := KeyAction{Type: ActionUnknown, Params: [3]byte{7, 8, 9}, Raw: [4]byte{42, 7, 8, 9}}
	b, _ := json.Marshal(a)
	var got KeyAction
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("Unmarshal(%s): %v", b, err)
	}
	if got != a {
		t.Errorf("round trip = %+v, want %+v", got, a)
	}
}
