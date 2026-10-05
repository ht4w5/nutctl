package device

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/ht4w5/nutctl/internal/fixture"
	"github.com/ht4w5/nutctl/internal/protocol"
)

// The verification step (ADR-0003) runs all three protocol self-checks as
// ONE reusable step: it passes only when everything passes, and a failure
// names every failure, one "self-check failed: " line each. The layout and
// check-code expectations are calibrated to hardware evidence (2026-10-05,
// real NUT87, firmware 1.20): the firmware-matrix extraSlots carry default
// Key Actions on BOTH Layers of a healthy Device, the keymap blocks end
// 00 00 AA 55, and the Lighting Effect check code reads 00 00 until the
// vendor app's SET path writes it.

// nut87Identity is the Device Identity a NUT87 shows on both sides of the
// check (USB enumeration and GET_DEVICE_INFO agree on it).
func nut87Identity() Identity {
	return Identity{
		VendorID: 0x0C45, ProductID: 0x880C, ProductName: "NUT87",
		Manufacturer: 0x0C45, Product: 0x880C,
	}
}

// tailMarker is the Key Slot 127 entry of a well-aligned keymap block: block
// bytes 508..511 read 00 00 AA 55, so the 0xAA 0x55 block-tail marker lands
// in the last Key Slot's raw bytes.
func tailMarker() protocol.KeyAction {
	return protocol.KeyAction{
		Type:   protocol.ActionDefault,
		Params: [3]byte{0, 0xAA, 0x55},
		Raw:    [4]byte{0, 0, 0xAA, 0x55},
	}
}

// passingInput is a fully read NUT87 that passes all three checks. Its
// keymaps are deliberately remapped-but-known: bindings in Key Slots the
// layout knows (Esc, the three Knob gestures, the last key), the block-tail
// markers in place, DEFAULT everywhere else.
func passingInput() CheckInput {
	in := CheckInput{
		USB:      nut87Identity(),
		Reported: nut87Identity(),
		Lighting: protocol.LightingEffect{CheckCode: [2]byte{0xAA, 0x55}, CheckCodeOK: true},
	}
	esc := protocol.KeyAction{Type: protocol.ActionKeyboard, Params: [3]byte{41, 0, 0}}
	in.Base[0] = esc
	in.Base[13] = protocol.KeyAction{Type: protocol.ActionConsumer} // Knob clockwise
	in.Base[14] = protocol.KeyAction{Type: protocol.ActionConsumer} // Knob counter-clockwise
	in.Base[15] = protocol.KeyAction{Type: protocol.ActionSystem}   // Knob press
	in.Base[108] = protocol.KeyAction{Type: protocol.ActionMouse}
	in.Fn[0] = protocol.KeyAction{Type: protocol.ActionMacro} // Fn layer remap
	in.Base[127] = tailMarker()
	in.Fn[127] = tailMarker()
	return in
}

// factoryInput is a never-remapped keymap: every Key Slot DEFAULT except the
// block tails (the tail marker decodes as DEFAULT with its raw bytes). The
// Lighting Effect check code is the factory/unwritten state 0x00 0x00 — the
// state observed on firmware 1.20.
func factoryInput() CheckInput {
	in := CheckInput{
		USB:      nut87Identity(),
		Reported: nut87Identity(),
		Lighting: protocol.LightingEffect{CheckCode: [2]byte{0, 0}, CheckCodeOK: true},
	}
	in.Base[127] = tailMarker()
	in.Fn[127] = tailMarker()
	return in
}

// keyboardAction is a KEYBOARD Key Action with the given HID keycode.
func keyboardAction(keycode byte) protocol.KeyAction {
	return protocol.KeyAction{
		Type:   protocol.ActionKeyboard,
		Params: [3]byte{0, keycode, 0},
		Raw:    [4]byte{2, 0, keycode, 0},
	}
}

func TestRunChecks(t *testing.T) {
	// Failure lines are exact strings: the CLI prints them verbatim on
	// stderr, so "fail loudly" is external behavior.
	const (
		verifyLine = `self-check failed: USB reports 0c45:880c "NUT87" but the firmware reports 0c45:9999 — refusing to trust this session`
		layoutLine = `self-check failed: keymap does not decode to the NUT87 layout: Key Actions sit in %s, but no physical key, Knob gesture, or firmware-matrix slot sits there — the read is misaligned or the block is corrupt`
		tailLine   = `self-check failed: keymap block misalignment: %s, want 0xAA 0x55 (the block-tail marker of a well-aligned read) — the read is misaligned or the block is corrupt`
		lightLine  = `self-check failed: lighting check code is %s at offsets 14..15, want 0xAA 0x55 (written by the vendor app's SET path) or 0x00 0x00 (factory/unwritten) — the read is misaligned or the block is corrupt`
		nut75Line  = `self-check failed: wrong Model: this is a NUT75 (USB 0c45:880c "NUT75"); this build only supports NUT87 — refusing to configure`
		nut75Table = `self-check failed: no layout table for Model "NUT75": missing data file internal/device/layouts/nut75.json — a Model's layout table is data; add the file to add the Model`
	)

	tests := []struct {
		name  string
		model Model
		in    func() CheckInput
		want  []string // exact failure lines; nil means the step passes
	}{
		{
			name:  "all checks pass (remapped but in-layout keymap, written check code)",
			model: NUT87,
			in:    passingInput,
			want:  nil,
		},
		{
			name:  "all checks pass (all-DEFAULT keymap, factory/unwritten check code)",
			model: NUT87,
			in:    factoryInput,
			want:  nil,
		},
		{
			// The calibrated healthy Device: the 22 firmware-matrix Key
			// Slots (the layout's "gaps" plus 109..111) carry default Key
			// Actions on BOTH Layers — shared firmware matrix with sibling
			// Models, normal state, not corruption. Values from the
			// 2026-10-05 recording (firmware 1.20).
			name:  "all checks pass (firmware-matrix defaults on both Layers)",
			model: NUT87,
			in: func() CheckInput {
				in := factoryInput()
				for _, raw := range []struct {
					slot int
					key  byte
				}{
					{29, 0x53}, {30, 0x54}, {31, 0x55},
					{101, 0xE7}, {109, 0x56}, {110, 0x57}, {111, 0x00},
				} {
					in.Base[raw.slot] = keyboardAction(raw.key)
					in.Fn[raw.slot] = keyboardAction(raw.key)
				}
				in.Base[96] = protocol.KeyAction{
					Type: protocol.ActionConsumer, Params: [3]byte{0x92, 0x01, 0}, Raw: [4]byte{3, 0x92, 0x01, 0},
				}
				in.Fn[96] = in.Base[96]
				return in
			},
			want: nil,
		},
		{
			name:  "check 1 alone: firmware reports another product id",
			model: NUT87,
			in: func() CheckInput {
				in := passingInput()
				in.Reported.ProductID = 0x9999
				in.Reported.Product = 0x9999
				return in
			},
			want: []string{verifyLine},
		},
		{
			name:  "check 1 alone: sibling Model NUT75 under a NUT87 session",
			model: NUT87,
			in: func() CheckInput {
				in := passingInput()
				in.USB.ProductName = "NUT75"
				in.Reported.ProductName = "NUT75"
				return in
			},
			want: []string{nut75Line},
		},
		{
			name:  "check 1 alone: unknown device",
			model: NUT87,
			in: func() CheckInput {
				in := passingInput()
				in.USB = Identity{VendorID: 0x1234, ProductID: 0x5678, ProductName: "Whatever"}
				in.Reported = Identity{VendorID: 0x1234, ProductID: 0x5678, ProductName: "Whatever"}
				return in
			},
			want: []string{
				`self-check failed: unknown device: USB 1234:5678 "Whatever" is not a keyboard this tool knows`,
			},
		},
		{
			name:  "NUT75 session: wrong-Model clarity plus the layout check cannot run",
			model: NUT75,
			in: func() CheckInput {
				in := passingInput()
				in.USB.ProductName = "NUT75"
				in.Reported.ProductName = "NUT75"
				return in
			},
			want: []string{nut75Line, nut75Table},
		},
		{
			name:  "check 2 alone: binding in a Key Slot nothing binds (slot 112)",
			model: NUT87,
			in: func() CheckInput {
				in := passingInput()
				in.Base[112] = keyboardAction(4)
				return in
			},
			want: []string{formatLine(layoutLine, "base layer Key Slot 112")},
		},
		{
			name:  "check 2 alone: out-of-layout binding past the layout (slot 120)",
			model: NUT87,
			in: func() CheckInput {
				in := passingInput()
				in.Base[120] = keyboardAction(5)
				return in
			},
			want: []string{formatLine(layoutLine, "base layer Key Slot 120")},
		},
		{
			name:  "check 2 alone: Fn layer is checked too (slot 121)",
			model: NUT87,
			in: func() CheckInput {
				in := passingInput()
				in.Fn[121] = protocol.KeyAction{Type: protocol.ActionMouse}
				return in
			},
			want: []string{formatLine(layoutLine, "fn layer Key Slot 121")},
		},
		{
			name:  "check 2 alone: both layers, still one line naming every slot",
			model: NUT87,
			in: func() CheckInput {
				in := passingInput()
				in.Base[112] = keyboardAction(4)
				in.Base[120] = protocol.KeyAction{Type: protocol.ActionMouse}
				in.Fn[121] = protocol.KeyAction{Type: protocol.ActionMacro}
				return in
			},
			want: []string{formatLine(layoutLine, "base layer Key Slots 112, 120; fn layer Key Slot 121")},
		},
		{
			// The block-tail marker (bytes 508..511 = 00 00 AA 55) is the
			// misalignment detector: a framing bug shifts the tail away.
			name:  "check 2 alone: base keymap block tail is not the marker",
			model: NUT87,
			in: func() CheckInput {
				in := passingInput()
				in.Base[127] = protocol.KeyAction{}
				return in
			},
			want: []string{formatLine(tailLine, "GET_KEY bytes 510..511 are 0x00 0x00")},
		},
		{
			name:  "check 2 alone: both block tails misaligned, one line naming both",
			model: NUT87,
			in: func() CheckInput {
				in := passingInput()
				in.Base[127] = protocol.KeyAction{}
				in.Fn[127] = protocol.KeyAction{Raw: [4]byte{0, 0, 0x12, 0x34}}
				return in
			},
			want: []string{formatLine(tailLine,
				"GET_KEY bytes 510..511 are 0x00 0x00; GET_FN_KEY bytes 510..511 are 0x12 0x34")},
		},
		{
			name:  "check 2: binding out of place and tail misaligned are both named",
			model: NUT87,
			in: func() CheckInput {
				in := passingInput()
				in.Base[120] = keyboardAction(5)
				in.Base[127] = protocol.KeyAction{}
				return in
			},
			want: []string{
				formatLine(layoutLine, "base layer Key Slot 120"),
				formatLine(tailLine, "GET_KEY bytes 510..511 are 0x00 0x00"),
			},
		},
		{
			name:  "check 3 alone: check code is neither written nor factory/unwritten",
			model: NUT87,
			in: func() CheckInput {
				in := passingInput()
				in.Lighting.CheckCode = [2]byte{0xAB, 0x01}
				in.Lighting.CheckCodeOK = false
				return in
			},
			want: []string{formatLine(lightLine, "0xAB 0x01")},
		},
		{
			name:  "checks 2 and 3 fail: both named, one line each",
			model: NUT87,
			in: func() CheckInput {
				in := passingInput()
				in.Base[120] = keyboardAction(4)
				in.Lighting.CheckCode = [2]byte{0x55, 0xAA}
				in.Lighting.CheckCodeOK = false
				return in
			},
			want: []string{formatLine(layoutLine, "base layer Key Slot 120"), formatLine(lightLine, "0x55 0xAA")},
		},
		{
			name:  "all three checks fail: all named, one line each",
			model: NUT87,
			in: func() CheckInput {
				in := passingInput()
				in.Reported.ProductID = 0x9999
				in.Reported.Product = 0x9999
				in.Base[120] = keyboardAction(4)
				in.Lighting.CheckCode = [2]byte{0xAA, 0x54}
				in.Lighting.CheckCodeOK = false
				return in
			},
			want: []string{
				verifyLine,
				formatLine(layoutLine, "base layer Key Slot 120"),
				formatLine(lightLine, "0xAA 0x54"),
			},
		},
		{
			name:  "unknown page type marker passes check 2 (known slot)",
			model: NUT87,
			in: func() CheckInput {
				in := passingInput()
				in.Base[0] = protocol.KeyAction{
					Type: protocol.ActionUnknown, Params: [3]byte{1, 2, 3}, Raw: [4]byte{42, 1, 2, 3},
				}
				return in
			},
			want: nil,
		},
		{
			name:  "unknown page type marker passes check 2 (firmware-matrix slot)",
			model: NUT87,
			in: func() CheckInput {
				in := passingInput()
				in.Fn[101] = protocol.KeyAction{
					Type: protocol.ActionUnknown, Params: [3]byte{1, 2, 3}, Raw: [4]byte{42, 1, 2, 3},
				}
				return in
			},
			want: nil,
		},
		{
			// The rule covers every non-DEFAULT Key Action, including an
			// explicit unknown marker: an unknown marker outside every
			// KnownSlot is exactly the misalignment symptom check 2 exists
			// to catch.
			name:  "check 2 alone: unknown page type marker fails check 2 (unknown slot)",
			model: NUT87,
			in: func() CheckInput {
				in := passingInput()
				in.Base[120] = protocol.KeyAction{
					Type: protocol.ActionUnknown, Params: [3]byte{1, 2, 3}, Raw: [4]byte{42, 1, 2, 3},
				}
				return in
			},
			want: []string{formatLine(layoutLine, "base layer Key Slot 120")},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := RunChecks(tc.model, tc.in())
			if tc.want == nil {
				if err != nil {
					t.Fatalf("RunChecks = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("RunChecks = nil, want %d failure line(s)", len(tc.want))
			}
			lines := strings.Split(err.Error(), "\n")
			if len(lines) != len(tc.want) {
				t.Fatalf("RunChecks error has %d lines, want %d:\n%s", len(lines), len(tc.want), err)
			}
			for i, line := range lines {
				if !strings.HasPrefix(line, selfCheckPrefix) {
					t.Errorf("line %d %q does not start with %q", i+1, line, selfCheckPrefix)
				}
				if line != tc.want[i] {
					t.Errorf("line %d =\n%q\nwant\n%q", i+1, line, tc.want[i])
				}
			}
		})
	}
}

// The wrong-Model refusal stays inspectable: the aggregated error unwraps
// to the WrongModelError the domain already defines.
func TestRunChecksWrongModelIsUnwrappable(t *testing.T) {
	in := passingInput()
	in.USB.ProductName = "NUT75"
	in.Reported.ProductName = "NUT75"
	err := RunChecks(NUT87, in)
	if err == nil {
		t.Fatal("RunChecks = nil, want wrong-Model error")
	}
	var wrong *WrongModelError
	if !errors.As(err, &wrong) {
		t.Fatalf("RunChecks error %v does not unwrap to *WrongModelError", err)
	}
}

// The healthy-Device regression guard: the REAL recorded fixtures
// (2026-10-05, firmware 1.20, docs/capture.md Method A —
// testdata/captures/) must pass the recalibrated checks. That is what
// "a healthy Device passes" is calibrated against.
func TestRunChecksPassesOnRealRecordedFixtures(t *testing.T) {
	const dir = "../../testdata/captures"
	in := CheckInput{
		USB:      nut87Identity(),
		Reported: nut87Identity(),
		Base:     loadKeymapFixture(t, dir+"/get_key", "nut87"),
		Fn:       loadKeymapFixture(t, dir+"/get_fn_key", "nut87"),
		Lighting: loadLightingFixture(t, dir+"/get_led_effect", "nut87"),
	}
	if err := RunChecks(NUT87, in); err != nil {
		t.Fatalf("RunChecks on the recorded fixtures = %v, want nil (a healthy Device must pass)", err)
	}
}

// loadKeymapFixture decodes one recorded keymap exchange the way the typed
// read does: reassemble response payloads, then decode.
func loadKeymapFixture(t *testing.T, dir, name string) protocol.Keymap {
	t.Helper()
	km, err := protocol.DecodeKeymap(fixturePayload(t, dir, name, protocol.KeymapSize))
	if err != nil {
		t.Fatalf("DecodeKeymap(%s/%s): %v", dir, name, err)
	}
	return km
}

func loadLightingFixture(t *testing.T, dir, name string) protocol.LightingEffect {
	t.Helper()
	le, err := protocol.DecodeLightingEffect(fixturePayload(t, dir, name, protocol.LEDEffectSize))
	if err != nil {
		t.Fatalf("DecodeLightingEffect(%s/%s): %v", dir, name, err)
	}
	return le
}

// fixturePayload reassembles a recorded exchange's response payloads and
// truncates to size (docs/protocol.md §2 — the same rule the transfer layer
// applies).
func fixturePayload(t *testing.T, dir, name string, size int) []byte {
	t.Helper()
	x, err := fixture.Load(dir, name)
	if err != nil {
		t.Fatalf("fixture.Load(%s, %s): %v", dir, name, err)
	}
	var out []byte
	for _, rep := range x.Responses {
		resp, err := protocol.ParseResponse(rep)
		if err != nil {
			t.Fatalf("ParseResponse(%s/%s): %v", dir, name, err)
		}
		out = append(out, resp.Data...)
	}
	if len(out) > size {
		out = out[:size]
	}
	return out
}

// formatLine fills one check-failure line's detail for the tests.
func formatLine(tmpl, detail string) string {
	return fmt.Sprintf(tmpl, detail)
}
