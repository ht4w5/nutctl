package protocol

import "fmt"

// This file is the one spelling of how wire values are rendered for humans.
// The CLI and the TUI both use these, so a Key Action or a firmware status
// never reads two ways.

// KeyActionText renders a Key Action for humans. A Key Action the catalog
// names (by its exact wire bytes) renders as that name with the kind of Key
// Action CONTEXT.md calls it — `keyboard key "Esc"`, `consumer key
// "Volume +"`, `mouse button "Left mouse button"`, `Function "Restore
// Factory Settings"` — the words the rebind picker offers it under
// (KeyActionName). Anything else renders as its page type with its
// raw params, e.g. "KEYBOARD(00 29 00)"; DEFAULT renders bare. An unknown
// page type is the explicit marker with all four wire bytes, e.g.
// "UNKNOWN(2a 01 02 03)" — never dropped, never a crash. Naming is exact:
// bytes the catalog does not carry stay visible as bytes.
func KeyActionText(a KeyAction) string {
	switch a.Type {
	case ActionDefault:
		return a.Type.String()
	case ActionUnknown:
		return fmt.Sprintf("UNKNOWN(%02x %02x %02x %02x)",
			a.Raw[0], a.Raw[1], a.Raw[2], a.Raw[3])
	}
	if name := KeyActionName(a); name != "" {
		return fmt.Sprintf("%s %q", kindOf(a.Type), name)
	}
	return fmt.Sprintf("%s(%02x %02x %02x)", a.Type, a.Params[0], a.Params[1], a.Params[2])
}

// FirmwareStatusText renders the firmware status (GET_DEVICE_INFO,
// docs/protocol.md §4) for humans. The bootloader state names its
// consequence: writes are refused there (ADR-0003).
func FirmwareStatusText(status uint8) string {
	switch status {
	case FirmwareOK:
		return "ok"
	case FirmwareBootloader:
		return "bootloader (writes will be refused)"
	default:
		return fmt.Sprintf("unknown(%d)", status)
	}
}

// LightingModeText renders an effect mode of the Lighting Effect block for
// humans: its wire value and the name the bundle's effect table gives it
// (LightingModeFor) — `11 Flowing with the Waves`. A value no table names
// keeps its raw form, marked: naming is exact, bytes stay visible.
func LightingModeText(value uint8) string {
	if value == LightingOff {
		// No table entry names it — the bundle's lighting switch simply
		// writes mode 0 to turn the backlight off (docs/protocol.md §4).
		// The meaning is observed; the word is ours.
		return "0 off"
	}
	if m, ok := LightingModeFor(value); ok {
		return fmt.Sprintf("%d %s", value, m.Name)
	}
	return fmt.Sprintf("%d (unknown mode)", value)
}

// DirectionText renders the Lighting Effect's direction byte for humans:
// the arrow the bundle's own direction buttons write for it
// (docs/protocol.md §4). A value outside those four keeps its raw form.
func DirectionText(v uint8) string {
	switch v {
	case DirectionRight:
		return "right"
	case DirectionLeft:
		return "left"
	case DirectionUp:
		return "up"
	case DirectionDown:
		return "down"
	default:
		return fmt.Sprintf("unknown(%d)", v)
	}
}

// RangeText renders a ranged wire value for humans — the one spelling the
// CLI and the TUI share (`6 (range 1-6)`): the value beside the range the
// Model advertises for it (device.Model.Lighting).
func RangeText(v, min, max uint8) string {
	return fmt.Sprintf("%d (range %d-%d)", v, min, max)
}

// HexRGB renders a color as "#rrggbb".
func HexRGB(r, g, b byte) string {
	return fmt.Sprintf("#%02x%02x%02x", r, g, b)
}
