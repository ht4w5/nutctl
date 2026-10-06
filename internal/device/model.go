// Package device models the Device side of the domain: which Model a
// connected Device is (Device Identity → Model), and the checks that protect
// later writes. Wire types live in internal/protocol; this package is the
// vocabulary of CONTEXT.md.
package device

import (
	"fmt"
	"strings"

	"github.com/ht4w5/nutctl/internal/protocol"
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

// Model is a keyboard product (CONTEXT.md). New Models are data: identity,
// a layout table (internal/device/layouts) and a capability set.
type Model struct {
	Name        string
	Connection  Connection
	VendorID    uint16
	ProductID   uint16
	ProductName string // exact match on the USB product string
	Supported   bool   // false for known sibling Models: identified, never configured
	// ReportRates is the capability set for the Settings block's Report Rate
	// (CONTEXT.md: the NUT87 offers 1K/4K/8K): the wire values this Model's
	// firmware accepts, in ascending order — what an editor may offer. The
	// vendor config carries the same list (settingsConfig.reportRateList,
	// in Hz), so this is data observed, not a guess.
	ReportRates []protocol.ReportRate
	// Lighting is the capability set for the Lighting Effect block
	// (CONTEXT.md): the effect modes this Model's firmware offers and the
	// brightness/speed ranges its vendor config advertises — what an editor
	// may offer. The vendor config carries the same values
	// (lightingConfig.customEffect, minBrightness/maxBrightness,
	// minSpeed/maxSpeed), so this is data observed, not a guess.
	Lighting LightingCapability
}

// LightingCapability is what a Model advertises for the Lighting Effect
// block: the effect modes it offers, in the vendor's display order, and the
// ranges brightness and speed may take (the acceptance's "constrained to
// the Model's advertised ranges").
type LightingCapability struct {
	Modes                        []protocol.LightingMode
	BrightnessMin, BrightnessMax uint8
	SpeedMin, SpeedMax           uint8
}

// Known Models. NUT75 shares the NUT87's USB product id; it is recognized so
// the refusal can name it, but it is not supported.
var (
	NUT87 = Model{
		Name: "NUT87", Connection: ConnectionUSB,
		VendorID: 0x0C45, ProductID: 0x880C, ProductName: "NUT87",
		Supported: true,
		ReportRates: []protocol.ReportRate{
			protocol.ReportRate1K, protocol.ReportRate4K, protocol.ReportRate8K,
		},
		// The vendor config 3141-34828-NUT87.ts carries lightingConfig:
		// customEffect [23, 24, 25] (the appended effect modes),
		// minBrightness 1, maxBrightness 6, minSpeed 1, maxSpeed 6.
		Lighting: LightingCapability{
			Modes:         protocol.LightingModes(23, 24, 25),
			BrightnessMin: 1,
			BrightnessMax: 6,
			SpeedMin:      1,
			SpeedMax:      6,
		},
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

// ModelByName returns the Model with this name (CONTEXT.md) — the lookup
// `nutctl fixtures import-pcap --model` uses, since a kernel capture carries
// no USB product strings to identify a Device by. An unknown name is a clear
// refusal listing the Models this build knows, never a best guess.
func ModelByName(name string) (Model, error) {
	for _, m := range knownModels {
		if m.Name == name {
			return m, nil
		}
	}
	names := make([]string, len(knownModels))
	for i, m := range knownModels {
		names[i] = m.Name
	}
	return Model{}, fmt.Errorf("unknown Model %q; this build knows %s", name, strings.Join(quoteAll(names), ", "))
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
// mismatch means the framing is wrong or the wire is not who it claims). Its
// failure line is already a self-check failure line (selfCheckPrefix): callers
// surface it verbatim, never re-prefix it.
func Verify(usb Identity, reported Identity) error {
	if usb.VendorID != reported.VendorID || usb.ProductID != reported.ProductID {
		return fmt.Errorf(
			selfCheckPrefix+"USB reports %s %q but the firmware reports %s — refusing to trust this session",
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
