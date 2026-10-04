// Package device models the Device side of the domain: which Model a
// connected Device is (Device Identity → Model), and the checks that protect
// later writes. Wire types live in internal/protocol; this package is the
// vocabulary of CONTEXT.md.
package device

import (
	"fmt"
	"strings"
)

// Connection is how a Device is reached (CONTEXT.md). The NUT87 is USB; it
// has no Bluetooth.
type Connection string

const (
	ConnectionUSB Connection = "USB"
	Connection24G Connection = "2.4G"
)

// Identity is the Device Identity (CONTEXT.md): the tuple that determines
// which Model a connected device is. ProductName (the USB product string) is
// decisive — Models share USB product ids. Manufacturer/Product are the
// firmware-reported u16 fields from GET_DEVICE_INFO.
type Identity struct {
	VendorID     uint16
	ProductID    uint16
	ProductName  string
	Manufacturer uint16
	Product      uint16
}

// USBID renders the vendor/product id pair the way lsusb does.
func (id Identity) USBID() string {
	return fmt.Sprintf("%04x:%04x", id.VendorID, id.ProductID)
}

// Model is a keyboard product (CONTEXT.md). New Models are data: identity
// plus, later, a layout table and capability set.
type Model struct {
	Name        string
	Connection  Connection
	VendorID    uint16
	ProductID   uint16
	ProductName string // exact match on the USB product string
	Supported   bool   // false for known sibling Models: identified, never configured
}

// Known Models. NUT75 shares the NUT87's USB product id; it is recognized so
// the refusal can name it, but it is not supported.
var (
	NUT87 = Model{
		Name: "NUT87", Connection: ConnectionUSB,
		VendorID: 0x0C45, ProductID: 0x880C, ProductName: "NUT87",
		Supported: true,
	}
	NUT75 = Model{
		Name: "NUT75", Connection: ConnectionUSB,
		VendorID: 0x0C45, ProductID: 0x880C, ProductName: "NUT75",
		Supported: false,
	}

	knownModels = []Model{NUT87, NUT75}
)

// Identify returns the Model the Identity belongs to. An Identity sharing a
// USB product id with a known Model but carrying another product name is a
// wrong-Model error — never a best guess.
func Identify(id Identity) (Model, error) {
	var sameID []Model
	for _, m := range knownModels {
		if m.VendorID == id.VendorID && m.ProductID == id.ProductID {
			sameID = append(sameID, m)
			if m.ProductName == id.ProductName {
				return m, nil
			}
		}
	}
	if len(sameID) > 0 {
		names := make([]string, len(sameID))
		for i, m := range sameID {
			names[i] = m.Name
		}
		return Model{}, &WrongModelError{
			Identity: id,
			Message: fmt.Sprintf(
				"wrong Model: device identifies as %q (USB %s), not any of %s; this USB product id is shared by %s",
				id.ProductName, id.USBID(), strings.Join(quoteAll(names), ", "), strings.Join(names, ", ")),
		}
	}
	return Model{}, fmt.Errorf("unknown device: USB %s %q is not a keyboard this tool knows", id.USBID(), id.ProductName)
}

// CheckSupported refuses Models this tool must not configure (wrong-Model
// error, never a silent best effort).
func CheckSupported(m Model) error {
	if m.Supported {
		return nil
	}
	supported := make([]string, 0, len(knownModels))
	for _, k := range knownModels {
		if k.Supported {
			supported = append(supported, k.Name)
		}
	}
	return &WrongModelError{
		Message: fmt.Sprintf(
			"wrong Model: this is a %s (USB %04x:%04x %q); this build only supports %s — refusing to configure",
			m.Name, m.VendorID, m.ProductID, m.ProductName, strings.Join(supported, ", ")),
	}
}

// WrongModelError is the clear refusal for a Device that is not a supported
// Model. It is returned before anything is written to the Device.
type WrongModelError struct {
	Identity Identity
	Message  string
}

func (e *WrongModelError) Error() string { return e.Message }

// Verify cross-checks the firmware-reported identity against the identity the
// transport enumerated (the self-check of docs/capture.md Method A: a
// mismatch means the framing is wrong or the wire is not who it claims).
func Verify(usb Identity, reported Identity) error {
	if usb.VendorID != reported.VendorID || usb.ProductID != reported.ProductID {
		return fmt.Errorf(
			"self-check failed: USB reports %s %q but the firmware reports %s — refusing to trust this session",
			usb.USBID(), usb.ProductName, reported.USBID())
	}
	return nil
}

func quoteAll(ss []string) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = fmt.Sprintf("%q", s)
	}
	return out
}
