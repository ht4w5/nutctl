package protocol

import (
	"strconv"
	"strings"
	"testing"
)

// The Key Action catalog is the picker's offering and the naming of Key
// Actions (ticket 06). Both ends are pinned here as external behavior: the
// wire bytes each choice binds are the vendor bundle's own lo() writer
// (docs/protocol.md §4), and a name is claimed ONLY for the exact bytes the
// catalog carries — every other byte pattern keeps its raw form visible.

// The four Key Action constructors encode exactly what the bundle's lo()
// writer emits for the same entry (docs/protocol.md §4): KEYBOARD keycode in
// param2, CONSUMER usage u16 LE in param1..2, MOUSE axis/value in
// param1..2, FUNC id big-endian in param1..3.
func TestKeyActionConstructorsEncodeTheWireBytes(t *testing.T) {
	cases := []struct {
		name string
		got  KeyAction
		want [4]byte
	}{
		{"keyboard Esc", KeyboardKey(41), [4]byte{0x02, 0x00, 0x29, 0x00}},
		{"keyboard L-Ctrl", KeyboardKey(224), [4]byte{0x02, 0x00, 0xe0, 0x00}},
		{"consumer volume +", ConsumerKey(233), [4]byte{0x03, 0xe9, 0x00, 0x00}},
		{"consumer calculator", ConsumerKey(402), [4]byte{0x03, 0x92, 0x01, 0x00}},
		{"mouse left", MouseButton(1, 1), [4]byte{0x01, 0x01, 0x01, 0x00}},
		{"mouse scroll down", MouseButton(3, 255), [4]byte{0x01, 0x03, 0xff, 0x00}},
		{"function id 1", FuncKey(1), [4]byte{0x0d, 0x00, 0x00, 0x01}},
		{"function id 25", FuncKey(25), [4]byte{0x0d, 0x00, 0x00, 0x19}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got.Raw != tc.want {
				t.Errorf("wire bytes = % x, want % x", tc.got.Raw, tc.want)
			}
			if encoded := EncodeKeymap(Keymap{0: tc.got}); string(encoded[:4]) != string(tc.want[:]) {
				t.Errorf("encoded Key Slot 0 = % x, want % x", encoded[:4], tc.want)
			}
		})
	}
}

// The catalog is the four kinds of Key Action the rebind picker offers, sized from the
// bundle's own tables (catalog.json's source field names them).
func TestCatalogOffersTheFourKinds(t *testing.T) {
	for _, tc := range []struct {
		kind  ActionKind
		name  string
		count int
	}{
		{KindKeyboard, "keyboard", 107},
		{KindConsumer, "consumer", 17},
		{KindMouse, "mouse", 7},
		{KindFunction, "functions", 287},
	} {
		got := Catalog(tc.kind)
		if len(got) != tc.count {
			t.Errorf("%s catalog has %d entries, want %d", tc.name, len(got), tc.count)
		}
		for i, ch := range got {
			if ch.Name == "" {
				t.Errorf("%s entry %d has no name", tc.name, i)
			}
		}
	}
}

// A Key Action the catalog carries renders as its name with its kind —
// the words the picker offers it under. DEFAULT stays bare.
func TestKeyActionTextNamesCatalogEntries(t *testing.T) {
	cases := []struct {
		action KeyAction
		want   string
	}{
		{KeyboardKey(41), `keyboard key "Esc"`},
		{KeyboardKey(224), `keyboard key "L-Ctrl"`},
		{ConsumerKey(233), `consumer key "Volume +"`},
		{ConsumerKey(226), `consumer key "Mute"`},
		{MouseButton(1, 4), `mouse button "Middle mouse button"`},
		{FuncKey(1), `Function "Restore Factory Settings"`},
		{KeyAction{}, "DEFAULT"},
	}
	for _, tc := range cases {
		if got := KeyActionText(tc.action); got != tc.want {
			t.Errorf("KeyActionText(% x) = %q, want %q", tc.action.Raw, got, tc.want)
		}
	}
}

// Bytes the catalog does not carry keep their raw form visible: a name is
// claimed only for exact wire bytes, so two different bindings never render
// the same and a diff of names never hides a byte difference.
func TestKeyActionTextFallsBackToRawOutsideTheCatalog(t *testing.T) {
	cases := []struct {
		name   string
		action KeyAction
		want   string
	}{
		{"keyboard with param1", action(ActionKeyboard, 1, 41, 0), "KEYBOARD(01 29 00)"},
		{"keyboard with param3", action(ActionKeyboard, 0, 41, 1), "KEYBOARD(00 29 01)"},
		{"unknown keycode", KeyboardKey(200), "KEYBOARD(00 c8 00)"},
		{"unknown function id", FuncKey(0x123456), "FUNC(12 34 56)"},
		{"unknown page type", KeyAction{Type: ActionUnknown, Raw: [4]byte{0x2a, 1, 2, 3}},
			"UNKNOWN(2a 01 02 03)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := KeyActionText(tc.action); got != tc.want {
				t.Errorf("KeyActionText(% x) = %q, want %q", tc.action.Raw, got, tc.want)
			}
		})
	}
}

// KeyActionName is the catalog's name lookup — the picker's own words — and
// returns nothing for bytes outside the catalog.
func TestKeyActionName(t *testing.T) {
	if got := KeyActionName(MouseButton(3, 1)); got != "Mouse scroll up" {
		t.Errorf(`KeyActionName(01 03 01 00) = %q, want "Mouse scroll up"`, got)
	}
	if got := KeyActionName(FuncKey(287)); got != "One-click pairing" {
		t.Errorf(`KeyActionName(function 287) = %q, want "One-click pairing"`, got)
	}
	if got := KeyActionName(KeyboardKey(200)); got != "" {
		t.Errorf("KeyActionName outside the catalog = %q, want empty", got)
	}
}

// Every catalog choice binds bytes its own name resolves back to — the
// picker round-trips through the same lookup the table renders with.
func TestCatalogChoicesRoundTripThroughKeyActionName(t *testing.T) {
	for _, g := range ActionKinds {
		for _, ch := range Catalog(g) {
			if got := KeyActionName(ch.Action); got != ch.Name {
				t.Errorf("%s choice %q binds % x named %q", g, ch.Name, ch.Action.Raw, got)
			}
			if text := KeyActionText(ch.Action); !strings.Contains(text, strconv.Quote(ch.Name)) {
				t.Errorf("%s choice %q renders as %q", g, ch.Name, text)
			}
		}
	}
}
