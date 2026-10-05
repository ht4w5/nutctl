package device

import (
	"context"
	"errors"
	"fmt"

	"github.com/ht4w5/nutctl/internal/hid"
	"github.com/ht4w5/nutctl/internal/protocol"
)

// Session is one open, identified connection to one Device (PLAN: "session
// semantics on top" of the protocol layer): the enumerated identity, the
// Model it was matched to, the protocol session and the firmware's identity
// block. Every path that reaches the wire — CLI commands and TUI actions
// alike — goes through one, so device selection, probing and the read pass
// are spelled once.

type Session struct {
	Info       hid.Info
	Model      Model
	Dev        *protocol.Device
	DeviceInfo protocol.DeviceInfo
}

// acceptedUsagePages are the HID collection usage pages the vendor protocol
// speaks on (docs/protocol.md §1). Interfaces without one of these cannot be
// connection candidates.
var acceptedUsagePages = map[uint16]bool{
	0xFF68: true,
	0xFF80: true,
	0xFF60: true,
	0xFF00: true,
	0xFF01: true,
	0xFF1B: true,
}

// IsCandidate says whether an enumerated interface could be a Device: it
// speaks on one of the protocol's usage pages.
func IsCandidate(info hid.Info) bool {
	return acceptedUsagePages[info.UsagePage]
}

// Select picks the Device to work with: an explicit path, or the single
// supported candidate. Ambiguity and refusals are errors — never a guess.
func Select(infos []hid.Info, selector string) (hid.Info, Model, error) {
	type candidate struct {
		info  hid.Info
		model Model
	}
	var supported []candidate
	var rejected error // a concrete refusal (wrong Model / unknown device), if any
	for _, info := range infos {
		if !IsCandidate(info) {
			continue
		}
		if selector != "" && info.Path != selector {
			continue
		}
		m, err := Identify(IdentityFromUSB(info))
		if err != nil {
			if selector != "" {
				return hid.Info{}, Model{}, err
			}
			// Prefer the specific wrong-Model refusal over a generic "none found".
			if rejected == nil {
				rejected = err
			}
			continue
		}
		if err := CheckSupported(m); err != nil {
			if selector != "" {
				return hid.Info{}, Model{}, err
			}
			var wrong *WrongModelError
			if errors.As(err, &wrong) && rejected == nil {
				rejected = err
			}
			continue
		}
		supported = append(supported, candidate{info: info, model: m})
	}

	if selector != "" {
		if len(supported) == 0 {
			return hid.Info{}, Model{}, fmt.Errorf("no device at %s", selector)
		}
		return supported[0].info, supported[0].model, nil
	}
	switch len(supported) {
	case 0:
		if rejected != nil {
			return hid.Info{}, Model{}, rejected
		}
		return hid.Info{}, Model{}, fmt.Errorf("no supported device found (looking for a NUT87)\n%s", hid.UdevHint)
	case 1:
		return supported[0].info, supported[0].model, nil
	default:
		paths := ""
		for i, c := range supported {
			if i > 0 {
				paths += ", "
			}
			paths += c.info.Path
		}
		return hid.Info{}, Model{}, fmt.Errorf(
			"multiple supported devices connected (%s); select one with --device PATH (CLI), or unplug the others", paths)
	}
}

// Open identifies the selected Device, opens it and proves the protocol
// speaks on it. The caller closes the Session.
func Open(e hid.Enumerator, selector string) (*Session, error) {
	infos, err := e.Enumerate()
	if err != nil {
		return nil, fmt.Errorf("enumerate devices: %w", err)
	}
	info, m, err := Select(infos, selector)
	if err != nil {
		return nil, err
	}
	return OpenInfo(e, info, m)
}

// OpenInfo opens one identified candidate and verifies it against its own
// firmware report (the self-check of docs/capture.md Method A: the firmware
// must report the USB ids the transport enumerated). The caller closes the
// Session.
func OpenInfo(e hid.Enumerator, info hid.Info, m Model) (*Session, error) {
	tr, err := e.Open(info)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", info.Path, err)
	}
	dev, err := protocol.Open(tr, protocol.Options{})
	if err != nil {
		tr.Close()
		return nil, err
	}
	di, err := dev.Info(context.Background())
	if err != nil {
		dev.Close()
		return nil, fmt.Errorf("probe %s: %w", info.Path, err)
	}
	if err := Verify(IdentityFromUSB(info), IdentityFromFirmware(di)); err != nil {
		dev.Close()
		return nil, err
	}
	return &Session{Info: info, Model: m, Dev: dev, DeviceInfo: di}, nil
}

// Close ends the session and releases the Device.
func (s *Session) Close() error { return s.Dev.Close() }

// Read makes ONE full read pass over the Session (docs/protocol.md §3 init
// sequence): GET_KEY, GET_FN_KEY, GET_LED_EFFECT, GET_CUSTOM_LED_DATA,
// GET_GAME_MODE.
func (s *Session) Read() (State, error) {
	ctx := context.Background()
	st := State{}
	var err error
	// The block names come from the protocol layer's errors (they already say
	// "GET_KEY: …" and friends); here only the probe path is added — the
	// established OpenInfo pattern, never a doubled prefix.
	if st.Base, err = s.Dev.Keymap(ctx); err != nil {
		return State{}, fmt.Errorf("probe %s: %w", s.Info.Path, err)
	}
	if st.Fn, err = s.Dev.FnKeymap(ctx); err != nil {
		return State{}, fmt.Errorf("probe %s: %w", s.Info.Path, err)
	}
	if st.Lighting, err = s.Dev.LightingEffect(ctx); err != nil {
		return State{}, fmt.Errorf("probe %s: %w", s.Info.Path, err)
	}
	if st.PerKey, err = s.Dev.PerKeyRGB(ctx); err != nil {
		return State{}, fmt.Errorf("probe %s: %w", s.Info.Path, err)
	}
	if st.Settings, err = s.Dev.Settings(ctx); err != nil {
		return State{}, fmt.Errorf("probe %s: %w", s.Info.Path, err)
	}
	return st, nil
}

// ReadChecked makes ONE full read pass and runs RunChecks ONCE over it
// (ADR-0003) — the shared verification step of every get/save/load and the
// write gate's check half. A failed self-check comes back as *CheckError
// ALONGSIDE the state: the checks gate WRITES, not reads, and the state is
// exactly what a failing check needs for diagnosis.
func (s *Session) ReadChecked() (State, error) {
	st, err := s.Read()
	if err != nil {
		return State{}, err
	}
	if err := RunChecks(s.Model, CheckInput{
		USB:      IdentityFromUSB(s.Info),
		Reported: IdentityFromFirmware(s.DeviceInfo),
		Base:     st.Base,
		Fn:       st.Fn,
		Lighting: st.Lighting,
	}); err != nil {
		return st, &CheckError{Err: err}
	}
	return st, nil
}

// CheckError wraps a RunChecks failure. Its lines already name themselves
// (each starts with "self-check failed: "), so callers surface them verbatim
// instead of re-prefixing them as a generic error.
type CheckError struct{ Err error }

func (e *CheckError) Error() string { return e.Err.Error() }
func (e *CheckError) Unwrap() error { return e.Err }

// IdentityFromUSB is the Device Identity as seen at enumeration (CONTEXT.md):
// the USB ids and the product-name string the Model is matched on. The USB
// product string is what the vendor bundle calls `productName` and matches
// config names with; GET_DEVICE_INFO carries no name fields.
func IdentityFromUSB(info hid.Info) Identity {
	return Identity{
		VendorID:    info.VendorID,
		ProductID:   info.ProductID,
		ProductName: info.ProductName,
	}
}

// IdentityFromFirmware is the identity the firmware reports in
// GET_DEVICE_INFO: the vendor/product ids and the u16 manufacturer/product
// fields. GET_DEVICE_INFO carries no name strings (docs/protocol.md §4), so
// the product-name half of the Device Identity can only be matched from USB
// enumeration (see IdentityFromUSB).
func IdentityFromFirmware(di protocol.DeviceInfo) Identity {
	return Identity{
		VendorID:     di.VID,
		ProductID:    di.PID,
		Manufacturer: di.Manufacturer,
		Product:      di.Product,
	}
}
