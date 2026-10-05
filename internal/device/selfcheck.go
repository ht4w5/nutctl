package device

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/ht4w5/nutctl/internal/protocol"
)

// selfCheckPrefix starts every failure line of the verification step, so a
// loud failure (exit 1, stderr) always names itself as a self-check. It is
// the one spelling of the prefix: checkFailure prepends it, and Verify
// (model.go) builds its already-prefixed line with it.
const selfCheckPrefix = "self-check failed: "

// CheckInput is everything the self-checks read (decoded blocks + identities).
type CheckInput struct {
	USB      Identity                // identity from USB enumeration
	Reported Identity                // identity from GET_DEVICE_INFO
	Base     protocol.Keymap         // GET_KEY
	Fn       protocol.Keymap         // GET_FN_KEY
	Lighting protocol.LightingEffect // GET_LED_EFFECT
}

// RunChecks runs the three protocol self-checks (ADR-0003, docs/capture.md
// Method A) as ONE reusable verification step over a full read of the Device
// state:
//
//  1. Device Identity match: the firmware-reported vendor/product ids match
//     the USB enumeration (Verify), the USB identity identifies the Model this
//     session is for (Identify), and that Model is one this build supports
//     (CheckSupported).
//  2. The keymap decodes to the layout: on BOTH Layers (Base, Fn) every Key
//     Slot carrying a non-DEFAULT Key Action is a KnownSlot the Model's
//     Layout knows (physical keys ∪ Knob gestures ∪ extraSlots), and each
//     keymap block ends with the 0xAA 0x55 block-tail marker at bytes
//     510..511 (see checkLayout).
//  3. The Lighting Effect check code at offsets 14..15 is 0xAA 0x55 (written
//     by the vendor app's SET path) or 0x00 0x00 (factory/unwritten).
//
// All three checks run every time; the result is nil, or one error naming
// EVERY failure, one "self-check failed: " line each. Never a partial pass: a
// check that cannot run (e.g. the Model has no layout table) fails loudly
// like a wrong result, and a failed check never hides the others. This step
// is reused verbatim by the `nutctl get` commands and by the write gate
// (reads before writes).
func RunChecks(model Model, in CheckInput) error {
	var failed []error
	for _, check := range []func(Model, CheckInput) error{checkIdentity, checkLayout, checkLighting} {
		if err := check(model, in); err != nil {
			failed = append(failed, err)
		}
	}
	return errors.Join(failed...)
}

// checkFailure is one failed self-check: exactly one error line starting
// with "self-check failed: " — selfCheckPrefix is prepended here, once (no
// reason passed to fail/failf carries it; Verify's already-prefixed line is
// surfaced verbatim, never wrapped). It unwraps to the underlying reason
// (e.g. a *WrongModelError), so callers can still inspect what kind of
// refusal this is.
type checkFailure struct{ reason error }

func (f *checkFailure) Error() string { return selfCheckPrefix + f.reason.Error() }

func (f *checkFailure) Unwrap() error { return f.reason }

func fail(reason error) error             { return &checkFailure{reason} }
func failf(format string, a ...any) error { return &checkFailure{fmt.Errorf(format, a...)} }

// checkIdentity is self-check 1 (docs/capture.md Method A: the response must
// be who lsusb says it is). The firmware-reported vendor/product ids must
// match the enumeration (Verify — its error is already a self-check failure
// line, so it is surfaced verbatim); the USB identity must identify the Model
// this session is for (Identify — never a best guess on a shared product id);
// and that Model must be one this build supports (CheckSupported — a
// sibling Model like the NUT75 is named and refused, never configured).
func checkIdentity(model Model, in CheckInput) error {
	if err := Verify(in.USB, in.Reported); err != nil {
		return err // already a self-check failure line (see Verify)
	}
	identified, err := Identify(in.USB)
	if err != nil {
		return fail(err)
	}
	if err := CheckSupported(identified); err != nil {
		return fail(err)
	}
	if identified.Name != model.Name {
		return fail(&WrongModelError{
			Identity: in.USB,
			Message: fmt.Sprintf(
				"wrong Model: the device identifies as %s (USB %s %q), not the %s this session is for — refusing to trust this session",
				identified.Name, in.USB.USBID(), in.USB.ProductName, model.Name),
		})
	}
	return nil
}

// checkLayout is self-check 2 (docs/capture.md Method A: "GET_KEY/GET_FN_KEY
// must decode to the physical layout — a layout mismatch instantly reveals a
// framing/off-by-one bug"), calibrated to hardware evidence (2026-10, real
// NUT87, firmware 1.20). It runs two detectors:
//
// (a) On BOTH Layers (Base and Fn) every Key Slot carrying a non-DEFAULT Key
// Action must be a KnownSlot the Model's Layout knows: physical keys ∪ Knob
// gestures ∪ extraSlots.
//
// (b) The block-tail marker: each keymap block ends with 0x00 0x00 0xAA 0x55
// — bytes 510..511 are 0xAA 0x55 (observed on firmware 1.20; the same marker
// sits at the Per-Key RGB block tail). Anything else names the observed
// bytes and fails: a framing/off-by-one bug shifts the tail away.
//
// The check is structural on purpose — it looks at WHERE bindings sit, not
// at which Key Actions they carry. An off-by-one/framing bug puts bindings
// in Key Slots the layout does not have; a remapped Device keeps its
// bindings within the layout, so a remapped Device still passes and is never
// locked out of the verification step. The evidence that calibrated this:
// 22 out-of-layout Key Slots (the layout's "gaps" plus 109..111) carry real
// default Key Actions on BOTH Layers of every healthy firmware-1.20 Device —
// a shared firmware matrix with sibling Models (bindings for keys the NUT87
// does not physically have), normal state, not corruption. Those are the
// data-only extraSlots of the Model's layout table, accepted by (a) with a
// loud false positive avoided. The rule still covers every non-DEFAULT Key
// Action, INCLUDING an explicit unknown page-type marker
// (protocol.ActionUnknown — a decoded, raw-preserving marker for a page type
// this build does not know, not a decode failure): a marker in a KnownSlot is
// just a binding this build cannot name, but a marker anywhere else is
// exactly the misalignment symptom this check exists to catch. A check that
// cannot run at all (no layout table for the Model) fails loudly: the keymap
// cannot be shown to decode to the layout, so the step must not pass.
func checkLayout(model Model, in CheckInput) error {
	layout, err := LayoutFor(model)
	if err != nil {
		return fail(err)
	}
	var failures []error

	// (a) every non-DEFAULT Key Action sits in a KnownSlot.
	var details []string
	for _, layer := range []struct {
		name   string
		keymap protocol.Keymap
	}{{"base", in.Base}, {"fn", in.Fn}} {
		var slots []int
		for slot, action := range layer.keymap {
			if action.Type == protocol.ActionDefault {
				continue // unbound: nothing to place
			}
			if !layout.HasKnownSlot(slot) {
				slots = append(slots, slot)
			}
		}
		if len(slots) > 0 {
			details = append(details, layer.name+" layer "+slotList(slots))
		}
	}
	if len(details) > 0 {
		failures = append(failures, failf(
			"keymap does not decode to the %s layout: Key Actions sit in %s, but no physical key, Knob gesture, or firmware-matrix slot sits there — the read is misaligned or the block is corrupt",
			model.Name, strings.Join(details, "; ")))
	}

	// (b) the block-tail marker: bytes 510..511 of each keymap block.
	var tails []string
	for _, block := range []struct {
		name   string
		keymap protocol.Keymap
	}{{"GET_KEY", in.Base}, {"GET_FN_KEY", in.Fn}} {
		tail := block.keymap[127].Raw // block bytes 508..511
		if tail[2] != protocol.CheckCodeByte0 || tail[3] != protocol.CheckCodeByte1 {
			tails = append(tails, fmt.Sprintf("%s bytes 510..511 are 0x%02X 0x%02X", block.name, tail[2], tail[3]))
		}
	}
	if len(tails) > 0 {
		failures = append(failures, failf(
			"keymap block misalignment: %s, want 0x%X 0x%X (the block-tail marker of a well-aligned read) — the read is misaligned or the block is corrupt",
			strings.Join(tails, "; "), protocol.CheckCodeByte0, protocol.CheckCodeByte1))
	}
	return errors.Join(failures...)
}

// checkLighting is self-check 3 (docs/capture.md Method A: "the check-code
// bytes must be 0xAA 0x55 at offsets 14..15 — an accidental
// endianness/offset detector built into the protocol"), calibrated to
// hardware evidence (2026-10, real NUT87, firmware 1.20): offsets 14..15
// pass when they read 0xAA 0x55 (written by the vendor app's SET_LED_EFFECT
// path) OR 0x00 0x00 (factory/unwritten — the observed state of a Device
// that has never been written; GET_LED_EFFECT on this firmware reads
// 0x00 0x00 there). Any other pair is corruption or misalignment and fails
// loudly, naming the observed bytes.
//
// The open question this observation raises closes with ticket 03's
// SET_LED_EFFECT read-back: once the tool writes a Lighting Effect, the
// read-back must show 0xAA 0x55 at offsets 14..15.
func checkLighting(_ Model, in CheckInput) error {
	checkCode := in.Lighting.CheckCode
	if checkCode == [2]byte{protocol.CheckCodeByte0, protocol.CheckCodeByte1} ||
		checkCode == [2]byte{0, 0} {
		return nil
	}
	return failf(
		"lighting check code is 0x%02X 0x%02X at offsets 14..15, want 0x%X 0x%X (written by the vendor app's SET path) or 0x00 0x00 (factory/unwritten) — the read is misaligned or the block is corrupt",
		checkCode[0], checkCode[1], protocol.CheckCodeByte0, protocol.CheckCodeByte1)
}

// slotList renders Key Slot ids for a failure line: "Key Slot 29" or
// "Key Slots 29, 120".
func slotList(slots []int) string {
	ids := make([]string, len(slots))
	for i, s := range slots {
		ids[i] = strconv.Itoa(s)
	}
	word := "Key Slots"
	if len(slots) == 1 {
		word = "Key Slot"
	}
	return word + " " + strings.Join(ids, ", ")
}
