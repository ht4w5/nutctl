package tui

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/ht4w5/nutctl/internal/hidfake"
)

// The Lighting screen (ticket 07): both halves of lighting configurable —
// the ambient Lighting Effect (mode, colors, brightness, speed, direction)
// and the Per-Key RGB slot grid — through the shared pending/apply/revert
// mechanics (edit.go). Tests run through the two agreed seams: key messages
// in / frames out, and every byte that reaches the fake Device. The value
// domains asserted here are the Model's advertised ones (internal/device):
// the effect modes its table offers, brightness and speed inside 1..6.

// effectFault patches bytes of the Lighting Effect payload into the recorded
// GET_LED_EFFECT response — the way a test scripts a read-back that reports
// the edit, so a verified apply can verify. off is the block offset
// (docs/protocol.md §4: mode at 0, brightness at 9) behind the 8-byte
// response header.
func effectFault(off int, data []byte) fault {
	return fault{cmd: "GET_LED_EFFECT", res: 0, off: 8 + off, data: data}
}

// setLEDEffectPayload is the Lighting Effect block of the SET_LED_EFFECT
// transfer the fake received — the 16 bytes the wire encoding acceptance is
// asserted on (docs/protocol.md §4).
func setLEDEffectPayload(t *testing.T, d *hidfake.Device) []byte {
	t.Helper()
	return setBlockPayload(t, d, cmdSetLEDEffect)[:16]
}

// --- the Lighting Effect form ---

// The Lighting Effect form shows the six editable rows with what the Device
// reports (golden frame, spec user story 36): mode named from the Model's
// table, colors as swatches and hex, brightness and speed with the Model's
// advertised range, direction as its arrow.
func TestLightingEffectFormFrame(t *testing.T) {
	d := nut87(t, "/dev/hidraw3")
	m := newModel(t, &hidfake.Enumerator{Devices: []*hidfake.Device{d}})

	drive(t, m, key("3"))
	wantFrame(t, m, `
nutctl — NUT87 at /dev/hidraw3 (firmware 1.20)
1 Device  2 Keys  [3 Lighting]  4 Settings

Lighting — Lighting Effect
  ↑/↓ select · ←/→ change · enter edit color · space Per-Key RGB · * = differs from the Device — a applies, r reverts

> Mode            11 Flowing with the Waves
  Primary color   ██ #ffffff
  Secondary color ██ #000000
  Brightness      6 (range 1-6)
  Speed           3 (range 1-6)
  Direction       right

status: ready
help: 1-4/tab switch screen · s save · l load · a apply · r revert · q quit`)
}

// The mode row cycles the Model's offered modes (the acceptance's "mode"):
// the vendor's effect table for the NUT87, in its display order, with `*`
// on the edit.
func TestLightingModeCyclesTheModelOffering(t *testing.T) {
	d := nut87(t, "/dev/hidraw3")
	m := newModel(t, &hidfake.Enumerator{Devices: []*hidfake.Device{d}})

	drive(t, m, key("3"))
	before := len(d.Sent())
	drive(t, m, key("right")) // 11 → 12
	if got := frame(m); !strings.Contains(got, "> Mode            12 Turning Peaks *") {
		t.Errorf("frame missing the edited Mode row:\n%s", got)
	}
	drive(t, m, key("left"), key("left")) // 12 → 11 → 10
	if got := frame(m); !strings.Contains(got, "> Mode            10 Colorful Interchange *") {
		t.Errorf("frame missing the edited Mode row:\n%s", got)
	}
	if got := len(d.Sent()); got != before {
		t.Errorf("editing locally must not touch the wire: %d → %d reports", before, got)
	}
}

// A mode read that no table names is shown as read — and stepping enters the
// Model's offering at an end (the Report Rate editor's precedent).
func TestLightingModeUnknownStepsIntoTheOffering(t *testing.T) {
	d := nut87(t, "/dev/hidraw3", effectFault(0, []byte{20}))
	m := newModel(t, &hidfake.Enumerator{Devices: []*hidfake.Device{d}})

	drive(t, m, key("3"))
	if got := frame(m); !strings.Contains(got, "> Mode            20 (unknown mode)") {
		t.Errorf("an unnamed mode must be shown as read:\n%s", got)
	}
	drive(t, m, key("right")) // steps in at the offering's start
	if got := frame(m); !strings.Contains(got, "> Mode            1 Static Bright *") {
		t.Errorf("stepping must enter the Model's offering:\n%s", got)
	}
}

// Brightness and speed are constrained to the Model's advertised ranges
// (ticket 07 acceptance): the editors clamp at 1 and 6 and never step
// outside — and the range stays on screen.
func TestLightingBrightnessAndSpeedClampToModelRanges(t *testing.T) {
	d := nut87(t, "/dev/hidraw3")
	m := newModel(t, &hidfake.Enumerator{Devices: []*hidfake.Device{d}})

	drive(t, m, key("3"))
	drive(t, m, key("down"), key("down"), key("down")) // the Brightness row
	for i := 0; i < 8; i++ {
		drive(t, m, key("left"))
	}
	if got := frame(m); !strings.Contains(got, "> Brightness      1 (range 1-6) *") {
		t.Errorf("brightness must clamp at the Model's minimum:\n%s", got)
	}
	for i := 0; i < 10; i++ {
		drive(t, m, key("right"))
	}
	got := frame(m)
	if !strings.Contains(got, "> Brightness      6 (range 1-6)") {
		t.Errorf("brightness must clamp at the Model's maximum:\n%s", got)
	}
	if strings.Contains(got, "> Brightness      6 (range 1-6) *") {
		t.Errorf("clamping back to the Device's value is not an edit:\n%s", got)
	}

	drive(t, m, key("down")) // the Speed row (3 on the Device)
	for i := 0; i < 8; i++ {
		drive(t, m, key("right"))
	}
	if got := frame(m); !strings.Contains(got, "> Speed           6 (range 1-6) *") {
		t.Errorf("speed must clamp at the Model's maximum:\n%s", got)
	}
}

// The direction row offers exactly the pair the effect mode animates — the
// two arrows the vendor's own direction buttons write for it — and a mode
// without a direction edits nothing (its table says the byte is not that
// mode's business).
func TestLightingDirectionFollowsTheEffectMode(t *testing.T) {
	d := nut87(t, "/dev/hidraw3")
	m := newModel(t, &hidfake.Enumerator{Devices: []*hidfake.Device{d}})

	drive(t, m, key("3"))
	drive(t, m, key("down"), key("down"), key("down"), key("down"), key("down")) // the Direction row

	// Mode 11 (Flowing with the Waves) animates left/right (1/0).
	drive(t, m, key("left"))
	if got := frame(m); !strings.Contains(got, "> Direction       left *") {
		t.Errorf("direction must offer the mode's left/right pair:\n%s", got)
	}
	drive(t, m, key("right"))
	if got := frame(m); !strings.Contains(got, "> Direction       right") ||
		strings.Contains(got, "> Direction       right *") {
		t.Errorf("right is the Device's value again:\n%s", got)
	}

	// Mode 10 (Colorful Interchange) animates up/down (2/3) — the pair
	// changes with the mode.
	drive(t, m, key("up"), key("up"), key("up"), key("up"), key("up"), key("left")) // mode 11 → 10
	drive(t, m, key("down"), key("down"), key("down"), key("down"), key("down"))
	drive(t, m, key("right"))
	if got := frame(m); !strings.Contains(got, "> Direction       down *") {
		t.Errorf("direction must follow the mode's up/down pair:\n%s", got)
	}
	drive(t, m, key("left"))
	if got := frame(m); !strings.Contains(got, "> Direction       up *") {
		t.Errorf("direction must follow the mode's up/down pair:\n%s", got)
	}

	// Mode 1 (Static Bright) has no direction — and no speed — in the
	// Model's table. The rows say so and edit nothing.
	drive(t, m, key("up"), key("up"), key("up"), key("up"), key("up"))
	for i := 0; i < 9; i++ {
		drive(t, m, key("left"))
	}
	got := frame(m)
	for _, want := range []string{
		"> Mode            1 Static Bright *",
		"Speed           3 (range 1-6) — not used by this mode",
		"Direction       up * — not used by this mode",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("frame missing %q:\n%s", want, got)
		}
	}
	before := len(d.Sent())
	drive(t, m, key("down"), key("down"), key("down"), key("down"), key("down"))
	drive(t, m, key("right"), key("left"))
	if got := frame(m); !strings.Contains(got, "Direction       up * — not used by this mode") {
		t.Errorf("a direction the mode ignores must not be edited:\n%s", got)
	}
	if got := len(d.Sent()); got != before {
		t.Errorf("editing locally must not touch the wire: %d → %d reports", before, got)
	}
}

// --- the color editor ---

// The color editor sets a color by channel (type its value, or step it):
// golden frame, then the commit lands in the local edit buffer only — the
// wire hears about it only through `a` (spec user story 19).
func TestLightingColorEditorEditsLocally(t *testing.T) {
	d := nut87(t, "/dev/hidraw3")
	m := newModel(t, &hidfake.Enumerator{Devices: []*hidfake.Device{d}})

	drive(t, m, key("3"), key("down"), key("enter"))       // the Primary color row
	drive(t, m, key("2"), key("5"), key("5"))              // red 255
	drive(t, m, key("down"), key("1"), key("3"), key("6")) // green 136
	drive(t, m, key("down"), key("0"))                     // blue 0

	wantFrame(t, m, `
nutctl — NUT87 at /dev/hidraw3 (firmware 1.20)
1 Device  2 Keys  [3 Lighting]  4 Settings

Edit color — Primary color
  ↑/↓ channel · ←/→ ±1 · pgup/pgdn ±16 · digits type · enter set · esc cancel

  was ██ #ffffff · now ██ #ff8800
  red     255
  green   136
> blue      0

status: ready
help: enter set · esc cancel · ctrl+c quit`)

	before := len(d.Sent())
	drive(t, m, key("enter"))
	wantStatus(t, m, "set Primary color to #ff8800 — a applies it to the Device")
	got := frame(m)
	for _, want := range []string{
		"> Primary color   ██ #ff8800 *",
		"pending: 1 change — lighting primary color: #ffffff → #ff8800",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("frame missing %q:\n%s", want, got)
		}
	}
	if got := len(d.Sent()); got != before {
		t.Errorf("a committed color must not touch the wire: %d → %d reports", before, got)
	}
}

// A cancelled color edit changes nothing (esc is always the no-escape
// hatch's opposite: nothing happens without enter).
func TestLightingColorEditorCancelChangesNothing(t *testing.T) {
	d := nut87(t, "/dev/hidraw3")
	m := newModel(t, &hidfake.Enumerator{Devices: []*hidfake.Device{d}})

	drive(t, m, key("3"), key("down"), key("enter"))
	drive(t, m, key("2"), key("5"), key("5"))
	drive(t, m, key("esc"))

	wantStatus(t, m, "color edit cancelled — nothing changed")
	if got := frame(m); strings.Contains(got, "pending:") {
		t.Errorf("a cancelled edit leaves nothing pending:\n%s", got)
	}
}

// --- the Per-Key RGB slot grid ---

// The Per-Key RGB half is the slot grid of per-slot colors (golden frame):
// one cell per entry (a Key Slot), swatch and hex, the cursor bracketed,
// and the selected entry named from the Model's layout table.
func TestLightingPerKeyGridFrame(t *testing.T) {
	d := nut87(t, "/dev/hidraw3")
	m := newModel(t, &hidfake.Enumerator{Devices: []*hidfake.Device{d}})

	drive(t, m, key("3"), key(" ")) // the Per-Key RGB half
	wantFrame(t, m, `
nutctl — NUT87 at /dev/hidraw3 (firmware 1.20)
1 Device  2 Keys  [3 Lighting]  4 Settings

Lighting — Per-Key RGB
  ↑/↓/←/→ select · enter edit color · pgup/pgdn page · space Lighting Effect · * = differs from the Device — a applies, r reverts

[  0 ██ #000000 ]     1 ██ #000000       2 ██ #000000       3 ██ #000000
   4 ██ #000000       5 ██ #000000       6 ██ #000000       7 ██ #000000
   8 ██ #000000       9 ██ #000000      10 ██ #000000      11 ██ #000000
  12 ██ #000000      13 ██ #000000      14 ██ #000000      15 ██ #000000
  16 ██ #000000      17 ██ #000000      18 ██ #000000      19 ██ #000000
  20 ██ #000000      21 ██ #000000      22 ██ #000000      23 ██ #000000
  24 ██ #000000      25 ██ #000000      26 ██ #000000      27 ██ #000000
  28 ██ #000000      29 ██ #000000      30 ██ #000000      31 ██ #000000

  entry 0 (Esc) — ██ #000000 · visible in effect mode 128 Custom

status: ready
help: 1-4/tab switch screen · s save · l load · a apply · r revert · q quit`)
}

// The grid moves as one cursor over the slots: arrows step, pgup/pgdn page
// the window (the Keys table's scroll behaviour).
func TestLightingPerKeyGridNavigates(t *testing.T) {
	d := nut87(t, "/dev/hidraw3")
	m := newModel(t, &hidfake.Enumerator{Devices: []*hidfake.Device{d}})

	drive(t, m, key("3"), key(" "))
	drive(t, m, key("down"), key("right")) // entry 5
	got := frame(m)
	for _, want := range []string{
		"[  5 ██ #000000 ]",
		"entry 5 (F5) — ██ #000000",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("frame missing %q:\n%s", want, got)
		}
	}

	// pgdown pages the window (32 entries) from wherever the cursor is.
	drive(t, m, key("pgdown"))
	if got := frame(m); !strings.Contains(got, "[ 37 ██ #000000 ]") {
		t.Errorf("pgdown must page the window to entry 37:\n%s", got)
	}
}

// A per-slot color is edited on the grid and pending like every other edit
// (spec user story 17: highlight specific keys), named as the pending line
// and the read-back diff name it.
func TestLightingPerKeyEditLocally(t *testing.T) {
	d := nut87(t, "/dev/hidraw3")
	m := newModel(t, &hidfake.Enumerator{Devices: []*hidfake.Device{d}})

	drive(t, m, key("3"), key(" "))
	drive(t, m, key("down"), key("right")) // entry 5 (F5)
	before := len(d.Sent())
	drive(t, m, key("enter"))
	if got := frame(m); !strings.Contains(got, "Edit color — Per-Key RGB entry 5 (F5)") {
		t.Errorf("the editor must name its entry:\n%s", got)
	}
	drive(t, m, key("2"), key("5"), key("5")) // red 255
	drive(t, m, key("down"), key("0"))        // green 0
	drive(t, m, key("down"), key("0"))        // blue 0
	drive(t, m, key("enter"))

	wantStatus(t, m, "set Per-Key RGB entry 5 (F5) to #ff0000 — a applies it to the Device")
	got := frame(m)
	for _, want := range []string{
		"  5 ██ #ff0000*",
		"entry 5 (F5) — ██ #ff0000",
		"pending: 1 change — per-key RGB entry 5 (F5): #000000 → #ff0000",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("frame missing %q:\n%s", want, got)
		}
	}
	if got := len(d.Sent()); got != before {
		t.Errorf("editing a color must not touch the wire: %d → %d reports", before, got)
	}
}

// --- apply (`a`): only the edited block, the SET wire encoding, read-back ---

// An apply of Lighting Effect edits writes ONLY the Lighting Effect block,
// in the SET wire encoding (docs/protocol.md §4: driverSetting forced 0xFF,
// check code forced 0xAA 0x55), and every untouched field keeps the
// Device's value. The read-back verifies; the pending changes clear.
func TestLightingApplyWritesOnlyTheLightingBlock(t *testing.T) {
	t.Chdir(t.TempDir())
	d := nut87(t, "/dev/hidraw3")
	m := newModel(t, &hidfake.Enumerator{Devices: []*hidfake.Device{d}})

	drive(t, m, key("3"))
	drive(t, m, key("down"), key("down"), key("down")) // the Brightness row
	drive(t, m, key("left"), key("left"))              // 6 → 4

	replayReadPath(d, t) // the write gate's fresh checked read
	setBlockScript(d, t, "set_led_effect")
	replayReadPath(d, t, effectFault(9, []byte{4})) // the read-back reports brightness 4

	drive(t, m, key("a"), key("y"))

	want := []byte{
		0x0b, 0xff, 0xff, 0xff, // mode 11, primary #ffffff
		0xff,             // driverSetting: forced 0xFF on write
		0x00, 0x00, 0x00, // secondary #000000
		0x01, 0x04, 0x03, 0x00, 0x00, // colorMode, brightness 4, speed, direction, effectModeType
		0x00,       // reserved
		0xaa, 0x55, // check code: forced 0xAA 0x55 on write
	}
	got := setLEDEffectPayload(t, d)
	if !bytes.Equal(got, want) {
		t.Errorf("SET_LED_EFFECT block = % x, want % x", got, want)
	}
	for _, cmd := range []byte{cmdSetKey, cmdSetFnKey, cmdSetCustomLEDData, cmdSetGameMode} {
		if sawCommand(d, cmd) {
			t.Errorf("an apply of Lighting Effect edits must not send command %d", cmd)
		}
	}
	wantStatus(t, m, "applied your pending edits to the NUT87 at /dev/hidraw3 (firmware 1.20) — read-back verified: the Device matches your edits")
	if got := frame(m); strings.Contains(got, "pending:") {
		t.Errorf("a verified apply clears the pending changes:\n%s", got)
	}
}

// An apply of Per-Key RGB edits writes ONLY the Per-Key RGB block: 128
// entries, the SET wire format's ledId = the entry index (docs/protocol.md
// §4), the edited color landed and every untouched entry kept the Device's
// colors — the block-tail marker included.
func TestLightingPerKeyApplyWritesOnlyThePerKeyBlock(t *testing.T) {
	t.Chdir(t.TempDir())
	d := nut87(t, "/dev/hidraw3")
	m := newModel(t, &hidfake.Enumerator{Devices: []*hidfake.Device{d}})

	drive(t, m, key("3"), key(" "))
	drive(t, m, key("down"), key("right")) // entry 5
	drive(t, m, key("enter"))
	drive(t, m, key("2"), key("5"), key("5")) // red 255
	drive(t, m, key("down"), key("0"))        // green 0
	drive(t, m, key("down"), key("0"))        // blue 0
	drive(t, m, key("enter"))

	replayReadPath(d, t) // the write gate's fresh checked read
	setBlockScript(d, t, "set_custom_led_data")
	replayReadPath(d, t, slotFault(t, "get_custom_led_data", 5, [4]byte{5, 0xff, 0x00, 0x00}))

	drive(t, m, key("a"), key("y"))

	got := setBlockPayload(t, d, cmdSetCustomLEDData)
	for _, tc := range []struct {
		at  int
		one []byte
	}{
		{4 * 5, []byte{5, 0xff, 0x00, 0x00}},     // the edit
		{4 * 6, []byte{6, 0x00, 0x00, 0x00}},     // an untouched entry
		{4 * 127, []byte{127, 0x00, 0xaa, 0x55}}, // the Device's tail marker, round-tripped
	} {
		if !sameBytes(got[tc.at:tc.at+4], [4]byte(tc.one)) {
			t.Errorf("SET_CUSTOM_LED_DATA entry %d = % x, want % x", tc.at/4, got[tc.at:tc.at+4], tc.one)
		}
	}
	for _, cmd := range []byte{cmdSetKey, cmdSetFnKey, cmdSetLEDEffect, cmdSetGameMode} {
		if sawCommand(d, cmd) {
			t.Errorf("an apply of Per-Key RGB edits must not send command %d", cmd)
		}
	}
	wantStatus(t, m, "applied your pending edits to the NUT87 at /dev/hidraw3 (firmware 1.20) — read-back verified: the Device matches your edits")
}

// --- pending and revert ---

// Lighting edits are session edits like any other: pending and visible from
// every screen, revertible with `r` and no wire traffic (spec user stories
// 20, 21).
func TestLightingEditsPendingAndRevert(t *testing.T) {
	d := nut87(t, "/dev/hidraw3")
	m := newModel(t, &hidfake.Enumerator{Devices: []*hidfake.Device{d}})

	drive(t, m, key("3"), key("right")) // mode 11 → 12

	drive(t, m, key("1"))
	if got := frame(m); !strings.Contains(got, "pending: 1 change — lighting mode: 11 Flowing with the Waves → 12 Turning Peaks") {
		t.Errorf("the Device screen must show the pending change:\n%s", got)
	}

	drive(t, m, key("3"))
	before := len(d.Sent())
	drive(t, m, key("r"))
	wantStatus(t, m, "reverted 1 change from the Device's actual state")
	if got := len(d.Sent()); got != before {
		t.Errorf("revert must not touch the wire: %d → %d reports", before, got)
	}
	got := frame(m)
	if strings.Contains(got, "pending:") || !strings.Contains(got, "> Mode            11 Flowing with the Waves") {
		t.Errorf("after revert the form shows the Device's state:\n%s", got)
	}
	if _, err := os.Stat(goldenName); err == nil {
		t.Error("a revert must write no file")
	}
}
