package cli

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/ht4w5/nutctl/internal/fixture"
	"github.com/ht4w5/nutctl/internal/hid"
	"github.com/ht4w5/nutctl/internal/hidfake"
)

const fixturesDir = "../../testdata/captures"

func loadFixture(t *testing.T, cmd, name string) fixture.Exchange {
	t.Helper()
	x, err := fixture.Load(fixturesDir+"/"+cmd, name)
	if err != nil {
		t.Fatalf("load fixture %s/%s: %v", cmd, name, err)
	}
	return x
}

// nut87 returns a scripted fake NUT87 that answers the v0 read path from the
// fixtures recorded off real hardware (64-byte reports).
func nut87(t *testing.T, path string) *hidfake.Device {
	t.Helper()
	d := hidfake.New(hid.Info{
		Path:         path,
		VendorID:     0x0C45,
		ProductID:    0x880C,
		ProductName:  "NUT87",
		Manufacturer: "hfdic",
		UsagePage:    0xFF68,
		ReportLength: 64,
	})
	d.Replay(loadFixture(t, "get_device_info", "nut87"))
	d.Replay(loadFixture(t, "get_game_mode", "nut87"))
	return d
}

func run(t *testing.T, enum hid.Enumerator, args ...string) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := Run(args, Deps{Devices: enum, Stdout: &out, Stderr: &errOut})
	return code, out.String(), errOut.String()
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
	// A sibling board sharing the USB product id must be refused with a clear
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
