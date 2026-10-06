package device

import (
	"strings"
	"testing"
)

func TestIdentifyNUT87(t *testing.T) {
	m, err := Identify(Identity{VendorID: 0x0C45, ProductID: 0x880C, ProductName: "NUT87"})
	if err != nil {
		t.Fatalf("Identify: %v", err)
	}
	if m.Name != "NUT87" || !m.Supported {
		t.Errorf("Identify = %+v, want NUT87 supported", m)
	}
	if err := CheckSupported(m); err != nil {
		t.Errorf("CheckSupported(NUT87) = %v, want nil", err)
	}
}

func TestIdentifyNUT75IsRefused(t *testing.T) {
	// A sibling Model sharing the USB product id must be identified for what
	// it is and refused with a clear wrong-Model error.
	m, err := Identify(Identity{VendorID: 0x0C45, ProductID: 0x880C, ProductName: "NUT75"})
	if err != nil {
		t.Fatalf("Identify: %v", err)
	}
	if m.Name != "NUT75" {
		t.Fatalf("Identify name = %q, want NUT75", m.Name)
	}
	err = CheckSupported(m)
	if err == nil {
		t.Fatal("CheckSupported(NUT75) = nil, want wrong-Model error")
	}
	msg := err.Error()
	for _, want := range []string{"wrong Model", "NUT75", "NUT87"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q does not mention %q", msg, want)
		}
	}
}

func TestIdentifyUnknownNameOnSharedProductID(t *testing.T) {
	_, err := Identify(Identity{VendorID: 0x0C45, ProductID: 0x880C, ProductName: "SOMETHING"})
	if err == nil {
		t.Fatal("Identify succeeded, want wrong-Model error")
	}
	msg := err.Error()
	for _, want := range []string{"wrong Model", "SOMETHING", "NUT87", "NUT75"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q does not mention %q", msg, want)
		}
	}
}

func TestIdentifyUnknownDevice(t *testing.T) {
	_, err := Identify(Identity{VendorID: 0x1234, ProductID: 0x5678, ProductName: "Whatever"})
	if err == nil {
		t.Fatal("Identify succeeded, want unknown-device error")
	}
	if !strings.Contains(err.Error(), "unknown device") {
		t.Errorf("error %q does not say the device is unknown", err)
	}
}

func TestVerifyIdentityCatchesProtocolMismatch(t *testing.T) {
	// The firmware must report the same USB ids the transport enumerated;
	// a mismatch means we are not talking to the Device we think we are.
	usb := Identity{VendorID: 0x0C45, ProductID: 0x880C, ProductName: "NUT87"}
	if err := Verify(usb, Identity{VendorID: 0x0C45, ProductID: 0x880C}); err != nil {
		t.Errorf("Verify matching ids = %v, want nil", err)
	}
	err := Verify(usb, Identity{VendorID: 0x0C45, ProductID: 0xFEF9})
	if err == nil {
		t.Fatal("Verify mismatched pid = nil, want error")
	}
	if !strings.Contains(err.Error(), "self-check") {
		t.Errorf("error %q does not mention the self-check", err)
	}
}

// ModelByName is the `--model` lookup of `nutctl fixtures import-pcap`
// (docs/capture.md Method B): a kernel capture carries no USB product strings
// to identify a Device by, so the Model is named by the operator and
// cross-checked against the capture's own GET_DEVICE_INFO.
func TestModelByName(t *testing.T) {
	m, err := ModelByName("NUT87")
	if err != nil {
		t.Fatalf("ModelByName(NUT87): %v", err)
	}
	if m.Name != "NUT87" || m.Connection != ConnectionUSB {
		t.Errorf("ModelByName(NUT87) = %+v, want the NUT87 over USB", m)
	}
	// Known sibling Models are data too: the lookup finds them (and
	// CheckSupported is what refuses them later).
	if m, err := ModelByName("NUT75"); err != nil || m.Name != "NUT75" {
		t.Errorf("ModelByName(NUT75) = %+v, %v; want the NUT75", m, err)
	}
	// An unknown name is a clear refusal naming the Models this build knows.
	if _, err := ModelByName("NUT60"); err == nil {
		t.Error("ModelByName(NUT60) accepted, want a refusal")
	} else if !strings.Contains(err.Error(), "NUT87") {
		t.Errorf("ModelByName(NUT60) error %q does not name the known Models", err)
	}
}
