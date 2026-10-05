package tui

import (
	"os"
	"strings"
	"testing"

	"github.com/ht4w5/nutctl/internal/device"
	"github.com/ht4w5/nutctl/internal/hidfake"
)

// The Keys screen and its rebind picker (ticket 06): the table of Key Slots
// with their current Key Actions beside the static ASCII TKL map, the Base
// Layer / Fn Layer toggle in place, the Knob's three gestures as rows like
// any Key Slot, the picker's four kinds of Key Action, Fn-disabled Key
// Slots marked and unbindable — and remaps through the pending/apply/revert
// mechanics (edit.go), proven at both agreed seams: golden frames, and
// every byte that reaches the fake Device.

// setBlockPayload assembles the payload of one SET transfer the fake
// received: the request chunks carry their offset in the header
// (docs/protocol.md §2), so the block the write sent can be checked byte
// for byte.
func setBlockPayload(t *testing.T, d *hidfake.Device, cmd byte) []byte {
	t.Helper()
	out := make([]byte, 512)
	seen := false
	for _, r := range d.Sent() {
		if len(r) <= 8 || r[1] != cmd {
			continue
		}
		seen = true
		addr := int(r[3]) | int(r[4])<<8
		copy(out[addr:], r[8:8+int(r[2])])
	}
	if !seen {
		t.Fatalf("no command %d request reached the Device", cmd)
	}
	return out
}

// sameBytes reports whether a payload slice holds exactly the four wire
// bytes want.
func sameBytes(got []byte, want [4]byte) bool {
	return len(got) == 4 && got[0] == want[0] && got[1] == want[1] && got[2] == want[2] && got[3] == want[3]
}

// setBlockScript scripts one SET exchange as the fake recorded it (the
// write-back echo of testdata/captures/set_*), accepting whatever request
// the code sends — the tests assert those bytes themselves.
func setBlockScript(d *hidfake.Device, t *testing.T, name string) {
	t.Helper()
	x := loadFixture(t, name, "nut87")
	for i := range x.Requests {
		if i < len(x.Responses) {
			d.Script(nil, x.Responses[i])
		} else {
			d.Script(nil)
		}
	}
}

// slotFault patches one Key Slot's four wire bytes into a recorded
// GET_KEY/GET_FN_KEY response — the way a test scripts a read-back that
// reports a remap, so a verified apply can verify.
func slotFault(t *testing.T, name string, slot int, raw [4]byte) fault {
	t.Helper()
	x := loadFixture(t, name, "nut87")
	want := slot * 4
	for i, r := range x.Responses {
		if len(r) < 8 {
			continue
		}
		addr := int(r[3]) | int(r[4])<<8
		if want >= addr && want+4 <= addr+int(r[2]) {
			return fault{cmd: x.Cmd, res: i, off: 8 + (want - addr), data: raw[:]}
		}
	}
	t.Fatalf("fixture %s carries no report covering Key Slot %d", name, slot)
	return fault{}
}

// --- the Keys screen ---

// The Keys screen is the table of Key Slots with their current Key Actions
// beside the static ASCII TKL map (spec user story 9): golden frame, so a
// layout regression in either pane is caught here.
func TestKeysScreenFrame(t *testing.T) {
	d := nut87(t, "/dev/hidraw3")
	m := newModel(t, &hidfake.Enumerator{Devices: []*hidfake.Device{d}})

	drive(t, m, key("2"))
	wantFrame(t, m, `nutctl — NUT87 at /dev/hidraw3 (firmware 1.20)
1 Device  [2 Keys]  3 Lighting  4 Settings

Keys — Base Layer
  ↑/↓ select · pgup/pgdn page · enter rebind · space Layer · * = differs from the Device — a applies, r reverts

>   0 Esc                    keyboard key "Esc"          Es F1 F2 F3 F4 F5 F6 F7 F8 F9 F10 F11 F12  Prt Scr Pau
    1 F1                     keyboard key "F1"           `+
		"`"+
		`~ 1  2  3  4  5  6  7  8  9  0  -  =  Bk  Ins Hm  PgUp
    2 F2                     keyboard key "F2"           Tb Q  W  E  R  T  Y  U  I  O  P  [  ]  `+
		"\\"+
		`   Del End PgDn
    3 F3                     keyboard key "F3"           Cp A  S  D  F  G  H  J  K  L  ;  '  En
    4 F4                     keyboard key "F4"           Sh Z  X  C  V  B  N  M  ,  .  /  Sh                  ↑
    5 F5                     keyboard key "F5"           Ct Wn Al Sp                Al Fn Mn Ct     ←   ↓   →
    6 F6                     keyboard key "F6"
    7 F7                     keyboard key "F7"
    8 F8                     keyboard key "F8"
    9 F9                     keyboard key "F9"
   10 F10                    keyboard key "F10"
   11 F11                    keyboard key "F11"
   12 F12                    keyboard key "F12"
   99 Print                  keyboard key "Print"
  100 Scroll                 keyboard key "Scroll"
  102 Pause                  keyboard key "Pause"
   16 `+
		"`"+
		` ~                    keyboard key "`+
		"`"+
		` ~"
   17 1 !                    keyboard key "1 !"
   18 2 @                    keyboard key "2 @"
   19 3 #                    keyboard key "3 #"

status: ready
help: 1-4/tab switch screen · s save · l load · a apply · r revert · q quit`)
}

// The Base Layer and the Fn Layer toggle in place (spec user story 10): the
// same table, the Layer named in the title, the Fn Layer's unbindable Key
// Slots marked "(fn-disabled)" (the CLI's spelling) — and the cursor and
// the edits stay where they were.
func TestKeysLayerToggleInPlace(t *testing.T) {
	d := nut87(t, "/dev/hidraw3")
	m := newModel(t, &hidfake.Enumerator{Devices: []*hidfake.Device{d}})

	drive(t, m, key("2"), key("down"), key("down"))
	drive(t, m, key(" ")) // the Layer toggles under the cursor

	wantFrame(t, m, `nutctl — NUT87 at /dev/hidraw3 (firmware 1.20)
1 Device  [2 Keys]  3 Lighting  4 Settings

Keys — Fn Layer
  ↑/↓ select · pgup/pgdn page · enter rebind · space Layer · * = differs from the Device — a applies, r reverts

    0 Esc                    Function "Restore Facto…    Es F1 F2 F3 F4 F5 F6 F7 F8 F9 F10 F11 F12  Prt Scr Pau
    1 F1 (fn-disabled)       keyboard key "F1"           `+
		"`"+
		`~ 1  2  3  4  5  6  7  8  9  0  -  =  Bk  Ins Hm  PgUp
>   2 F2 (fn-disabled)       keyboard key "F2"           Tb Q  W  E  R  T  Y  U  I  O  P  [  ]  `+
		"\\"+
		`   Del End PgDn
    3 F3 (fn-disabled)       keyboard key "F3"           Cp A  S  D  F  G  H  J  K  L  ;  '  En
    4 F4 (fn-disabled)       keyboard key "F4"           Sh Z  X  C  V  B  N  M  ,  .  /  Sh                  ↑
    5 F5 (fn-disabled)       keyboard key "F5"           Ct Wn Al Sp                Al Fn Mn Ct     ←   ↓   →
    6 F6 (fn-disabled)       keyboard key "F6"
    7 F7 (fn-disabled)       keyboard key "F7"
    8 F8 (fn-disabled)       keyboard key "F8"
    9 F9 (fn-disabled)       keyboard key "F9"
   10 F10 (fn-disabled)      keyboard key "F10"
   11 F11 (fn-disabled)      keyboard key "F11"
   12 F12 (fn-disabled)      keyboard key "F12"
   99 Print                  keyboard key "Print"
  100 Scroll                 keyboard key "Scroll"
  102 Pause                  keyboard key "Pause"
   16 `+
		"`"+
		` ~                    keyboard key "`+
		"`"+
		` ~"
   17 1 !                    keyboard key "1 !"
   18 2 @                    keyboard key "2 @"
   19 3 #                    keyboard key "3 #"

status: ready
help: 1-4/tab switch screen · s save · l load · a apply · r revert · q quit`)
}

// The Knob's three gestures — clockwise, press, counter-clockwise — are
// rows like any Key Slot (spec user story 13): at the end of the table,
// named by gesture, rebound through the same picker.
func TestKeysKnobGesturesAreRows(t *testing.T) {
	d := nut87(t, "/dev/hidraw3")
	m := newModel(t, &hidfake.Enumerator{Devices: []*hidfake.Device{d}})

	drive(t, m, key("2"))
	drive(t, m, key("pgdown"), key("pgdown"), key("pgdown"), key("pgdown"), key("pgdown"))

	got := frame(m)
	for _, want := range []string{
		"   13 knob clockwise         consumer key \"Volume +\"",
		"   15 knob press             consumer key \"Mute\"",
		">  14 knob counter-clockwise consumer key \"Volume -\"",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the table lacks the Knob row %q:\n%s", want, got)
		}
	}

	drive(t, m, key("enter")) // a Knob gesture rebinds like any Key Slot
	if got := frame(m); !strings.Contains(got, "Bind Key Slot 14 (knob counter-clockwise) on the Base Layer") {
		t.Errorf("enter must open the picker for the Knob gesture:\n%s", got)
	}
}

// --- the rebind picker ---

// The picker opens with the four kinds of Key Action — keyboard key,
// consumer key, mouse button and Function (spec user story 14) —
// and shows what the Key Slot is bound to now.
func TestKeysPickerOffersEveryKeyActionFamily(t *testing.T) {
	d := nut87(t, "/dev/hidraw3")
	m := newModel(t, &hidfake.Enumerator{Devices: []*hidfake.Device{d}})

	drive(t, m, key("2"), key("enter"))
	wantFrame(t, m, `
nutctl — NUT87 at /dev/hidraw3 (firmware 1.20)
1 Device  [2 Keys]  3 Lighting  4 Settings

Bind Key Slot 0 (Esc) on the Base Layer — now: keyboard key "Esc"
  ←/→ kind · ↑/↓ move · type to filter · enter bind · esc cancel

  [keyboard key 107]  consumer key 17  mouse button 7  Function 287
> A
  B
  C
  D
  E
  F
  G
  H
  I
  J
  K
  L

status: ready
help: enter bind · esc cancel · ctrl+c quit`)

	for _, tc := range []struct {
		press string
		bar   string
		entry string
	}{
		{"right", "[consumer key 17]", "Volume +"},
		{"right", "[mouse button 7]", "Left mouse button"},
		{"right", "[Function 287]", "Restore Factory Settings"},
	} {
		drive(t, m, key(tc.press))
		got := frame(m)
		if !strings.Contains(got, tc.bar) || !strings.Contains(got, tc.entry) {
			t.Errorf("the %s kind must show %q and %q:\n%s", tc.bar, tc.entry, tc.entry, got)
		}
	}
}

// Typing filters the kind — the way to find one entry among 287
// Functions — and `enter` binds the highlighted entry into the local edit
// buffer (never the wire: spec user story 19).
func TestKeysPickerFiltersAndBindsLocally(t *testing.T) {
	d := nut87(t, "/dev/hidraw3")
	m := newModel(t, &hidfake.Enumerator{Devices: []*hidfake.Device{d}})

	drive(t, m, key("2"), key("enter"), key("right")) // picker → the consumer key kind
	before := len(d.Sent())
	drive(t, m, typeText("calc")...)

	wantFrame(t, m, `
nutctl — NUT87 at /dev/hidraw3 (firmware 1.20)
1 Device  [2 Keys]  3 Lighting  4 Settings

Bind Key Slot 0 (Esc) on the Base Layer — now: keyboard key "Esc"
  ←/→ kind · ↑/↓ move · type to filter · enter bind · esc cancel

  keyboard key 107  [consumer key 17]  mouse button 7  Function 287
> Calculator
  filter "calc" — 1 of 17 entries

status: ready
help: enter bind · esc cancel · ctrl+c quit`)

	drive(t, m, key("enter"))
	if got := len(d.Sent()); got != before {
		t.Errorf("binding locally must not touch the wire: %d → %d reports", before, got)
	}
	wantStatus(t, m, `bound Key Slot 0 (Esc) on the Base Layer to consumer key "Calculator" — a applies it to the Device`)
	got := frame(m)
	for _, want := range []string{
		`>   0 Esc                    consumer key`,
		"pending: 1 change — base Key Slot 0 (Esc): keyboard key \"Esc\" → consumer key \"Calculator\"",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("frame missing %q:\n%s", want, got)
		}
	}
}

// A picker with nothing to offer says so — and a cancelled picker changes
// nothing (the status bar names the cancellation, the table is untouched).
func TestKeysPickerCancelAndEmptyFilter(t *testing.T) {
	d := nut87(t, "/dev/hidraw3")
	m := newModel(t, &hidfake.Enumerator{Devices: []*hidfake.Device{d}})

	drive(t, m, key("2"), key("enter"))
	drive(t, m, typeText("no such key")...)
	got := frame(m)
	if !strings.Contains(got, "(no entry matches the filter)") {
		t.Errorf("an empty filter result must say so:\n%s", got)
	}

	drive(t, m, key("esc"))
	wantStatus(t, m, "rebind cancelled — nothing changed")
	got = frame(m)
	for _, want := range []string{">   0 Esc                    keyboard key \"Esc\"", "Keys — Base Layer"} {
		if !strings.Contains(got, want) {
			t.Errorf("after esc the table is untouched, missing %q:\n%s", want, got)
		}
	}
}

// A Fn-disabled Key Slot of the Model's layout table cannot be rebound on
// the Fn Layer (spec user story 15): the table marks it, and the picker
// never opens for it — the refusal names the row and the reason.
func TestKeysFnDisabledSlotCannotBeRebound(t *testing.T) {
	d := nut87(t, "/dev/hidraw3")
	m := newModel(t, &hidfake.Enumerator{Devices: []*hidfake.Device{d}})

	drive(t, m, key("2"), key(" "))
	drive(t, m, key("down")) // row 1 = F1, Fn-disabled in the layout table

	before := len(d.Sent())
	drive(t, m, key("enter"))

	wantStatus(t, m, "refusing to rebind Key Slot 1 (F1) on the Fn Layer: Fn-disabled in the Model's layout table")
	got := frame(m)
	if strings.Contains(got, "Bind Key Slot") {
		t.Errorf("the picker must not open for a Fn-disabled Key Slot:\n%s", got)
	}
	if got := len(d.Sent()); got != before {
		t.Errorf("a refused rebind touched the wire: %d → %d reports", before, got)
	}

	// On the Base Layer the same Key Slot rebinds — the restriction is the
	// Fn Layer's, exactly as the layout table's fnDisabledSlots say.
	drive(t, m, key(" "), key("enter"))
	if got := frame(m); !strings.Contains(got, "Bind Key Slot 1 (F1) on the Base Layer") {
		t.Errorf("F1 must rebind on the Base Layer:\n%s", got)
	}
}

// --- remaps through the pending/apply/revert mechanics ---

// A remap applies through the shared write path (edit.go + write.go): the
// write gate before the session's first write, then the edited Layer's
// block alone — a Base Layer remap sends SET_KEY and never the untouched
// blocks — verified by a read-back that reports the remap.
func TestKeysRemapAppliesTheEditedBlockOnly(t *testing.T) {
	t.Chdir(t.TempDir())
	d := nut87(t, "/dev/hidraw3")
	m := newModel(t, &hidfake.Enumerator{Devices: []*hidfake.Device{d}})

	drive(t, m, key("2"))
	drive(t, m, key("enter"))
	drive(t, m, typeText("L-Ctrl")...)
	drive(t, m, key("enter")) // Esc → L-Ctrl, locally

	replayReadPath(d, t) // the write gate's fresh checked read
	setBlockScript(d, t, "set_key")
	replayReadPath(d, t, slotFault(t, "get_key", 0, [4]byte{0x02, 0x00, 0xe0, 0x00}))

	drive(t, m, key("a"), key("y"))

	if !sameBytes(setBlockPayload(t, d, cmdSetKey)[0:4], [4]byte{0x02, 0x00, 0xe0, 0x00}) {
		t.Errorf("SET_KEY Key Slot 0 = % x, want 02 00 e0 00 (the vendor's KEYBOARD encoding)", setBlockPayload(t, d, cmdSetKey)[0:4])
	}
	if !sameBytes(setBlockPayload(t, d, cmdSetKey)[4:8], [4]byte{0x02, 0x00, 0x3a, 0x00}) {
		t.Errorf("SET_KEY Key Slot 1 = % x, want the Device's untouched 02 00 3a 00", setBlockPayload(t, d, cmdSetKey)[4:8])
	}
	for _, cmd := range []byte{cmdSetFnKey, cmdSetLEDEffect, cmdSetCustomLEDData, cmdSetGameMode} {
		if sawCommand(d, cmd) {
			t.Errorf("a Base Layer remap must not send command %d", cmd)
		}
	}
	wantStatus(t, m, "applied your pending edits to the NUT87 at /dev/hidraw3 (firmware 1.20) — read-back verified: the Device matches your edits")
	got := frame(m)
	for _, want := range []string{`>   0 Esc                    keyboard key "L-Ctrl"`, "status:"} {
		if !strings.Contains(got, want) {
			t.Errorf("frame missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "pending:") {
		t.Errorf("a verified apply clears the pending changes:\n%s", got)
	}
	if _, err := os.Stat(goldenName); err != nil {
		t.Errorf("y must save the golden read to ./%s: %v", goldenName, err)
	}
}

// A Fn Layer remap writes the Fn Layer's block (SET_FN_KEY) and nothing
// else — the two Layers are independent Key Slot tables (CONTEXT.md).
func TestKeysRemapFnLayerWritesTheFnBlock(t *testing.T) {
	t.Chdir(t.TempDir())
	d := nut87(t, "/dev/hidraw3")
	m := newModel(t, &hidfake.Enumerator{Devices: []*hidfake.Device{d}})

	drive(t, m, key("2"), key(" ")) // the Fn Layer
	drive(t, m, key("enter"))
	drive(t, m, key("right"), key("right")) // → the mouse button kind
	drive(t, m, typeText("left")...)
	drive(t, m, key("enter")) // Fn + Esc → mouse left button

	replayReadPath(d, t) // the write gate's fresh checked read
	setBlockScript(d, t, "set_fn_key")
	replayReadPath(d, t, slotFault(t, "get_fn_key", 0, [4]byte{0x01, 0x01, 0x01, 0x00}))

	drive(t, m, key("a"), key("y"))

	if !sameBytes(setBlockPayload(t, d, cmdSetFnKey)[0:4], [4]byte{0x01, 0x01, 0x01, 0x00}) {
		t.Errorf("SET_FN_KEY Key Slot 0 = % x, want 01 01 01 00 (the vendor's MOUSE encoding)", setBlockPayload(t, d, cmdSetFnKey)[0:4])
	}
	if sawCommand(d, cmdSetKey) {
		t.Error("a Fn Layer remap must not write the Base Layer block")
	}
	wantStatus(t, m, "applied your pending edits to the NUT87 at /dev/hidraw3 (firmware 1.20) — read-back verified: the Device matches your edits")
}

// Unapplied remaps are pending and revertible (spec user story 21): the
// status bar's pending line names them in the picker's words, and `r`
// restores the Device's actual state without touching the wire.
func TestKeysRemapPendingAndRevert(t *testing.T) {
	d := nut87(t, "/dev/hidraw3")
	m := newModel(t, &hidfake.Enumerator{Devices: []*hidfake.Device{d}})

	drive(t, m, key("2"), key("enter"))
	drive(t, m, typeText("L-Ctrl")...)
	drive(t, m, key("enter"))

	before := len(d.Sent())
	drive(t, m, key("r"))

	wantStatus(t, m, "reverted 1 change from the Device's actual state")
	if got := len(d.Sent()); got != before {
		t.Errorf("revert must not touch the wire: %d → %d reports", before, got)
	}
	got := frame(m)
	if strings.Contains(got, "pending:") || !strings.Contains(got, `keyboard key "Esc"`) {
		t.Errorf("after revert the table shows the Device's state:\n%s", got)
	}
}

// A remap survives a State File round-trip (ticket 06 acceptance): applied
// to one Device, saved, then loaded onto another — the same Key Action
// reaches the wire again, byte for byte.
func TestKeysRemapSurvivesStateFileRoundTrip(t *testing.T) {
	t.Chdir(t.TempDir())

	// Session one: Esc → L-Ctrl on the Base Layer, applied and saved.
	d1 := nut87(t, "/dev/hidraw1")
	m1 := newModel(t, &hidfake.Enumerator{Devices: []*hidfake.Device{d1}})
	drive(t, m1, key("2"), key("enter"))
	drive(t, m1, typeText("L-Ctrl")...)
	drive(t, m1, key("enter"))
	replayReadPath(d1, t)
	setBlockScript(d1, t, "set_key")
	replayReadPath(d1, t, slotFault(t, "get_key", 0, [4]byte{0x02, 0x00, 0xe0, 0x00}))
	drive(t, m1, key("a"), key("y"))
	saveStateFile(t, m1, "state.json")

	sf, err := device.LoadStateFile("state.json")
	if err != nil {
		t.Fatalf("saved State File does not load: %v", err)
	}
	if got := sf.State.Base[0].Raw; got != [4]byte{0x02, 0x00, 0xe0, 0x00} {
		t.Errorf("the State File carries Key Slot 0 = % x, want the remap 02 00 e0 00", got)
	}
	// Session two: the same State File onto another Device — the remap
	// lands again (a load writes every block and verifies the read-back).
	d2 := nut87(t, "/dev/hidraw2")
	m2 := newModel(t, &hidfake.Enumerator{Devices: []*hidfake.Device{d2}})
	replayReadPath(d2, t) // the write gate's fresh checked read
	for _, name := range []string{"set_key", "set_fn_key", "set_led_effect", "set_custom_led_data", "set_game_mode"} {
		setBlockScript(d2, t, name)
	}
	replayReadPath(d2, t, slotFault(t, "get_key", 0, [4]byte{0x02, 0x00, 0xe0, 0x00})) // the read-back

	drive(t, m2, key("l"))
	drive(t, m2, typeText("state.json")...)
	drive(t, m2, key("enter"), key("y"))

	if !sameBytes(setBlockPayload(t, d2, cmdSetKey)[0:4], [4]byte{0x02, 0x00, 0xe0, 0x00}) {
		t.Errorf("after the round-trip SET_KEY Key Slot 0 = % x, want the remap 02 00 e0 00", setBlockPayload(t, d2, cmdSetKey)[0:4])
	}
	wantStatus(t, m2, "loaded state.json onto the NUT87 at /dev/hidraw2 (firmware 1.20) — read-back verified: the Device matches the State File")
}
