package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/ht4w5/nutctl/internal/fixture"
	"github.com/ht4w5/nutctl/internal/hid"
	"github.com/ht4w5/nutctl/internal/hidfake"
)

// fixturesDir is resolved to an absolute path up front: the State File tests
// chdir into scratch directories (the golden read writes to the CWD).
var fixturesDir = func() string {
	abs, err := filepath.Abs("../../testdata/captures")
	if err != nil {
		panic(err)
	}
	return abs
}()

func loadFixture(t *testing.T, cmd, name string) fixture.Exchange {
	t.Helper()
	x, err := fixture.Load(fixturesDir+"/"+cmd, name)
	if err != nil {
		t.Fatalf("load fixture %s/%s: %v", cmd, name, err)
	}
	return x
}

// readPathFixtures are the fixture exchanges of the full v0 read pass, in
// read order: identity, both keymaps, the Lighting Effect, Per-Key RGB and
// Settings.
func readPathFixtures(t *testing.T) []fixture.Exchange {
	t.Helper()
	return []fixture.Exchange{
		loadFixture(t, "get_device_info", "nut87"),
		loadFixture(t, "get_key", "nut87"),
		loadFixture(t, "get_fn_key", "nut87"),
		loadFixture(t, "get_led_effect", "nut87"),
		loadFixture(t, "get_custom_led_data", "nut87"),
		loadFixture(t, "get_game_mode", "nut87"),
	}
}

// fault corrupts bytes of one response report in a fixture exchange, e.g. to
// break the lighting check code or plant a Key Action outside the layout.
type fault struct {
	cmd  string // fixture directory name (the exchange's cmd)
	res  int    // response report index within the exchange
	off  int    // byte offset within the report
	data []byte // replacement bytes
}

// newNut87Fake returns an UNSCRIPTED fake NUT87 (identity only), for tests
// that script whole sessions themselves — as opposed to nut87, whose fake
// already answers one full read path.
func newNut87Fake(t *testing.T, path string) *hidfake.Device {
	t.Helper()
	return hidfake.New(hid.Info{
		Path:         path,
		VendorID:     0x0C45,
		ProductID:    0x880C,
		ProductName:  "NUT87",
		Manufacturer: "hfdic",
		UsagePage:    0xFF68,
		ReportLength: 64,
	})
}

// replayReadPath scripts one full read pass (probe + the five blocks) against
// a fake.
func replayReadPath(d *hidfake.Device, t *testing.T) {
	t.Helper()
	for _, x := range readPathFixtures(t) {
		d.Replay(x)
	}
}

// replaySets scripts one full batched apply: the recorded set_* exchanges, in
// the order `load` writes the blocks.
func replaySets(d *hidfake.Device, t *testing.T) {
	t.Helper()
	for _, x := range setFixtures(t) {
		d.Replay(x)
	}
}

// setFixtures are the recorded write exchanges of one full apply, in the
// order `load` writes the blocks.
func setFixtures(t *testing.T) []fixture.Exchange {
	t.Helper()
	return []fixture.Exchange{
		loadFixture(t, "set_key", "nut87"),
		loadFixture(t, "set_fn_key", "nut87"),
		loadFixture(t, "set_led_effect", "nut87"),
		loadFixture(t, "set_custom_led_data", "nut87"),
		loadFixture(t, "set_game_mode", "nut87"),
	}
}

// nut87 returns a scripted fake NUT87 that answers the full v0 read path from
// the fixtures recorded off real hardware (64-byte reports).
func nut87(t *testing.T, path string) *hidfake.Device {
	t.Helper()
	return nut87Faulty(t, path)
}

// nut87Faulty is nut87 with bytes patched into response reports — the way to
// simulate a Device that fails a self-check or carries an out-of-layout
// binding.
func nut87Faulty(t *testing.T, path string, faults ...fault) *hidfake.Device {
	t.Helper()
	d := newNut87Fake(t, path)
	for _, x := range readPathFixtures(t) {
		for _, f := range faults {
			if f.cmd != x.Cmd {
				continue
			}
			report := bytes.Clone(x.Responses[f.res])
			copy(report[f.off:], f.data)
			x.Responses[f.res] = report
		}
		d.Replay(x)
	}
	return d
}

func run(t *testing.T, enum hid.Enumerator, args ...string) (int, string, string) {
	t.Helper()
	return runStdin(t, enum, "", args...)
}

// runStdin is run with scripted standard input — the write gate's golden-read
// prompt reads it.
func runStdin(t *testing.T, enum hid.Enumerator, stdin string, args ...string) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := Run(args, Deps{
		Devices: enum,
		Stdin:   strings.NewReader(stdin),
		Stdout:  &out,
		Stderr:  &errOut,
	})
	return code, out.String(), errOut.String()
}

// keySlotRows counts the Key Slot rows (leading slot id) of a tabwriter
// keymap listing — one row per Key Slot 0..127 is the listing contract.
func keySlotRows(out string) int {
	return len(regexp.MustCompile(`(?m)^\d+\s`).FindAllString(out, -1))
}

func TestListIdentifiesModels(t *testing.T) {
	d87 := nut87(t, "/dev/hidraw3")
	d75 := hidfake.New(hid.Info{
		Path: "/dev/hidraw4", VendorID: 0x0C45, ProductID: 0x880C,
		ProductName: "NUT75", UsagePage: 0xFF68, ReportLength: 64,
	})
	enum := &hidfake.Enumerator{Devices: []*hidfake.Device{d87, d75}}

	code, out, errOut := run(t, enum, "list")
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr: %s)", code, errOut)
	}
	for _, want := range []string{
		"/dev/hidraw3", "/dev/hidraw4",
		"0c45:880c", "NUT87", "NUT75",
		"1.20", // firmware version from the probe
		"ok", "wrong Model (not supported)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("list output missing %q:\n%s", want, out)
		}
	}
}

func TestListJSONIsStable(t *testing.T) {
	d87 := nut87(t, "/dev/hidraw3")
	enum := &hidfake.Enumerator{Devices: []*hidfake.Device{d87}}

	code, out, errOut := run(t, enum, "list", "--json")
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr: %s)", code, errOut)
	}
	want := `[
  {
    "path": "/dev/hidraw3",
    "usbVendorId": 3141,
    "usbProductId": 34828,
    "productName": "NUT87",
    "manufacturer": "hfdic",
    "model": "NUT87",
    "supported": true,
    "firmware": "1.20",
    "status": "ok",
    "detail": ""
  }
]
`
	if out != want {
		t.Errorf("list --json:\ngot:\n%s\nwant:\n%s", out, want)
	}
}

func TestInfoHumanOutput(t *testing.T) {
	d87 := nut87(t, "/dev/hidraw3")
	enum := &hidfake.Enumerator{Devices: []*hidfake.Device{d87}}

	code, out, errOut := run(t, enum, "info")
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr: %s)", code, errOut)
	}
	for _, want := range []string{
		"Model:           NUT87",
		"Connection:      USB",
		"Path:            /dev/hidraw3",
		`Device Identity: 0c45:880c "NUT87"`,
		"Firmware:        1.20",
		"Firmware status: ok",
		"Report Rate:     8K",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("info output missing %q:\n%s", want, out)
		}
	}
}

func TestInfoJSONIsStable(t *testing.T) {
	d87 := nut87(t, "/dev/hidraw3")
	enum := &hidfake.Enumerator{Devices: []*hidfake.Device{d87}}

	code, out, errOut := run(t, enum, "info", "--json")
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr: %s)", code, errOut)
	}
	want := `{
  "model": "NUT87",
  "connection": "USB",
  "path": "/dev/hidraw3",
  "identity": {
    "usbVendorId": 3141,
    "usbProductId": 34828,
    "productName": "NUT87",
    "manufacturer": "hfdic",
    "firmwareVendorId": 3141,
    "firmwareProductId": 34828,
    "firmwareManufacturer": 32,
    "firmwareProduct": 2
  },
  "firmware": {
    "version": "1.20",
    "frameVersion": 0,
    "lightingVersion": 1,
    "status": 0,
    "statusText": "ok"
  },
  "reportRate": "8K",
  "batteryLevel": 0,
  "chargeStatus": 2,
  "workMode": 0,
  "romSize": 64,
  "macroSpaceSize": 3072
}
`
	if out != want {
		t.Errorf("info --json:\ngot:\n%s\nwant:\n%s", out, want)
	}
}

func TestInfoRefusesWrongModel(t *testing.T) {
	// A sibling Model sharing the USB product id must be refused with a clear
	// wrong-Model error and never be configured.
	d75 := hidfake.New(hid.Info{
		Path: "/dev/hidraw4", VendorID: 0x0C45, ProductID: 0x880C,
		ProductName: "NUT75", UsagePage: 0xFF68, ReportLength: 64,
	})
	enum := &hidfake.Enumerator{Devices: []*hidfake.Device{d75}}

	code, out, errOut := run(t, enum, "info")
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if out != "" {
		t.Errorf("stdout = %q, want empty", out)
	}
	for _, want := range []string{"wrong Model", "NUT75", "NUT87"} {
		if !strings.Contains(errOut, want) {
			t.Errorf("stderr missing %q:\n%s", want, errOut)
		}
	}
	if d75.Opened() {
		t.Error("the NUT75 was opened; it must never be configured")
	}
	if got := len(d75.Sent()); got != 0 {
		t.Errorf("the NUT75 received %d reports; it must never be configured", got)
	}
}

func TestInfoNoDevice(t *testing.T) {
	enum := &hidfake.Enumerator{}
	code, _, errOut := run(t, enum, "info")
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	for _, want := range []string{"no supported device found", "udev"} {
		if !strings.Contains(errOut, want) {
			t.Errorf("stderr missing %q:\n%s", want, errOut)
		}
	}
}

func TestInfoMultipleDevicesNeedsSelector(t *testing.T) {
	d1 := nut87(t, "/dev/hidraw3")
	d2 := nut87(t, "/dev/hidraw5")
	enum := &hidfake.Enumerator{Devices: []*hidfake.Device{d1, d2}}

	code, _, errOut := run(t, enum, "info")
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(errOut, "--device") {
		t.Errorf("stderr does not point at --device:\n%s", errOut)
	}

	code, out, _ := run(t, enum, "info", "--device", "/dev/hidraw5")
	if code != 0 {
		t.Fatalf("exit with --device = %d, want 0", code)
	}
	if !strings.Contains(out, "Path:            /dev/hidraw5") {
		t.Errorf("info --device picked the wrong device:\n%s", out)
	}
}

func TestInfoPermissionDeniedHint(t *testing.T) {
	d87 := nut87(t, "/dev/hidraw3")
	d87.OpenErr = hid.ErrPermission
	enum := &hidfake.Enumerator{Devices: []*hidfake.Device{d87}}

	code, _, errOut := run(t, enum, "info")
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	for _, want := range []string{"permission denied", "udev"} {
		if !strings.Contains(errOut, want) {
			t.Errorf("stderr missing %q:\n%s", want, errOut)
		}
	}
}

func TestInfoDeviceBusyHint(t *testing.T) {
	d87 := nut87(t, "/dev/hidraw3")
	d87.OpenErr = hid.ErrBusy
	enum := &hidfake.Enumerator{Devices: []*hidfake.Device{d87}}

	code, _, errOut := run(t, enum, "info")
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	for _, want := range []string{"device busy", "another nutctl session", "retry", "vendor app"} {
		if !strings.Contains(errOut, want) {
			t.Errorf("stderr missing %q:\n%s", want, errOut)
		}
	}
}

func TestInfoProbeFailure(t *testing.T) {
	d87 := nut87(t, "/dev/hidraw3")
	d87.SendErr = errors.New("cable on fire")
	enum := &hidfake.Enumerator{Devices: []*hidfake.Device{d87}}

	code, _, errOut := run(t, enum, "info")
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(errOut, "cable on fire") {
		t.Errorf("stderr does not surface the probe error:\n%s", errOut)
	}
}

func TestUnknownCommandIsUsageError(t *testing.T) {
	enum := &hidfake.Enumerator{}
	code, _, errOut := run(t, enum, "frobnicate")
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if !strings.Contains(errOut, "unknown command") {
		t.Errorf("stderr missing usage error:\n%s", errOut)
	}
}

func TestCandidateFilterIgnoresForeignInterfaces(t *testing.T) {
	// A boot-protocol keyboard interface (usage page 1) is not a candidate:
	// it cannot speak the vendor protocol.
	d := hidfake.New(hid.Info{
		Path: "/dev/hidraw2", VendorID: 0x046D, ProductID: 0xC31C,
		ProductName: "Logitech K120", UsagePage: 0x0001, ReportLength: 8,
	})
	enum := &hidfake.Enumerator{Devices: []*hidfake.Device{d}}

	code, out, _ := run(t, enum, "list")
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if strings.Contains(out, "Logitech") {
		t.Errorf("foreign interface listed as candidate:\n%s", out)
	}
}

// --- `nutctl get` ---

func TestGetKeymapHumanOutput(t *testing.T) {
	d := nut87(t, "/dev/hidraw3")
	enum := &hidfake.Enumerator{Devices: []*hidfake.Device{d}}

	code, out, errOut := run(t, enum, "get", "keymap")
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr: %s)", code, errOut)
	}
	// The healthy-Device pass is asserted over the REAL recorded fixtures
	// (testdata/captures/, 2026-10-05, firmware 1.20): the self-checks must
	// accept what real hardware reports.
	if strings.Contains(errOut, "self-check failed") {
		t.Errorf("stderr has self-check failures on a healthy Device:\n%s", errOut)
	}
	for _, want := range []string{
		"Model: NUT87",
		"Layer: base",
		"Esc", // every Key Slot is listed with its display name
		"KEYBOARD(00 29 00)",
		"Volume Up (knob clockwise)", // Knob gestures are ordinary rows
		"Mute (knob press)",
		"Volume Down (knob counter-clockwise)",
		"CONSUMER(e9 00 00)",
		"DEFAULT",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("get keymap output missing %q:\n%s", want, out)
		}
	}
	// One row per Key Slot 0..127: every Key Slot is listed, labelled with
	// what it is — physical key, Knob gesture, firmware-matrix slot, or
	// nothing the Model knows at all.
	if got := keySlotRows(out); got != 128 {
		t.Errorf("get keymap lists %d Key Slot rows, want 128", got)
	}
	if !regexp.MustCompile(`(?m)^29\s+\(firmware default — no physical key\)\s+KEYBOARD\(00 53 00\)$`).MatchString(out) {
		t.Errorf("get keymap output lacks the firmware-matrix Key Slot 29 row:\n%s", out)
	}
	if !regexp.MustCompile(`(?m)^112\s+\(not in layout\)\s+DEFAULT$`).MatchString(out) {
		t.Errorf("get keymap output lacks the out-of-layout Key Slot 112 row:\n%s", out)
	}
}

func TestGetKeymapUnknownMarkerRendersExplicitly(t *testing.T) {
	// An unknown page-type marker in a Key Slot the layout knows is just a
	// binding this build cannot name: it renders as the explicit
	// UNKNOWN(...) marker — never dropped, never a crash — and the
	// self-checks still pass. (Unknown-page-type coverage lives in the codec
	// tests; this is the rendering seam.)
	d := nut87Faulty(t, "/dev/hidraw3", fault{
		cmd: "GET_KEY", res: 0, off: 28, data: []byte{0x2a, 0x01, 0x02, 0x03},
	})
	enum := &hidfake.Enumerator{Devices: []*hidfake.Device{d}}

	code, out, errOut := run(t, enum, "get", "keymap")
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr: %s)", code, errOut)
	}
	if !regexp.MustCompile(`(?m)^5\s+F5\s+UNKNOWN\(2a 01 02 03\)$`).MatchString(out) {
		t.Errorf("get keymap output lacks the UNKNOWN marker row:\n%s", out)
	}
}

func TestGetKeymapFnLayerHumanOutput(t *testing.T) {
	d := nut87(t, "/dev/hidraw3")
	enum := &hidfake.Enumerator{Devices: []*hidfake.Device{d}}

	code, out, errOut := run(t, enum, "get", "keymap", "--layer", "fn")
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr: %s)", code, errOut)
	}
	for _, want := range []string{
		"Layer: fn",
		"FUNC(00 00 01)", // Fn layer slot 0
		"FUNC(00 00 19)", // Fn layer slot 108
		// Fn-disabled Key Slots stay visible but marked — their Key Action
		// is still what the Device reports.
		"F2 (fn-disabled)",
		"F12 (fn-disabled)",
		"Volume Up (knob clockwise)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("get keymap --layer fn output missing %q:\n%s", want, out)
		}
	}
	if got := keySlotRows(out); got != 128 {
		t.Errorf("get keymap --layer fn lists %d Key Slot rows, want 128", got)
	}
}

func TestGetKeymapOutOfLayoutUnknownMarkerFailsSelfChecks(t *testing.T) {
	// An unknown marker outside every Key Slot the Model knows is exactly
	// the misalignment symptom self-check 2 exists to catch: it must fail
	// loudly (exit 1, the "self-check failed: …" lines on stderr). The
	// requested view still goes to stdout — the checks gate WRITES
	// (ADR-0003), and the state display is exactly what a failing check
	// needs for diagnosis.
	d := nut87Faulty(t, "/dev/hidraw3", fault{
		cmd: "GET_KEY", res: 8, off: 40, data: []byte{0x2a, 0x01, 0x02, 0x03},
	})
	enum := &hidfake.Enumerator{Devices: []*hidfake.Device{d}}

	code, out, errOut := run(t, enum, "get", "keymap")
	if code != 1 {
		t.Fatalf("exit = %d, want 1 (stderr: %s)", code, errOut)
	}
	if !strings.Contains(out, "Model: NUT87") {
		t.Errorf("stdout does not show the requested view for diagnosis:\n%s", out)
	}
	if !regexp.MustCompile(`(?m)^120\s+\(not in layout\)\s+UNKNOWN\(2a 01 02 03\)$`).MatchString(out) {
		t.Errorf("stdout lacks the failing Key Slot 120 row:\n%s", out)
	}
	for _, want := range []string{"self-check failed", "Key Slot 120"} {
		if !strings.Contains(errOut, want) {
			t.Errorf("stderr missing %q:\n%s", want, errOut)
		}
	}
}

func TestGetKeymapBlockTailMisalignmentFailsSelfCheck(t *testing.T) {
	// The block-tail marker (bytes 510..511 = 0xaa 0x55 of each keymap
	// block) is the misalignment detector: when it is not there, the check
	// fails loudly naming the observed bytes — and the view still prints.
	d := nut87Faulty(t, "/dev/hidraw3", fault{
		cmd: "GET_KEY", res: 9, off: 14, data: []byte{0x12, 0x34},
	})
	enum := &hidfake.Enumerator{Devices: []*hidfake.Device{d}}

	code, out, errOut := run(t, enum, "get", "keymap")
	if code != 1 {
		t.Fatalf("exit = %d, want 1 (stderr: %s)", code, errOut)
	}
	if !strings.Contains(out, "Model: NUT87") {
		t.Errorf("stdout does not show the requested view for diagnosis:\n%s", out)
	}
	for _, want := range []string{"self-check failed", "GET_KEY bytes 510..511 are 0x12 0x34"} {
		if !strings.Contains(errOut, want) {
			t.Errorf("stderr missing %q:\n%s", want, errOut)
		}
	}
}

func TestGetKeymapJSONIsStable(t *testing.T) {
	d := nut87(t, "/dev/hidraw3")
	enum := &hidfake.Enumerator{Devices: []*hidfake.Device{d}}

	code, out, errOut := run(t, enum, "get", "keymap", "--json")
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr: %s)", code, errOut)
	}
	want := `{
  "model": "NUT87",
  "layer": "base",
  "slots": [
    {
      "slot": 0,
      "name": "Esc",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 29 00",
        "raw": "02 00 29 00"
      }
    },
    {
      "slot": 1,
      "name": "F1",
      "knob": null,
      "fnDisabled": true,
      "action": {
        "type": "KEYBOARD",
        "params": "00 3a 00",
        "raw": "02 00 3a 00"
      }
    },
    {
      "slot": 2,
      "name": "F2",
      "knob": null,
      "fnDisabled": true,
      "action": {
        "type": "KEYBOARD",
        "params": "00 3b 00",
        "raw": "02 00 3b 00"
      }
    },
    {
      "slot": 3,
      "name": "F3",
      "knob": null,
      "fnDisabled": true,
      "action": {
        "type": "KEYBOARD",
        "params": "00 3c 00",
        "raw": "02 00 3c 00"
      }
    },
    {
      "slot": 4,
      "name": "F4",
      "knob": null,
      "fnDisabled": true,
      "action": {
        "type": "KEYBOARD",
        "params": "00 3d 00",
        "raw": "02 00 3d 00"
      }
    },
    {
      "slot": 5,
      "name": "F5",
      "knob": null,
      "fnDisabled": true,
      "action": {
        "type": "KEYBOARD",
        "params": "00 3e 00",
        "raw": "02 00 3e 00"
      }
    },
    {
      "slot": 6,
      "name": "F6",
      "knob": null,
      "fnDisabled": true,
      "action": {
        "type": "KEYBOARD",
        "params": "00 3f 00",
        "raw": "02 00 3f 00"
      }
    },
    {
      "slot": 7,
      "name": "F7",
      "knob": null,
      "fnDisabled": true,
      "action": {
        "type": "KEYBOARD",
        "params": "00 40 00",
        "raw": "02 00 40 00"
      }
    },
    {
      "slot": 8,
      "name": "F8",
      "knob": null,
      "fnDisabled": true,
      "action": {
        "type": "KEYBOARD",
        "params": "00 41 00",
        "raw": "02 00 41 00"
      }
    },
    {
      "slot": 9,
      "name": "F9",
      "knob": null,
      "fnDisabled": true,
      "action": {
        "type": "KEYBOARD",
        "params": "00 42 00",
        "raw": "02 00 42 00"
      }
    },
    {
      "slot": 10,
      "name": "F10",
      "knob": null,
      "fnDisabled": true,
      "action": {
        "type": "KEYBOARD",
        "params": "00 43 00",
        "raw": "02 00 43 00"
      }
    },
    {
      "slot": 11,
      "name": "F11",
      "knob": null,
      "fnDisabled": true,
      "action": {
        "type": "KEYBOARD",
        "params": "00 44 00",
        "raw": "02 00 44 00"
      }
    },
    {
      "slot": 12,
      "name": "F12",
      "knob": null,
      "fnDisabled": true,
      "action": {
        "type": "KEYBOARD",
        "params": "00 45 00",
        "raw": "02 00 45 00"
      }
    },
    {
      "slot": 13,
      "name": "Volume Up",
      "knob": "clockwise",
      "fnDisabled": false,
      "action": {
        "type": "CONSUMER",
        "params": "e9 00 00",
        "raw": "03 e9 00 00"
      }
    },
    {
      "slot": 14,
      "name": "Volume Down",
      "knob": "counter-clockwise",
      "fnDisabled": false,
      "action": {
        "type": "CONSUMER",
        "params": "ea 00 00",
        "raw": "03 ea 00 00"
      }
    },
    {
      "slot": 15,
      "name": "Mute",
      "knob": "press",
      "fnDisabled": false,
      "action": {
        "type": "CONSUMER",
        "params": "e2 00 00",
        "raw": "03 e2 00 00"
      }
    },
    {
      "slot": 16,
      "name": "` + "`" + ` ~",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 35 00",
        "raw": "02 00 35 00"
      }
    },
    {
      "slot": 17,
      "name": "1 !",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 1e 00",
        "raw": "02 00 1e 00"
      }
    },
    {
      "slot": 18,
      "name": "2 @",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 1f 00",
        "raw": "02 00 1f 00"
      }
    },
    {
      "slot": 19,
      "name": "3 #",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 20 00",
        "raw": "02 00 20 00"
      }
    },
    {
      "slot": 20,
      "name": "4 $",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 21 00",
        "raw": "02 00 21 00"
      }
    },
    {
      "slot": 21,
      "name": "5 %",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 22 00",
        "raw": "02 00 22 00"
      }
    },
    {
      "slot": 22,
      "name": "6 ^",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 23 00",
        "raw": "02 00 23 00"
      }
    },
    {
      "slot": 23,
      "name": "7 \u0026",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 24 00",
        "raw": "02 00 24 00"
      }
    },
    {
      "slot": 24,
      "name": "8 *",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 25 00",
        "raw": "02 00 25 00"
      }
    },
    {
      "slot": 25,
      "name": "9 (",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 26 00",
        "raw": "02 00 26 00"
      }
    },
    {
      "slot": 26,
      "name": "0 )",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 27 00",
        "raw": "02 00 27 00"
      }
    },
    {
      "slot": 27,
      "name": "ˉ -",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 2d 00",
        "raw": "02 00 2d 00"
      }
    },
    {
      "slot": 28,
      "name": "= +",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 2e 00",
        "raw": "02 00 2e 00"
      }
    },
    {
      "slot": 29,
      "name": null,
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 53 00",
        "raw": "02 00 53 00"
      }
    },
    {
      "slot": 30,
      "name": null,
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 54 00",
        "raw": "02 00 54 00"
      }
    },
    {
      "slot": 31,
      "name": null,
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 55 00",
        "raw": "02 00 55 00"
      }
    },
    {
      "slot": 32,
      "name": "Tab",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 2b 00",
        "raw": "02 00 2b 00"
      }
    },
    {
      "slot": 33,
      "name": "Q",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 14 00",
        "raw": "02 00 14 00"
      }
    },
    {
      "slot": 34,
      "name": "W",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 1a 00",
        "raw": "02 00 1a 00"
      }
    },
    {
      "slot": 35,
      "name": "E",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 08 00",
        "raw": "02 00 08 00"
      }
    },
    {
      "slot": 36,
      "name": "R",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 15 00",
        "raw": "02 00 15 00"
      }
    },
    {
      "slot": 37,
      "name": "T",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 17 00",
        "raw": "02 00 17 00"
      }
    },
    {
      "slot": 38,
      "name": "Y",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 1c 00",
        "raw": "02 00 1c 00"
      }
    },
    {
      "slot": 39,
      "name": "U",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 18 00",
        "raw": "02 00 18 00"
      }
    },
    {
      "slot": 40,
      "name": "I",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 0c 00",
        "raw": "02 00 0c 00"
      }
    },
    {
      "slot": 41,
      "name": "O",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 12 00",
        "raw": "02 00 12 00"
      }
    },
    {
      "slot": 42,
      "name": "P",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 13 00",
        "raw": "02 00 13 00"
      }
    },
    {
      "slot": 43,
      "name": "[ {",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 2f 00",
        "raw": "02 00 2f 00"
      }
    },
    {
      "slot": 44,
      "name": "] }",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 30 00",
        "raw": "02 00 30 00"
      }
    },
    {
      "slot": 45,
      "name": null,
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 5f 00",
        "raw": "02 00 5f 00"
      }
    },
    {
      "slot": 46,
      "name": null,
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 60 00",
        "raw": "02 00 60 00"
      }
    },
    {
      "slot": 47,
      "name": null,
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 61 00",
        "raw": "02 00 61 00"
      }
    },
    {
      "slot": 48,
      "name": "Caps",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 39 00",
        "raw": "02 00 39 00"
      }
    },
    {
      "slot": 49,
      "name": "A",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 04 00",
        "raw": "02 00 04 00"
      }
    },
    {
      "slot": 50,
      "name": "S",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 16 00",
        "raw": "02 00 16 00"
      }
    },
    {
      "slot": 51,
      "name": "D",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 07 00",
        "raw": "02 00 07 00"
      }
    },
    {
      "slot": 52,
      "name": "F",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 09 00",
        "raw": "02 00 09 00"
      }
    },
    {
      "slot": 53,
      "name": "G",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 0a 00",
        "raw": "02 00 0a 00"
      }
    },
    {
      "slot": 54,
      "name": "H",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 0b 00",
        "raw": "02 00 0b 00"
      }
    },
    {
      "slot": 55,
      "name": "J",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 0d 00",
        "raw": "02 00 0d 00"
      }
    },
    {
      "slot": 56,
      "name": "K",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 0e 00",
        "raw": "02 00 0e 00"
      }
    },
    {
      "slot": 57,
      "name": "L",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 0f 00",
        "raw": "02 00 0f 00"
      }
    },
    {
      "slot": 58,
      "name": "; :",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 33 00",
        "raw": "02 00 33 00"
      }
    },
    {
      "slot": 59,
      "name": "' \"",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 34 00",
        "raw": "02 00 34 00"
      }
    },
    {
      "slot": 60,
      "name": "\\ | ",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 31 00",
        "raw": "02 00 31 00"
      }
    },
    {
      "slot": 61,
      "name": null,
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 5c 00",
        "raw": "02 00 5c 00"
      }
    },
    {
      "slot": 62,
      "name": null,
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 5d 00",
        "raw": "02 00 5d 00"
      }
    },
    {
      "slot": 63,
      "name": null,
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 5e 00",
        "raw": "02 00 5e 00"
      }
    },
    {
      "slot": 64,
      "name": "L-Shift",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 e1 00",
        "raw": "02 00 e1 00"
      }
    },
    {
      "slot": 65,
      "name": "Z",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 1d 00",
        "raw": "02 00 1d 00"
      }
    },
    {
      "slot": 66,
      "name": "X",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 1b 00",
        "raw": "02 00 1b 00"
      }
    },
    {
      "slot": 67,
      "name": "C",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 06 00",
        "raw": "02 00 06 00"
      }
    },
    {
      "slot": 68,
      "name": "V",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 19 00",
        "raw": "02 00 19 00"
      }
    },
    {
      "slot": 69,
      "name": "B",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 05 00",
        "raw": "02 00 05 00"
      }
    },
    {
      "slot": 70,
      "name": "N",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 11 00",
        "raw": "02 00 11 00"
      }
    },
    {
      "slot": 71,
      "name": "M",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 10 00",
        "raw": "02 00 10 00"
      }
    },
    {
      "slot": 72,
      "name": ", \u003c",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 36 00",
        "raw": "02 00 36 00"
      }
    },
    {
      "slot": 73,
      "name": ". \u003e",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 37 00",
        "raw": "02 00 37 00"
      }
    },
    {
      "slot": 74,
      "name": "/ ?",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 38 00",
        "raw": "02 00 38 00"
      }
    },
    {
      "slot": 75,
      "name": "R-Shift",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 e5 00",
        "raw": "02 00 e5 00"
      }
    },
    {
      "slot": 76,
      "name": "Enter",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 28 00",
        "raw": "02 00 28 00"
      }
    },
    {
      "slot": 77,
      "name": null,
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 59 00",
        "raw": "02 00 59 00"
      }
    },
    {
      "slot": 78,
      "name": null,
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 5a 00",
        "raw": "02 00 5a 00"
      }
    },
    {
      "slot": 79,
      "name": null,
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 5b 00",
        "raw": "02 00 5b 00"
      }
    },
    {
      "slot": 80,
      "name": "L-Ctrl",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 e0 00",
        "raw": "02 00 e0 00"
      }
    },
    {
      "slot": 81,
      "name": "L-Win",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 e3 00",
        "raw": "02 00 e3 00"
      }
    },
    {
      "slot": 82,
      "name": "L-Alt",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 e2 00",
        "raw": "02 00 e2 00"
      }
    },
    {
      "slot": 83,
      "name": "Spacebar",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 2c 00",
        "raw": "02 00 2c 00"
      }
    },
    {
      "slot": 84,
      "name": "R-Alt",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 e6 00",
        "raw": "02 00 e6 00"
      }
    },
    {
      "slot": 85,
      "name": "Fn",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 af 00",
        "raw": "02 00 af 00"
      }
    },
    {
      "slot": 86,
      "name": "Menu",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 65 00",
        "raw": "02 00 65 00"
      }
    },
    {
      "slot": 87,
      "name": "R-Ctrl",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 e4 00",
        "raw": "02 00 e4 00"
      }
    },
    {
      "slot": 88,
      "name": "←",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 50 00",
        "raw": "02 00 50 00"
      }
    },
    {
      "slot": 89,
      "name": "↓",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 51 00",
        "raw": "02 00 51 00"
      }
    },
    {
      "slot": 90,
      "name": "↑",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 52 00",
        "raw": "02 00 52 00"
      }
    },
    {
      "slot": 91,
      "name": "→",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 4f 00",
        "raw": "02 00 4f 00"
      }
    },
    {
      "slot": 92,
      "name": "Backspace",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 2a 00",
        "raw": "02 00 2a 00"
      }
    },
    {
      "slot": 93,
      "name": null,
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 62 00",
        "raw": "02 00 62 00"
      }
    },
    {
      "slot": 94,
      "name": null,
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 63 00",
        "raw": "02 00 63 00"
      }
    },
    {
      "slot": 95,
      "name": null,
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 58 00",
        "raw": "02 00 58 00"
      }
    },
    {
      "slot": 96,
      "name": null,
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "CONSUMER",
        "params": "92 01 00",
        "raw": "03 92 01 00"
      }
    },
    {
      "slot": 97,
      "name": null,
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 32 00",
        "raw": "02 00 32 00"
      }
    },
    {
      "slot": 98,
      "name": null,
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 64 00",
        "raw": "02 00 64 00"
      }
    },
    {
      "slot": 99,
      "name": "Print",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 46 00",
        "raw": "02 00 46 00"
      }
    },
    {
      "slot": 100,
      "name": "Scroll",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 47 00",
        "raw": "02 00 47 00"
      }
    },
    {
      "slot": 101,
      "name": null,
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 e7 00",
        "raw": "02 00 e7 00"
      }
    },
    {
      "slot": 102,
      "name": "Pause",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 48 00",
        "raw": "02 00 48 00"
      }
    },
    {
      "slot": 103,
      "name": "Ins",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 49 00",
        "raw": "02 00 49 00"
      }
    },
    {
      "slot": 104,
      "name": "Home",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 4a 00",
        "raw": "02 00 4a 00"
      }
    },
    {
      "slot": 105,
      "name": "PgUp",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 4b 00",
        "raw": "02 00 4b 00"
      }
    },
    {
      "slot": 106,
      "name": "Del",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 4c 00",
        "raw": "02 00 4c 00"
      }
    },
    {
      "slot": 107,
      "name": "End",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 4d 00",
        "raw": "02 00 4d 00"
      }
    },
    {
      "slot": 108,
      "name": "PgDn",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 4e 00",
        "raw": "02 00 4e 00"
      }
    },
    {
      "slot": 109,
      "name": null,
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 56 00",
        "raw": "02 00 56 00"
      }
    },
    {
      "slot": 110,
      "name": null,
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 57 00",
        "raw": "02 00 57 00"
      }
    },
    {
      "slot": 111,
      "name": null,
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 00 00",
        "raw": "02 00 00 00"
      }
    },
    {
      "slot": 112,
      "name": null,
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "DEFAULT",
        "params": "00 00 00",
        "raw": "00 00 00 00"
      }
    },
    {
      "slot": 113,
      "name": null,
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "DEFAULT",
        "params": "00 00 00",
        "raw": "00 00 00 00"
      }
    },
    {
      "slot": 114,
      "name": null,
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "DEFAULT",
        "params": "00 00 00",
        "raw": "00 00 00 00"
      }
    },
    {
      "slot": 115,
      "name": null,
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "DEFAULT",
        "params": "00 00 00",
        "raw": "00 00 00 00"
      }
    },
    {
      "slot": 116,
      "name": null,
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "DEFAULT",
        "params": "00 00 00",
        "raw": "00 00 00 00"
      }
    },
    {
      "slot": 117,
      "name": null,
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "DEFAULT",
        "params": "00 00 00",
        "raw": "00 00 00 00"
      }
    },
    {
      "slot": 118,
      "name": null,
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "DEFAULT",
        "params": "00 00 00",
        "raw": "00 00 00 00"
      }
    },
    {
      "slot": 119,
      "name": null,
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "DEFAULT",
        "params": "00 00 00",
        "raw": "00 00 00 00"
      }
    },
    {
      "slot": 120,
      "name": null,
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "DEFAULT",
        "params": "00 00 00",
        "raw": "00 00 00 00"
      }
    },
    {
      "slot": 121,
      "name": null,
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "DEFAULT",
        "params": "00 00 00",
        "raw": "00 00 00 00"
      }
    },
    {
      "slot": 122,
      "name": null,
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "DEFAULT",
        "params": "00 00 00",
        "raw": "00 00 00 00"
      }
    },
    {
      "slot": 123,
      "name": null,
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "DEFAULT",
        "params": "00 00 00",
        "raw": "00 00 00 00"
      }
    },
    {
      "slot": 124,
      "name": null,
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "DEFAULT",
        "params": "00 00 00",
        "raw": "00 00 00 00"
      }
    },
    {
      "slot": 125,
      "name": null,
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "DEFAULT",
        "params": "00 00 00",
        "raw": "00 00 00 00"
      }
    },
    {
      "slot": 126,
      "name": null,
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "DEFAULT",
        "params": "00 00 00",
        "raw": "00 00 00 00"
      }
    },
    {
      "slot": 127,
      "name": null,
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "DEFAULT",
        "params": "00 aa 55",
        "raw": "00 00 aa 55"
      }
    }
  ]
}
`
	if out != want {
		t.Errorf("get keymap --json:\ngot:\n%s\nwant:\n%s", out, want)
	}
}

func TestGetKeymapFnJSONIsStable(t *testing.T) {
	d := nut87(t, "/dev/hidraw3")
	enum := &hidfake.Enumerator{Devices: []*hidfake.Device{d}}

	code, out, errOut := run(t, enum, "get", "keymap", "--layer", "fn", "--json")
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr: %s)", code, errOut)
	}
	want := `{
  "model": "NUT87",
  "layer": "fn",
  "slots": [
    {
      "slot": 0,
      "name": "Esc",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "FUNC",
        "params": "00 00 01",
        "raw": "0d 00 00 01"
      }
    },
    {
      "slot": 1,
      "name": "F1",
      "knob": null,
      "fnDisabled": true,
      "action": {
        "type": "KEYBOARD",
        "params": "00 3a 00",
        "raw": "02 00 3a 00"
      }
    },
    {
      "slot": 2,
      "name": "F2",
      "knob": null,
      "fnDisabled": true,
      "action": {
        "type": "KEYBOARD",
        "params": "00 3b 00",
        "raw": "02 00 3b 00"
      }
    },
    {
      "slot": 3,
      "name": "F3",
      "knob": null,
      "fnDisabled": true,
      "action": {
        "type": "KEYBOARD",
        "params": "00 3c 00",
        "raw": "02 00 3c 00"
      }
    },
    {
      "slot": 4,
      "name": "F4",
      "knob": null,
      "fnDisabled": true,
      "action": {
        "type": "KEYBOARD",
        "params": "00 3d 00",
        "raw": "02 00 3d 00"
      }
    },
    {
      "slot": 5,
      "name": "F5",
      "knob": null,
      "fnDisabled": true,
      "action": {
        "type": "KEYBOARD",
        "params": "00 3e 00",
        "raw": "02 00 3e 00"
      }
    },
    {
      "slot": 6,
      "name": "F6",
      "knob": null,
      "fnDisabled": true,
      "action": {
        "type": "KEYBOARD",
        "params": "00 3f 00",
        "raw": "02 00 3f 00"
      }
    },
    {
      "slot": 7,
      "name": "F7",
      "knob": null,
      "fnDisabled": true,
      "action": {
        "type": "KEYBOARD",
        "params": "00 40 00",
        "raw": "02 00 40 00"
      }
    },
    {
      "slot": 8,
      "name": "F8",
      "knob": null,
      "fnDisabled": true,
      "action": {
        "type": "KEYBOARD",
        "params": "00 41 00",
        "raw": "02 00 41 00"
      }
    },
    {
      "slot": 9,
      "name": "F9",
      "knob": null,
      "fnDisabled": true,
      "action": {
        "type": "KEYBOARD",
        "params": "00 42 00",
        "raw": "02 00 42 00"
      }
    },
    {
      "slot": 10,
      "name": "F10",
      "knob": null,
      "fnDisabled": true,
      "action": {
        "type": "KEYBOARD",
        "params": "00 43 00",
        "raw": "02 00 43 00"
      }
    },
    {
      "slot": 11,
      "name": "F11",
      "knob": null,
      "fnDisabled": true,
      "action": {
        "type": "KEYBOARD",
        "params": "00 44 00",
        "raw": "02 00 44 00"
      }
    },
    {
      "slot": 12,
      "name": "F12",
      "knob": null,
      "fnDisabled": true,
      "action": {
        "type": "KEYBOARD",
        "params": "00 45 00",
        "raw": "02 00 45 00"
      }
    },
    {
      "slot": 13,
      "name": "Volume Up",
      "knob": "clockwise",
      "fnDisabled": false,
      "action": {
        "type": "CONSUMER",
        "params": "e9 00 00",
        "raw": "03 e9 00 00"
      }
    },
    {
      "slot": 14,
      "name": "Volume Down",
      "knob": "counter-clockwise",
      "fnDisabled": false,
      "action": {
        "type": "CONSUMER",
        "params": "ea 00 00",
        "raw": "03 ea 00 00"
      }
    },
    {
      "slot": 15,
      "name": "Mute",
      "knob": "press",
      "fnDisabled": false,
      "action": {
        "type": "CONSUMER",
        "params": "e2 00 00",
        "raw": "03 e2 00 00"
      }
    },
    {
      "slot": 16,
      "name": "` + "`" + ` ~",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 35 00",
        "raw": "02 00 35 00"
      }
    },
    {
      "slot": 17,
      "name": "1 !",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 1e 00",
        "raw": "02 00 1e 00"
      }
    },
    {
      "slot": 18,
      "name": "2 @",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 1f 00",
        "raw": "02 00 1f 00"
      }
    },
    {
      "slot": 19,
      "name": "3 #",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 20 00",
        "raw": "02 00 20 00"
      }
    },
    {
      "slot": 20,
      "name": "4 $",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 21 00",
        "raw": "02 00 21 00"
      }
    },
    {
      "slot": 21,
      "name": "5 %",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 22 00",
        "raw": "02 00 22 00"
      }
    },
    {
      "slot": 22,
      "name": "6 ^",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 23 00",
        "raw": "02 00 23 00"
      }
    },
    {
      "slot": 23,
      "name": "7 \u0026",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 24 00",
        "raw": "02 00 24 00"
      }
    },
    {
      "slot": 24,
      "name": "8 *",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 25 00",
        "raw": "02 00 25 00"
      }
    },
    {
      "slot": 25,
      "name": "9 (",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 26 00",
        "raw": "02 00 26 00"
      }
    },
    {
      "slot": 26,
      "name": "0 )",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 27 00",
        "raw": "02 00 27 00"
      }
    },
    {
      "slot": 27,
      "name": "ˉ -",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 2d 00",
        "raw": "02 00 2d 00"
      }
    },
    {
      "slot": 28,
      "name": "= +",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 2e 00",
        "raw": "02 00 2e 00"
      }
    },
    {
      "slot": 29,
      "name": null,
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 53 00",
        "raw": "02 00 53 00"
      }
    },
    {
      "slot": 30,
      "name": null,
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 54 00",
        "raw": "02 00 54 00"
      }
    },
    {
      "slot": 31,
      "name": null,
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 55 00",
        "raw": "02 00 55 00"
      }
    },
    {
      "slot": 32,
      "name": "Tab",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 2b 00",
        "raw": "02 00 2b 00"
      }
    },
    {
      "slot": 33,
      "name": "Q",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "FUNC",
        "params": "00 00 02",
        "raw": "0d 00 00 02"
      }
    },
    {
      "slot": 34,
      "name": "W",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "FUNC",
        "params": "00 00 03",
        "raw": "0d 00 00 03"
      }
    },
    {
      "slot": 35,
      "name": "E",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "FUNC",
        "params": "00 00 04",
        "raw": "0d 00 00 04"
      }
    },
    {
      "slot": 36,
      "name": "R",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "FUNC",
        "params": "00 00 05",
        "raw": "0d 00 00 05"
      }
    },
    {
      "slot": 37,
      "name": "T",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "FUNC",
        "params": "00 00 52",
        "raw": "0d 00 00 52"
      }
    },
    {
      "slot": 38,
      "name": "Y",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 1c 00",
        "raw": "02 00 1c 00"
      }
    },
    {
      "slot": 39,
      "name": "U",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 18 00",
        "raw": "02 00 18 00"
      }
    },
    {
      "slot": 40,
      "name": "I",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 0c 00",
        "raw": "02 00 0c 00"
      }
    },
    {
      "slot": 41,
      "name": "O",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 12 00",
        "raw": "02 00 12 00"
      }
    },
    {
      "slot": 42,
      "name": "P",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "FUNC",
        "params": "00 00 4b",
        "raw": "0d 00 00 4b"
      }
    },
    {
      "slot": 43,
      "name": "[ {",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 2f 00",
        "raw": "02 00 2f 00"
      }
    },
    {
      "slot": 44,
      "name": "] }",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 30 00",
        "raw": "02 00 30 00"
      }
    },
    {
      "slot": 45,
      "name": null,
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 5f 00",
        "raw": "02 00 5f 00"
      }
    },
    {
      "slot": 46,
      "name": null,
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 60 00",
        "raw": "02 00 60 00"
      }
    },
    {
      "slot": 47,
      "name": null,
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 61 00",
        "raw": "02 00 61 00"
      }
    },
    {
      "slot": 48,
      "name": "Caps",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 39 00",
        "raw": "02 00 39 00"
      }
    },
    {
      "slot": 49,
      "name": "A",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "FUNC",
        "params": "00 00 08",
        "raw": "0d 00 00 08"
      }
    },
    {
      "slot": 50,
      "name": "S",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "FUNC",
        "params": "00 00 11",
        "raw": "0d 00 00 11"
      }
    },
    {
      "slot": 51,
      "name": "D",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "FUNC",
        "params": "00 00 e3",
        "raw": "0d 00 00 e3"
      }
    },
    {
      "slot": 52,
      "name": "F",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 09 00",
        "raw": "02 00 09 00"
      }
    },
    {
      "slot": 53,
      "name": "G",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 0a 00",
        "raw": "02 00 0a 00"
      }
    },
    {
      "slot": 54,
      "name": "H",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 0b 00",
        "raw": "02 00 0b 00"
      }
    },
    {
      "slot": 55,
      "name": "J",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 0d 00",
        "raw": "02 00 0d 00"
      }
    },
    {
      "slot": 56,
      "name": "K",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 0e 00",
        "raw": "02 00 0e 00"
      }
    },
    {
      "slot": 57,
      "name": "L",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 0f 00",
        "raw": "02 00 0f 00"
      }
    },
    {
      "slot": 58,
      "name": "; :",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 33 00",
        "raw": "02 00 33 00"
      }
    },
    {
      "slot": 59,
      "name": "' \"",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 34 00",
        "raw": "02 00 34 00"
      }
    },
    {
      "slot": 60,
      "name": "\\ | ",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "FUNC",
        "params": "00 00 0b",
        "raw": "0d 00 00 0b"
      }
    },
    {
      "slot": 61,
      "name": null,
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 5c 00",
        "raw": "02 00 5c 00"
      }
    },
    {
      "slot": 62,
      "name": null,
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 5d 00",
        "raw": "02 00 5d 00"
      }
    },
    {
      "slot": 63,
      "name": null,
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 5e 00",
        "raw": "02 00 5e 00"
      }
    },
    {
      "slot": 64,
      "name": "L-Shift",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 e1 00",
        "raw": "02 00 e1 00"
      }
    },
    {
      "slot": 65,
      "name": "Z",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 1d 00",
        "raw": "02 00 1d 00"
      }
    },
    {
      "slot": 66,
      "name": "X",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 1b 00",
        "raw": "02 00 1b 00"
      }
    },
    {
      "slot": 67,
      "name": "C",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 06 00",
        "raw": "02 00 06 00"
      }
    },
    {
      "slot": 68,
      "name": "V",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 19 00",
        "raw": "02 00 19 00"
      }
    },
    {
      "slot": 69,
      "name": "B",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 05 00",
        "raw": "02 00 05 00"
      }
    },
    {
      "slot": 70,
      "name": "N",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 11 00",
        "raw": "02 00 11 00"
      }
    },
    {
      "slot": 71,
      "name": "M",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "FUNC",
        "params": "00 00 09",
        "raw": "0d 00 00 09"
      }
    },
    {
      "slot": 72,
      "name": ", \u003c",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 36 00",
        "raw": "02 00 36 00"
      }
    },
    {
      "slot": 73,
      "name": ". \u003e",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 37 00",
        "raw": "02 00 37 00"
      }
    },
    {
      "slot": 74,
      "name": "/ ?",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 38 00",
        "raw": "02 00 38 00"
      }
    },
    {
      "slot": 75,
      "name": "R-Shift",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 e5 00",
        "raw": "02 00 e5 00"
      }
    },
    {
      "slot": 76,
      "name": "Enter",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "FUNC",
        "params": "00 00 0c",
        "raw": "0d 00 00 0c"
      }
    },
    {
      "slot": 77,
      "name": null,
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 59 00",
        "raw": "02 00 59 00"
      }
    },
    {
      "slot": 78,
      "name": null,
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 5a 00",
        "raw": "02 00 5a 00"
      }
    },
    {
      "slot": 79,
      "name": null,
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 5b 00",
        "raw": "02 00 5b 00"
      }
    },
    {
      "slot": 80,
      "name": "L-Ctrl",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "FUNC",
        "params": "00 00 e7",
        "raw": "0d 00 00 e7"
      }
    },
    {
      "slot": 81,
      "name": "L-Win",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "FUNC",
        "params": "00 00 16",
        "raw": "0d 00 00 16"
      }
    },
    {
      "slot": 82,
      "name": "L-Alt",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 e2 00",
        "raw": "02 00 e2 00"
      }
    },
    {
      "slot": 83,
      "name": "Spacebar",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "FUNC",
        "params": "00 00 07",
        "raw": "0d 00 00 07"
      }
    },
    {
      "slot": 84,
      "name": "R-Alt",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 e6 00",
        "raw": "02 00 e6 00"
      }
    },
    {
      "slot": 85,
      "name": "Fn",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 af 00",
        "raw": "02 00 af 00"
      }
    },
    {
      "slot": 86,
      "name": "Menu",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 65 00",
        "raw": "02 00 65 00"
      }
    },
    {
      "slot": 87,
      "name": "R-Ctrl",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 e4 00",
        "raw": "02 00 e4 00"
      }
    },
    {
      "slot": 88,
      "name": "←",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "FUNC",
        "params": "00 00 10",
        "raw": "0d 00 00 10"
      }
    },
    {
      "slot": 89,
      "name": "↓",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "FUNC",
        "params": "00 00 0e",
        "raw": "0d 00 00 0e"
      }
    },
    {
      "slot": 90,
      "name": "↑",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "FUNC",
        "params": "00 00 0d",
        "raw": "0d 00 00 0d"
      }
    },
    {
      "slot": 91,
      "name": "→",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "FUNC",
        "params": "00 00 0f",
        "raw": "0d 00 00 0f"
      }
    },
    {
      "slot": 92,
      "name": "Backspace",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 2a 00",
        "raw": "02 00 2a 00"
      }
    },
    {
      "slot": 93,
      "name": null,
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 62 00",
        "raw": "02 00 62 00"
      }
    },
    {
      "slot": 94,
      "name": null,
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 63 00",
        "raw": "02 00 63 00"
      }
    },
    {
      "slot": 95,
      "name": null,
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 58 00",
        "raw": "02 00 58 00"
      }
    },
    {
      "slot": 96,
      "name": null,
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "CONSUMER",
        "params": "92 01 00",
        "raw": "03 92 01 00"
      }
    },
    {
      "slot": 97,
      "name": null,
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 32 00",
        "raw": "02 00 32 00"
      }
    },
    {
      "slot": 98,
      "name": null,
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 64 00",
        "raw": "02 00 64 00"
      }
    },
    {
      "slot": 99,
      "name": "Print",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 46 00",
        "raw": "02 00 46 00"
      }
    },
    {
      "slot": 100,
      "name": "Scroll",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 47 00",
        "raw": "02 00 47 00"
      }
    },
    {
      "slot": 101,
      "name": null,
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 e7 00",
        "raw": "02 00 e7 00"
      }
    },
    {
      "slot": 102,
      "name": "Pause",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 48 00",
        "raw": "02 00 48 00"
      }
    },
    {
      "slot": 103,
      "name": "Ins",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "FUNC",
        "params": "00 01 01",
        "raw": "0d 00 01 01"
      }
    },
    {
      "slot": 104,
      "name": "Home",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "FUNC",
        "params": "00 01 03",
        "raw": "0d 00 01 03"
      }
    },
    {
      "slot": 105,
      "name": "PgUp",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "FUNC",
        "params": "00 00 17",
        "raw": "0d 00 00 17"
      }
    },
    {
      "slot": 106,
      "name": "Del",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "FUNC",
        "params": "00 01 02",
        "raw": "0d 00 01 02"
      }
    },
    {
      "slot": 107,
      "name": "End",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "FUNC",
        "params": "00 01 04",
        "raw": "0d 00 01 04"
      }
    },
    {
      "slot": 108,
      "name": "PgDn",
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "FUNC",
        "params": "00 00 19",
        "raw": "0d 00 00 19"
      }
    },
    {
      "slot": 109,
      "name": null,
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 56 00",
        "raw": "02 00 56 00"
      }
    },
    {
      "slot": 110,
      "name": null,
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 57 00",
        "raw": "02 00 57 00"
      }
    },
    {
      "slot": 111,
      "name": null,
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "KEYBOARD",
        "params": "00 00 00",
        "raw": "02 00 00 00"
      }
    },
    {
      "slot": 112,
      "name": null,
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "DEFAULT",
        "params": "00 00 00",
        "raw": "00 00 00 00"
      }
    },
    {
      "slot": 113,
      "name": null,
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "DEFAULT",
        "params": "00 00 00",
        "raw": "00 00 00 00"
      }
    },
    {
      "slot": 114,
      "name": null,
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "DEFAULT",
        "params": "00 00 00",
        "raw": "00 00 00 00"
      }
    },
    {
      "slot": 115,
      "name": null,
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "DEFAULT",
        "params": "00 00 00",
        "raw": "00 00 00 00"
      }
    },
    {
      "slot": 116,
      "name": null,
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "DEFAULT",
        "params": "00 00 00",
        "raw": "00 00 00 00"
      }
    },
    {
      "slot": 117,
      "name": null,
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "DEFAULT",
        "params": "00 00 00",
        "raw": "00 00 00 00"
      }
    },
    {
      "slot": 118,
      "name": null,
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "DEFAULT",
        "params": "00 00 00",
        "raw": "00 00 00 00"
      }
    },
    {
      "slot": 119,
      "name": null,
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "DEFAULT",
        "params": "00 00 00",
        "raw": "00 00 00 00"
      }
    },
    {
      "slot": 120,
      "name": null,
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "DEFAULT",
        "params": "00 00 00",
        "raw": "00 00 00 00"
      }
    },
    {
      "slot": 121,
      "name": null,
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "DEFAULT",
        "params": "00 00 00",
        "raw": "00 00 00 00"
      }
    },
    {
      "slot": 122,
      "name": null,
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "DEFAULT",
        "params": "00 00 00",
        "raw": "00 00 00 00"
      }
    },
    {
      "slot": 123,
      "name": null,
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "DEFAULT",
        "params": "00 00 00",
        "raw": "00 00 00 00"
      }
    },
    {
      "slot": 124,
      "name": null,
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "DEFAULT",
        "params": "00 00 00",
        "raw": "00 00 00 00"
      }
    },
    {
      "slot": 125,
      "name": null,
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "DEFAULT",
        "params": "00 00 00",
        "raw": "00 00 00 00"
      }
    },
    {
      "slot": 126,
      "name": null,
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "DEFAULT",
        "params": "00 00 00",
        "raw": "00 00 00 00"
      }
    },
    {
      "slot": 127,
      "name": null,
      "knob": null,
      "fnDisabled": false,
      "action": {
        "type": "DEFAULT",
        "params": "00 aa 55",
        "raw": "00 00 aa 55"
      }
    }
  ]
}
`
	if out != want {
		t.Errorf("get keymap --layer fn --json:\ngot:\n%s\nwant:\n%s", out, want)
	}
}

func TestGetLightingHumanOutput(t *testing.T) {
	d := nut87(t, "/dev/hidraw3")
	enum := &hidfake.Enumerator{Devices: []*hidfake.Device{d}}

	code, out, errOut := run(t, enum, "get", "lighting")
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr: %s)", code, errOut)
	}
	for _, want := range []string{
		"Lighting Effect:",
		"Primary color:     #ffffff",
		"Secondary color:   #000000",
		"Brightness:        6 (range 1-6)",
		"Speed:             3 (range 1-6)",
		// The factory/unwritten check code is the observed firmware-1.20
		// state — a healthy Device shows it, not a failure.
		"Check code:        0x00 0x00 (factory/unwritten)",
		"Per-Key RGB (128 entries):",
		"0    #000000",
		"0    #00aa55", // the block-tail marker lands in the last entry's bytes
	} {
		if !strings.Contains(out, want) {
			t.Errorf("get lighting output missing %q:\n%s", want, out)
		}
	}
}

func TestGetLightingJSONIsStable(t *testing.T) {
	d := nut87(t, "/dev/hidraw3")
	enum := &hidfake.Enumerator{Devices: []*hidfake.Device{d}}

	code, out, errOut := run(t, enum, "get", "lighting", "--json")
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr: %s)", code, errOut)
	}
	want := `{
  "model": "NUT87",
  "effect": {
    "mode": 11,
    "rgb": "#ffffff",
    "driverSetting": 0,
    "secondaryRgb": "#000000",
    "colorMode": 1,
    "brightness": 6,
    "speed": 3,
    "direction": 0,
    "effectModeType": 0,
    "checkCode": "00 00",
    "checkCodeOk": true
  },
  "perKeyRgb": [
    {
      "ledId": 0,
      "rgb": "#000000"
    },
    {
      "ledId": 0,
      "rgb": "#000000"
    },
    {
      "ledId": 0,
      "rgb": "#000000"
    },
    {
      "ledId": 0,
      "rgb": "#000000"
    },
    {
      "ledId": 0,
      "rgb": "#000000"
    },
    {
      "ledId": 0,
      "rgb": "#000000"
    },
    {
      "ledId": 0,
      "rgb": "#000000"
    },
    {
      "ledId": 0,
      "rgb": "#000000"
    },
    {
      "ledId": 0,
      "rgb": "#000000"
    },
    {
      "ledId": 0,
      "rgb": "#000000"
    },
    {
      "ledId": 0,
      "rgb": "#000000"
    },
    {
      "ledId": 0,
      "rgb": "#000000"
    },
    {
      "ledId": 0,
      "rgb": "#000000"
    },
    {
      "ledId": 0,
      "rgb": "#000000"
    },
    {
      "ledId": 0,
      "rgb": "#000000"
    },
    {
      "ledId": 0,
      "rgb": "#000000"
    },
    {
      "ledId": 0,
      "rgb": "#000000"
    },
    {
      "ledId": 0,
      "rgb": "#000000"
    },
    {
      "ledId": 0,
      "rgb": "#000000"
    },
    {
      "ledId": 0,
      "rgb": "#000000"
    },
    {
      "ledId": 0,
      "rgb": "#000000"
    },
    {
      "ledId": 0,
      "rgb": "#000000"
    },
    {
      "ledId": 0,
      "rgb": "#000000"
    },
    {
      "ledId": 0,
      "rgb": "#000000"
    },
    {
      "ledId": 0,
      "rgb": "#000000"
    },
    {
      "ledId": 0,
      "rgb": "#000000"
    },
    {
      "ledId": 0,
      "rgb": "#000000"
    },
    {
      "ledId": 0,
      "rgb": "#000000"
    },
    {
      "ledId": 0,
      "rgb": "#000000"
    },
    {
      "ledId": 0,
      "rgb": "#000000"
    },
    {
      "ledId": 0,
      "rgb": "#000000"
    },
    {
      "ledId": 0,
      "rgb": "#000000"
    },
    {
      "ledId": 0,
      "rgb": "#000000"
    },
    {
      "ledId": 0,
      "rgb": "#000000"
    },
    {
      "ledId": 0,
      "rgb": "#000000"
    },
    {
      "ledId": 0,
      "rgb": "#000000"
    },
    {
      "ledId": 0,
      "rgb": "#000000"
    },
    {
      "ledId": 0,
      "rgb": "#000000"
    },
    {
      "ledId": 0,
      "rgb": "#000000"
    },
    {
      "ledId": 0,
      "rgb": "#000000"
    },
    {
      "ledId": 0,
      "rgb": "#000000"
    },
    {
      "ledId": 0,
      "rgb": "#000000"
    },
    {
      "ledId": 0,
      "rgb": "#000000"
    },
    {
      "ledId": 0,
      "rgb": "#000000"
    },
    {
      "ledId": 0,
      "rgb": "#000000"
    },
    {
      "ledId": 0,
      "rgb": "#000000"
    },
    {
      "ledId": 0,
      "rgb": "#000000"
    },
    {
      "ledId": 0,
      "rgb": "#000000"
    },
    {
      "ledId": 0,
      "rgb": "#000000"
    },
    {
      "ledId": 0,
      "rgb": "#000000"
    },
    {
      "ledId": 0,
      "rgb": "#000000"
    },
    {
      "ledId": 0,
      "rgb": "#000000"
    },
    {
      "ledId": 0,
      "rgb": "#000000"
    },
    {
      "ledId": 0,
      "rgb": "#000000"
    },
    {
      "ledId": 0,
      "rgb": "#000000"
    },
    {
      "ledId": 0,
      "rgb": "#000000"
    },
    {
      "ledId": 0,
      "rgb": "#000000"
    },
    {
      "ledId": 0,
      "rgb": "#000000"
    },
    {
      "ledId": 0,
      "rgb": "#000000"
    },
    {
      "ledId": 0,
      "rgb": "#000000"
    },
    {
      "ledId": 0,
      "rgb": "#000000"
    },
    {
      "ledId": 0,
      "rgb": "#000000"
    },
    {
      "ledId": 0,
      "rgb": "#000000"
    },
    {
      "ledId": 0,
      "rgb": "#000000"
    },
    {
      "ledId": 0,
      "rgb": "#000000"
    },
    {
      "ledId": 0,
      "rgb": "#000000"
    },
    {
      "ledId": 0,
      "rgb": "#000000"
    },
    {
      "ledId": 0,
      "rgb": "#000000"
    },
    {
      "ledId": 0,
      "rgb": "#000000"
    },
    {
      "ledId": 0,
      "rgb": "#000000"
    },
    {
      "ledId": 0,
      "rgb": "#000000"
    },
    {
      "ledId": 0,
      "rgb": "#000000"
    },
    {
      "ledId": 0,
      "rgb": "#000000"
    },
    {
      "ledId": 0,
      "rgb": "#000000"
    },
    {
      "ledId": 0,
      "rgb": "#000000"
    },
    {
      "ledId": 0,
      "rgb": "#000000"
    },
    {
      "ledId": 0,
      "rgb": "#000000"
    },
    {
      "ledId": 0,
      "rgb": "#000000"
    },
    {
      "ledId": 0,
      "rgb": "#000000"
    },
    {
      "ledId": 0,
      "rgb": "#000000"
    },
    {
      "ledId": 0,
      "rgb": "#000000"
    },
    {
      "ledId": 0,
      "rgb": "#000000"
    },
    {
      "ledId": 0,
      "rgb": "#000000"
    },
    {
      "ledId": 0,
      "rgb": "#000000"
    },
    {
      "ledId": 0,
      "rgb": "#000000"
    },
    {
      "ledId": 0,
      "rgb": "#000000"
    },
    {
      "ledId": 0,
      "rgb": "#000000"
    },
    {
      "ledId": 0,
      "rgb": "#000000"
    },
    {
      "ledId": 0,
      "rgb": "#000000"
    },
    {
      "ledId": 0,
      "rgb": "#000000"
    },
    {
      "ledId": 0,
      "rgb": "#000000"
    },
    {
      "ledId": 0,
      "rgb": "#000000"
    },
    {
      "ledId": 0,
      "rgb": "#000000"
    },
    {
      "ledId": 0,
      "rgb": "#000000"
    },
    {
      "ledId": 0,
      "rgb": "#000000"
    },
    {
      "ledId": 0,
      "rgb": "#000000"
    },
    {
      "ledId": 0,
      "rgb": "#000000"
    },
    {
      "ledId": 0,
      "rgb": "#000000"
    },
    {
      "ledId": 0,
      "rgb": "#000000"
    },
    {
      "ledId": 0,
      "rgb": "#000000"
    },
    {
      "ledId": 0,
      "rgb": "#000000"
    },
    {
      "ledId": 0,
      "rgb": "#000000"
    },
    {
      "ledId": 0,
      "rgb": "#000000"
    },
    {
      "ledId": 0,
      "rgb": "#000000"
    },
    {
      "ledId": 0,
      "rgb": "#000000"
    },
    {
      "ledId": 0,
      "rgb": "#000000"
    },
    {
      "ledId": 0,
      "rgb": "#000000"
    },
    {
      "ledId": 0,
      "rgb": "#000000"
    },
    {
      "ledId": 0,
      "rgb": "#000000"
    },
    {
      "ledId": 0,
      "rgb": "#000000"
    },
    {
      "ledId": 0,
      "rgb": "#000000"
    },
    {
      "ledId": 0,
      "rgb": "#000000"
    },
    {
      "ledId": 0,
      "rgb": "#000000"
    },
    {
      "ledId": 0,
      "rgb": "#000000"
    },
    {
      "ledId": 0,
      "rgb": "#000000"
    },
    {
      "ledId": 0,
      "rgb": "#000000"
    },
    {
      "ledId": 0,
      "rgb": "#000000"
    },
    {
      "ledId": 0,
      "rgb": "#000000"
    },
    {
      "ledId": 0,
      "rgb": "#000000"
    },
    {
      "ledId": 0,
      "rgb": "#000000"
    },
    {
      "ledId": 0,
      "rgb": "#000000"
    },
    {
      "ledId": 0,
      "rgb": "#000000"
    },
    {
      "ledId": 0,
      "rgb": "#000000"
    },
    {
      "ledId": 0,
      "rgb": "#000000"
    },
    {
      "ledId": 0,
      "rgb": "#000000"
    },
    {
      "ledId": 0,
      "rgb": "#000000"
    },
    {
      "ledId": 0,
      "rgb": "#000000"
    },
    {
      "ledId": 0,
      "rgb": "#00aa55"
    }
  ]
}
`
	if out != want {
		t.Errorf("get lighting --json:\ngot:\n%s\nwant:\n%s", out, want)
	}
}

func TestGetSettingsHumanOutput(t *testing.T) {
	d := nut87(t, "/dev/hidraw3")
	enum := &hidfake.Enumerator{Devices: []*hidfake.Device{d}}

	code, out, errOut := run(t, enum, "get", "settings")
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr: %s)", code, errOut)
	}
	for _, want := range []string{
		"Model: NUT87",
		"Report rate:           8K", // Report Rate renders as its label
		"Sleep time:            5",
		"Key delay:             3",
		"Top dead zone:         0.00",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("get settings output missing %q:\n%s", want, out)
		}
	}
}

func TestGetSettingsJSONIsStable(t *testing.T) {
	d := nut87(t, "/dev/hidraw3")
	enum := &hidfake.Enumerator{Devices: []*hidfake.Device{d}}

	code, out, errOut := run(t, enum, "get", "settings", "--json")
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr: %s)", code, errOut)
	}
	want := `{
  "model": "NUT87",
  "gameMode": 0,
  "fnSwitch": 0,
  "sleepTime": 5,
  "keyDelay": 3,
  "reportRate": "8K",
  "systemMode": 0,
  "tftDisplayTime": 0,
  "topDeadZone": 0,
  "bottomDeadZone": 0,
  "stabilityMode": 0,
  "autoCalibration": 0,
  "singleKeyWakeup": 0,
  "pushButtonMode": 0,
  "nkroSwitch": 0,
  "wirelessReportRate": 0,
  "powerMode": 0
}
`
	if out != want {
		t.Errorf("get settings --json:\ngot:\n%s\nwant:\n%s", out, want)
	}
}

func TestGetSelfChecksFailLoudly(t *testing.T) {
	// Two broken self-checks at once: the lighting check code is neither
	// written (0xaa 0x55) nor factory/unwritten (0x00 0x00), and a Key
	// Action sits in a Key Slot nothing binds. Both must be named on stderr,
	// exit 1 — and the requested view still goes to stdout, because the
	// checks gate WRITES (ADR-0003) and the state display is exactly what a
	// failing check needs for diagnosis.
	faults := []fault{
		// offsets 14..15 of the Lighting Effect must be 0xaa 0x55 or 0x00 0x00.
		{cmd: "GET_LED_EFFECT", res: 0, off: 22, data: []byte{0xAB, 0x01}},
		// a KEYBOARD Key Action in Key Slot 120, which nothing binds.
		{cmd: "GET_FN_KEY", res: 8, off: 40, data: []byte{0x02, 0x00, 0x04, 0x00}},
	}
	for _, args := range [][]string{
		{"get", "keymap"},
		{"get", "keymap", "--layer", "fn", "--json"},
		{"get", "lighting"},
		{"get", "settings", "--json"},
	} {
		d := nut87Faulty(t, "/dev/hidraw3", faults...)
		enum := &hidfake.Enumerator{Devices: []*hidfake.Device{d}}

		code, out, errOut := run(t, enum, args...)
		if code != 1 {
			t.Errorf("%v: exit = %d, want 1", args, code)
		}
		if !strings.Contains(out, `"model": "NUT87"`) && !strings.Contains(out, "Model: NUT87") {
			t.Errorf("%v: stdout does not show the requested view for diagnosis:\n%s", args, out)
		}
		if got := strings.Count(errOut, "self-check failed: "); got != 2 {
			t.Errorf("%v: stderr has %d self-check failure lines, want 2:\n%s", args, got, errOut)
		}
		for _, want := range []string{"lighting check code", "Key Slot 120"} {
			if !strings.Contains(errOut, want) {
				t.Errorf("%v: stderr missing %q:\n%s", args, want, errOut)
			}
		}
	}
}

func TestGetMacrosIsDeferred(t *testing.T) {
	enum := &hidfake.Enumerator{}
	code, out, errOut := run(t, enum, "get", "macros")
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if out != "" {
		t.Errorf("stdout = %q, want empty", out)
	}
	if !strings.Contains(errOut, "deferred to v0.1") {
		t.Errorf("stderr missing the deferral message:\n%s", errOut)
	}
}

func TestGetUsageErrors(t *testing.T) {
	for _, args := range [][]string{
		{"get"},                              // no subcommand
		{"get", "frobnicate"},                // unknown subcommand
		{"get", "keymap", "--layer", "sic"},  // bad --layer value
		{"get", "lighting", "--layer", "fn"}, // --layer does not apply here
		{"get", "settings", "extra"},         // unexpected positional argument
	} {
		enum := &hidfake.Enumerator{}
		code, out, errOut := run(t, enum, args...)
		if code != 2 {
			t.Errorf("%v: exit = %d, want 2 (stderr: %s)", args, code, errOut)
		}
		if out != "" {
			t.Errorf("%v: stdout = %q, want empty", args, out)
		}
		if !strings.Contains(errOut, "nutctl:") {
			t.Errorf("%v: stderr missing a usage error:\n%s", args, errOut)
		}
	}
}

// --- State Files (ticket 03) ---
//
// The State File format is a contract, tested as external behavior: bytes on
// disk through the CLI seam with temporary files (the spec's Testing
// Decisions — no new seam).

// stateFileEnvelope asserts the file at path has exactly the documented
// envelope: model, firmware, schema, state, with the five state blocks.
func stateFileEnvelope(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read State File: %v", err)
	}
	if !strings.HasPrefix(string(raw), "{\n  \"model\": \"NUT87\",") {
		t.Errorf("State File must be pretty-printed JSON with the envelope first, got:\n%.80s", raw)
	}
	if !strings.HasSuffix(string(raw), "}\n") {
		t.Error("State File must end with a trailing newline")
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("State File does not decode: %v", err)
	}
	if got := sortedKeys(doc); !reflect.DeepEqual(got, []string{"firmware", "model", "schema", "state"}) {
		t.Errorf("envelope keys = %v, want exactly [firmware model schema state]", got)
	}
	if doc["model"] != "NUT87" || doc["firmware"] != "1.20" || doc["schema"] != float64(1) {
		t.Errorf("envelope = model %v firmware %v schema %v, want NUT87 / 1.20 / 1",
			doc["model"], doc["firmware"], doc["schema"])
	}
	state, ok := doc["state"].(map[string]any)
	if !ok {
		t.Fatalf("state = %v, want an object", doc["state"])
	}
	if got := sortedKeys(state); !reflect.DeepEqual(got, []string{"base", "fn", "lighting", "perKeyRgb", "settings"}) {
		t.Errorf("state keys = %v, want exactly [base fn lighting perKeyRgb settings]", got)
	}
	return state
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func TestSaveWritesStateFileEnvelope(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	d := nut87(t, "/dev/hidraw3")
	enum := &hidfake.Enumerator{Devices: []*hidfake.Device{d}}

	code, out, errOut := run(t, enum, "save", path)
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr: %s)", code, errOut)
	}
	if !strings.Contains(out, path) {
		t.Errorf("save output %q does not name the file it wrote", out)
	}
	state := stateFileEnvelope(t, path)

	base := state["base"].([]any)
	if len(base) != 128 {
		t.Fatalf("base has %d rows, want 128 Key Slots", len(base))
	}
	esc := base[0].(map[string]any)
	if esc["type"] != "KEYBOARD" || esc["params"] != "00 29 00" || esc["raw"] != "02 00 29 00" {
		t.Errorf("base slot 0 (Esc) = %v, want KEYBOARD(00 29 00)", esc)
	}

	lighting := state["lighting"].(map[string]any)
	for _, marker := range []string{"driverSetting", "checkCode", "checkCodeOk"} {
		if _, ok := lighting[marker]; ok {
			t.Errorf("state.lighting carries %q — the SET format forces it on write, it is not state", marker)
		}
	}
	if lighting["brightness"] != float64(6) || lighting["mode"] != float64(11) {
		t.Errorf("lighting = mode %v brightness %v, want mode 11 brightness 6", lighting["mode"], lighting["brightness"])
	}

	entry := state["perKeyRgb"].([]any)[0].(map[string]any)
	if got := sortedKeys(entry); !reflect.DeepEqual(got, []string{"b", "g", "r"}) {
		t.Errorf("perKeyRgb entry keys = %v, want [b g r] (ledId is the index on the wire, never state)", got)
	}

	settings := state["settings"].(map[string]any)
	if settings["reportRate"] != "8K" || settings["sleepTime"] != float64(5) {
		t.Errorf("settings = reportRate %v sleepTime %v, want 8K / 5", settings["reportRate"], settings["sleepTime"])
	}
}

func TestSaveNeedsAFilePath(t *testing.T) {
	d := nut87(t, "/dev/hidraw3")
	enum := &hidfake.Enumerator{Devices: []*hidfake.Device{d}}
	code, _, errOut := run(t, enum, "save")
	if code != 2 {
		t.Fatalf("exit = %d, want 2 (usage error)", code)
	}
	if !strings.Contains(errOut, "State File path") {
		t.Errorf("stderr = %q, want the missing path named", errOut)
	}
}

// TestLoadRoundTrip is the ticket-03 round-trip property, as external
// behavior: save → load → save yields identical State Files. The fake Device
// replays the recorded write exchanges (the real set_* fixtures) for the
// apply in between; the load's read-back verification must pass clean.
func TestLoadRoundTrip(t *testing.T) {
	t.Chdir(t.TempDir())
	d := newNut87Fake(t, "/dev/hidraw3")
	for i := 0; i < 2; i++ { // save 1 and load's pre-write read pass
		for _, x := range readPathFixtures(t) {
			d.Replay(x)
		}
	}
	replaySets(d, t)         // the batched writes
	for i := 0; i < 2; i++ { // load's read-back pass and save 2
		for _, x := range readPathFixtures(t) {
			d.Replay(x)
		}
	}
	enum := &hidfake.Enumerator{Devices: []*hidfake.Device{d}}

	if code, _, errOut := run(t, enum, "save", "a.json"); code != 0 {
		t.Fatalf("save exit = %d, want 0 (stderr: %s)", code, errOut)
	}
	code, out, errOut := run(t, enum, "load", "a.json", "--i-know-what-im-doing")
	if code != 0 {
		t.Fatalf("load exit = %d, want 0 (stderr: %s)", code, errOut)
	}
	if !strings.Contains(out, "read-back verified: the Device matches the State File") {
		t.Errorf("load output missing the verification result:\n%s", out)
	}
	if code, _, errOut := run(t, enum, "save", "b.json"); code != 0 {
		t.Fatalf("save 2 exit = %d, want 0 (stderr: %s)", code, errOut)
	}

	a, err := os.ReadFile("a.json")
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile("b.json")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) {
		t.Error("save → load → save changed the State File; round trip must be identical")
	}
}

// A State File saved from another Model is refused before anything is
// written (spec user story 25): no SET request reaches the Device.
func TestLoadRefusesStateFileFromAnotherModel(t *testing.T) {
	t.Chdir(t.TempDir())
	d := newNut87Fake(t, "/dev/hidraw3")
	replayReadPath(d, t) // save only
	replayReadPath(d, t) // load's probe + read pass — no writes may follow
	enum := &hidfake.Enumerator{Devices: []*hidfake.Device{d}}

	if code, _, errOut := run(t, enum, "save", "friend.json"); code != 0 {
		t.Fatalf("save exit = %d, want 0 (stderr: %s)", code, errOut)
	}
	raw, err := os.ReadFile("friend.json")
	if err != nil {
		t.Fatal(err)
	}
	foreign := strings.Replace(string(raw), `"model": "NUT87"`, `"model": "NUT75"`, 1)
	if err := os.WriteFile("friend.json", []byte(foreign), 0o644); err != nil {
		t.Fatal(err)
	}

	code, _, errOut := run(t, enum, "load", "friend.json", "--i-know-what-im-doing")
	if code != 1 {
		t.Fatalf("exit = %d, want 1 (refused)", code)
	}
	for _, want := range []string{"wrong Model", "NUT75", "NUT87", "refusing to load"} {
		if !strings.Contains(errOut, want) {
			t.Errorf("stderr missing %q:\n%s", want, errOut)
		}
	}
}

// A firmware mismatch warns but proceeds (spec user story 26).
func TestLoadWarnsOnFirmwareMismatchButProceeds(t *testing.T) {
	t.Chdir(t.TempDir())
	d := newNut87Fake(t, "/dev/hidraw3")
	replayReadPath(d, t) // save
	replayReadPath(d, t) // load pre-write read pass
	replaySets(d, t)
	replayReadPath(d, t) // read-back
	enum := &hidfake.Enumerator{Devices: []*hidfake.Device{d}}

	if code, _, errOut := run(t, enum, "save", "old.json"); code != 0 {
		t.Fatalf("save exit = %d, want 0 (stderr: %s)", code, errOut)
	}
	raw, err := os.ReadFile("old.json")
	if err != nil {
		t.Fatal(err)
	}
	old := strings.Replace(string(raw), `"firmware": "1.20"`, `"firmware": "1.19"`, 1)
	if err := os.WriteFile("old.json", []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}

	code, out, errOut := run(t, enum, "load", "old.json", "--i-know-what-im-doing")
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (proceed) (stderr: %s)", code, errOut)
	}
	for _, want := range []string{"warning", "1.19", "1.20", "proceeding"} {
		if !strings.Contains(errOut, want) {
			t.Errorf("stderr missing %q:\n%s", want, errOut)
		}
	}
	if !strings.Contains(out, "read-back verified") {
		t.Errorf("load did not proceed to the writes:\n%s", out)
	}
}

// Writes are refused while the Device reports bootloader/firmware-recovery
// state (spec user story 28) — before the self-checks, before the prompt.
func TestLoadRefusesBootloaderDevice(t *testing.T) {
	t.Chdir(t.TempDir())
	d := newNut87Fake(t, "/dev/hidraw3")
	replayReadPath(d, t) // save against the healthy fake
	enum := &hidfake.Enumerator{Devices: []*hidfake.Device{d}}
	if code, _, errOut := run(t, enum, "save", "state.json"); code != 0 {
		t.Fatalf("save exit = %d, want 0 (stderr: %s)", code, errOut)
	}

	// firmwareStatus lives at data offset 32 = report offset 40 of the
	// GET_DEVICE_INFO response.
	boot := nut87Faulty(t, "/dev/hidraw3",
		fault{cmd: "GET_DEVICE_INFO", res: 0, off: 40, data: []byte{1}})
	for _, x := range readPathFixtures(t) {
		if x.Cmd != "GET_DEVICE_INFO" {
			boot.Replay(x) // only the probe may run; nothing else may be reached
		}
	}
	enum = &hidfake.Enumerator{Devices: []*hidfake.Device{boot}}

	code, _, errOut := run(t, enum, "load", "state.json", "--i-know-what-im-doing")
	if code != 1 {
		t.Fatalf("exit = %d, want 1 (refused)", code)
	}
	for _, want := range []string{"refusing to write", "bootloader"} {
		if !strings.Contains(errOut, want) {
			t.Errorf("stderr missing %q:\n%s", want, errOut)
		}
	}
}

// The session's first write requires the self-checks passing (ADR-0003):
// a Device that fails them is never written to.
func TestLoadRefusesFailedSelfChecks(t *testing.T) {
	t.Chdir(t.TempDir())
	d := newNut87Fake(t, "/dev/hidraw3")
	replayReadPath(d, t) // save
	enum := &hidfake.Enumerator{Devices: []*hidfake.Device{d}}
	if code, _, errOut := run(t, enum, "save", "state.json"); code != 0 {
		t.Fatalf("save exit = %d, want 0 (stderr: %s)", code, errOut)
	}

	// Break the keymap block-tail marker: a misaligned/corrupt read fails
	// self-check 2 loudly (ticket 02's detector).
	bad := nut87Faulty(t, "/dev/hidraw3",
		fault{cmd: "GET_KEY", res: 9, off: 8 + 6, data: []byte{0x00}}) // block bytes 508..511 ride in chunk 9
	replayReadPath(bad, t)
	enum = &hidfake.Enumerator{Devices: []*hidfake.Device{bad}}

	code, _, errOut := run(t, enum, "load", "state.json", "--i-know-what-im-doing")
	if code != 1 {
		t.Fatalf("exit = %d, want 1 (refused)", code)
	}
	if !strings.Contains(errOut, "self-check failed:") {
		t.Errorf("stderr missing the self-check failure:\n%s", errOut)
	}
	if !strings.Contains(errOut, "refusing to write") {
		t.Errorf("stderr missing the write refusal:\n%s", errOut)
	}
}

// The golden read of ADR-0003 (spec user stories 4, 5): the session's first
// write offers to save the current Device state first — the default filename
// is offered, never forced (ADR-0005: no file activity the user did not ask
// for).

// goldenFiles lists the golden reads written into the (scratch) CWD.
func goldenFiles(t *testing.T) []string {
	t.Helper()
	files, err := filepath.Glob("golden-*.json")
	if err != nil {
		t.Fatal(err)
	}
	return files
}

// loadSession scripts one `load` invocation: probe + pre-write read pass,
// the batched writes, and the read-back pass.
func loadSession(d *hidfake.Device, t *testing.T) {
	t.Helper()
	replayReadPath(d, t)
	replaySets(d, t)
	replayReadPath(d, t)
}

func TestLoadGoldenReadPromptSavesBeforeWriting(t *testing.T) {
	t.Chdir(t.TempDir())
	d := newNut87Fake(t, "/dev/hidraw3")
	replayReadPath(d, t) // save
	loadSession(d, t)
	enum := &hidfake.Enumerator{Devices: []*hidfake.Device{d}}

	if code, _, errOut := run(t, enum, "save", "state.json"); code != 0 {
		t.Fatalf("save exit = %d, want 0 (stderr: %s)", code, errOut)
	}
	code, out, errOut := runStdin(t, enum, "y\n", "load", "state.json")
	if code != 0 {
		t.Fatalf("load exit = %d, want 0 (stderr: %s)", code, errOut)
	}
	if !strings.Contains(errOut, "save current state to ./golden-") || !strings.Contains(errOut, "? [Y/n]") {
		t.Errorf("stderr missing the golden-read prompt:\n%s", errOut)
	}
	files := goldenFiles(t)
	if len(files) != 1 {
		t.Fatalf("golden reads written = %v, want exactly one", files)
	}
	state := stateFileEnvelope(t, files[0]) // the golden read is a State File — one code path
	if len(state["base"].([]any)) != 128 {
		t.Error("golden read does not carry the full state")
	}
	if !strings.Contains(errOut, "golden read saved to ./"+files[0]) {
		t.Errorf("stderr missing where the golden read went:\n%s", errOut)
	}
	if !strings.Contains(out, "read-back verified") {
		t.Errorf("load did not proceed to the writes:\n%s", out)
	}
}

func TestLoadGoldenReadPromptDeclineWritesWithoutSaving(t *testing.T) {
	t.Chdir(t.TempDir())
	d := newNut87Fake(t, "/dev/hidraw3")
	replayReadPath(d, t) // save
	loadSession(d, t)
	enum := &hidfake.Enumerator{Devices: []*hidfake.Device{d}}

	if code, _, _ := run(t, enum, "save", "state.json"); code != 0 {
		t.Fatalf("save exit = %d, want 0", code)
	}
	code, out, errOut := runStdin(t, enum, "n\n", "load", "state.json")
	if code != 0 {
		t.Fatalf("load exit = %d, want 0 (stderr: %s)", code, errOut)
	}
	if files := goldenFiles(t); len(files) != 0 {
		t.Errorf("golden reads written = %v, want none — a declined offer writes nothing", files)
	}
	if !strings.Contains(errOut, "skipped the golden read") {
		t.Errorf("stderr missing the dismissal:\n%s", errOut)
	}
	if !strings.Contains(out, "read-back verified") {
		t.Errorf("load did not proceed to the writes:\n%s", out)
	}
}

func TestLoadWithoutInputRefusesTheFirstWrite(t *testing.T) {
	t.Chdir(t.TempDir())
	d := newNut87Fake(t, "/dev/hidraw3")
	replayReadPath(d, t) // save
	loadSession(d, t)    // load may read, but the writes must never run unattended
	enum := &hidfake.Enumerator{Devices: []*hidfake.Device{d}}

	if code, _, _ := run(t, enum, "save", "state.json"); code != 0 {
		t.Fatalf("save exit = %d, want 0", code)
	}
	code, _, errOut := runStdin(t, enum, "", "load", "state.json") // EOF: nobody can answer
	if code != 1 {
		t.Fatalf("exit = %d, want 1 (refused)", code)
	}
	for _, want := range []string{"golden read", "--i-know-what-im-doing"} {
		if !strings.Contains(errOut, want) {
			t.Errorf("stderr missing %q:\n%s", want, errOut)
		}
	}
	if files := goldenFiles(t); len(files) != 0 {
		t.Errorf("golden reads written = %v, want none", files)
	}
}

func TestLoadSkipFlagSkipsThePromptNoisily(t *testing.T) {
	t.Chdir(t.TempDir())
	d := newNut87Fake(t, "/dev/hidraw3")
	replayReadPath(d, t) // save
	loadSession(d, t)
	enum := &hidfake.Enumerator{Devices: []*hidfake.Device{d}}

	if code, _, _ := run(t, enum, "save", "state.json"); code != 0 {
		t.Fatalf("save exit = %d, want 0", code)
	}
	code, out, errOut := run(t, enum, "load", "state.json", "--i-know-what-im-doing")
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr: %s)", code, errOut)
	}
	if strings.Contains(errOut, "? [Y/n]") {
		t.Errorf("the skip flag must skip the prompt:\n%s", errOut)
	}
	for _, want := range []string{"warning", "--i-know-what-im-doing", "golden read", "NOT saved"} {
		if !strings.Contains(errOut, want) {
			t.Errorf("the skip flag must be noisy about it, stderr missing %q:\n%s", want, errOut)
		}
	}
	if files := goldenFiles(t); len(files) != 0 {
		t.Errorf("golden reads written = %v, want none", files)
	}
	if !strings.Contains(out, "read-back verified") {
		t.Errorf("load did not proceed to the writes:\n%s", out)
	}
}

// Every apply is verified by reading the Device back and showing the diff
// (spec user story 22): a Device that does not report what was written fails
// loudly with every difference named.
func TestLoadPrintsReadBackVerificationDiff(t *testing.T) {
	t.Chdir(t.TempDir())
	d := newNut87Fake(t, "/dev/hidraw3")
	for i := 0; i < 2; i++ { // save and load's pre-write read pass
		for _, x := range readPathFixtures(t) {
			d.Replay(x)
		}
	}
	replaySets(d, t)
	// The read-back pass reports brightness 2 where the State File says 6.
	for _, x := range readPathFixtures(t) {
		if x.Cmd == "GET_LED_EFFECT" {
			report := bytes.Clone(x.Responses[0])
			report[17] = 2 // brightness: data offset 9 = report offset 17
			x.Responses[0] = report
		}
		d.Replay(x)
	}
	enum := &hidfake.Enumerator{Devices: []*hidfake.Device{d}}

	if code, _, _ := run(t, enum, "save", "state.json"); code != 0 {
		t.Fatalf("save exit = %d, want 0", code)
	}
	code, out, _ := run(t, enum, "load", "state.json", "--i-know-what-im-doing")
	if code != 1 {
		t.Fatalf("exit = %d, want 1 (verification failed)\n%s", code, out)
	}
	for _, want := range []string{
		"read-back verification: 1 difference(s) (State File → Device)",
		"lighting brightness: 6 → 2",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

// State File validation is external behavior too: a file this build cannot
// trust is refused with a named reason and nothing is written.
func TestLoadRejectsUnknownSchemaAndMissingBlocks(t *testing.T) {
	t.Chdir(t.TempDir())
	d := newNut87Fake(t, "/dev/hidraw3")
	replayReadPath(d, t) // save
	replayReadPath(d, t) // load probe + read pass for the second case
	enum := &hidfake.Enumerator{Devices: []*hidfake.Device{d}}
	if code, _, _ := run(t, enum, "save", "state.json"); code != 0 {
		t.Fatalf("save exit = %d, want 0", code)
	}
	raw, err := os.ReadFile("state.json")
	if err != nil {
		t.Fatal(err)
	}

	newer := strings.Replace(string(raw), `"schema": 1`, `"schema": 2`, 1)
	if err := os.WriteFile("state.json", []byte(newer), 0o644); err != nil {
		t.Fatal(err)
	}
	code, _, errOut := run(t, enum, "load", "state.json", "--i-know-what-im-doing")
	if code != 1 {
		t.Fatalf("schema 2: exit = %d, want 1 (stderr: %s)", code, errOut)
	}
	if !strings.Contains(errOut, "newer nutctl") {
		t.Errorf("stderr missing the version explanation:\n%s", errOut)
	}

	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	delete(doc["state"].(map[string]any), "fn")
	noFn, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("state.json", noFn, 0o644); err != nil {
		t.Fatal(err)
	}
	code, _, errOut = run(t, enum, "load", "state.json", "--i-know-what-im-doing")
	if code != 1 {
		t.Fatalf("missing block: exit = %d, want 1 (stderr: %s)", code, errOut)
	}
	if !strings.Contains(errOut, "state.fn is missing") {
		t.Errorf("stderr missing the named block:\n%s", errOut)
	}
}

// Like `get`, `save` records what the Device reports even when the
// self-checks fail — and says so loudly.
func TestSaveWritesEvenWhenSelfChecksFail(t *testing.T) {
	t.Chdir(t.TempDir())
	bad := nut87Faulty(t, "/dev/hidraw3",
		fault{cmd: "GET_KEY", res: 9, off: 8 + 6, data: []byte{0x00}})
	replayReadPath(bad, t)
	enum := &hidfake.Enumerator{Devices: []*hidfake.Device{bad}}

	code, _, errOut := run(t, enum, "save", "state.json")
	if code != 1 {
		t.Fatalf("exit = %d, want 1 (checks failed)", code)
	}
	if !strings.Contains(errOut, "self-check failed:") {
		t.Errorf("stderr missing the self-check failure:\n%s", errOut)
	}
	if _, err := os.Stat("state.json"); err != nil {
		t.Errorf("State File not written: %v (the snapshot is exactly what a failing check needs)", err)
	}
}

func TestLoadNeedsAFilePath(t *testing.T) {
	d := nut87(t, "/dev/hidraw3")
	enum := &hidfake.Enumerator{Devices: []*hidfake.Device{d}}
	code, _, errOut := run(t, enum, "load")
	if code != 2 {
		t.Fatalf("exit = %d, want 2 (usage error)", code)
	}
	if !strings.Contains(errOut, "State File path") {
		t.Errorf("stderr = %q, want the missing path named", errOut)
	}
}

// The wire markers the SET format forces are never verified (they are not
// state): after a real write the Device reports the Per-Key ledId bytes as
// the entry index and the Lighting Effect's driverSetting/check code as
// 0xFF/0xAA 0x55 — exactly what was written. A read-back carrying those must
// verify clean; only state differences may surface. (This shape was observed
// on real hardware: firmware 1.20 reports ledIds as indexes after
// SET_CUSTOM_LED_DATA.)
func TestLoadVerificationIgnoresWireMarkers(t *testing.T) {
	t.Chdir(t.TempDir())
	d := newNut87Fake(t, "/dev/hidraw3")
	for i := 0; i < 2; i++ { // save and load's pre-write read pass
		for _, x := range readPathFixtures(t) {
			d.Replay(x)
		}
	}
	replaySets(d, t)
	// Read-back as a written Device reports it: ledIds = entry index, and
	// the Lighting Effect's forced markers visible.
	for _, x := range readPathFixtures(t) {
		switch x.Cmd {
		case "GET_CUSTOM_LED_DATA":
			r0 := bytes.Clone(x.Responses[0])
			r0[8+20] = 5 // entry 5's ledId: block byte 20 = chunk 0, report offset 28
			x.Responses[0] = r0
			r9 := bytes.Clone(x.Responses[9])
			r9[8+4] = 127 // entry 127's ledId: block byte 508 = chunk 9, report offset 12
			x.Responses[9] = r9
		case "GET_LED_EFFECT":
			r := bytes.Clone(x.Responses[0])
			r[8+4] = 0xFF  // driverSetting
			r[8+14] = 0xAA // check code
			r[8+15] = 0x55
			x.Responses[0] = r
		}
		d.Replay(x)
	}
	enum := &hidfake.Enumerator{Devices: []*hidfake.Device{d}}

	if code, _, _ := run(t, enum, "save", "state.json"); code != 0 {
		t.Fatalf("save exit = %d, want 0", code)
	}
	code, out, _ := run(t, enum, "load", "state.json", "--i-know-what-im-doing")
	if code != 0 {
		t.Fatalf("exit = %d, want 0 — wire markers are not state, verification must pass\n%s", code, out)
	}
	if !strings.Contains(out, "read-back verified: the Device matches the State File") {
		t.Errorf("output missing the clean verification:\n%s", out)
	}
}

// The file argument and the flags parse in either order, and bad input names
// the offending argument (the actionable-errors convention of spec user
// story 29) — never a bare count.
func TestSaveAndLoadAcceptFlagsAfterTheFile(t *testing.T) {
	d := newNut87Fake(t, "/dev/hidraw3")
	replayReadPath(d, t)
	enum := &hidfake.Enumerator{Devices: []*hidfake.Device{d}}

	path := filepath.Join(t.TempDir(), "state.json")
	if code, _, errOut := run(t, enum, "save", path, "--device", "/dev/hidraw3"); code != 0 {
		t.Fatalf("save exit = %d, want 0 (flags after the file) (stderr: %s)", code, errOut)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("State File not written: %v", err)
	}
}

func TestSaveNamesTheOffendingArgument(t *testing.T) {
	d := newNut87Fake(t, "/dev/hidraw3")
	enum := &hidfake.Enumerator{Devices: []*hidfake.Device{d}}
	code, _, errOut := run(t, enum, "save", "a.json", "b.json")
	if code != 2 {
		t.Fatalf("exit = %d, want 2 (usage error)", code)
	}
	if !strings.Contains(errOut, `"b.json"`) {
		t.Errorf("stderr = %q, want the offending argument named", errOut)
	}
}
