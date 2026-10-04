// Package hid defines the Transport seam between the protocol layer and HID
// devices, plus the pure-Go hidraw adapter (ADR-0006: no cgo anywhere).
//
// Everything above this seam runs against any Transport implementation; the
// hidraw adapter is the real one, and internal/hidfake is the scripted one used
// by tests and offline CLI work.
package hid

import "errors"

// Transport is one open HID interface. It carries raw input/output reports;
// framing and protocol semantics live in internal/protocol.
type Transport interface {
	// ReportLength is the output report length in bytes as reported by the HID
	// report descriptor. Chunking must use this value, never a hardcoded 32.
	ReportLength() int
	// SendReport writes one output report. id is the HID report id (0 for the
	// unnumbered output report used by the Device).
	SendReport(id uint8, report []byte) error
	// Reports delivers input reports as they arrive. The channel is closed by
	// Close (or when the device disappears).
	Reports() <-chan []byte
	// Close releases the device. It is safe to call more than once.
	Close() error
}

// Info describes one enumerated HID interface: a connection candidate.
type Info struct {
	Path         string // OS path, e.g. /dev/hidraw3
	VendorID     uint16
	ProductID    uint16
	ProductName  string // USB product string (iProduct) — decisive for the Model
	Manufacturer string // USB manufacturer string (iManufacturer)
	UsagePage    uint16 // usage page of the first HID collection, 0 if unknown
	ReportLength int    // output report length from the report descriptor
	Numbered     bool   // input/output reports carry a report id prefix
}

// Enumerator finds connection candidates and opens Transports for them.
type Enumerator interface {
	Enumerate() ([]Info, error)
	Open(Info) (Transport, error)
}

// Sentinel errors adapters wrap so callers can give actionable messages.
var (
	// ErrPermission: the OS refused access (usually missing udev rule).
	ErrPermission = errors.New("permission denied opening hidraw device")
	// ErrBusy: another process holds the device.
	ErrBusy = errors.New("device busy")
	// ErrNotFound: the enumerated device vanished before it could be opened.
	ErrNotFound = errors.New("device not found")
)
