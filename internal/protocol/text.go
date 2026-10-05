package protocol

import "fmt"

// This file is the one spelling of how wire values are rendered for humans.
// The CLI and the TUI both use these, so a Key Action or a firmware status
// never reads two ways.

// KeyActionText renders a Key Action for humans: the page type with its
// params, e.g. "KEYBOARD(00 29 00)"; DEFAULT renders bare. An unknown page
// type is the explicit marker with all four wire bytes, e.g.
// "UNKNOWN(2a 01 02 03)" — never dropped, never a crash.
func KeyActionText(a KeyAction) string {
	switch a.Type {
	case ActionDefault:
		return a.Type.String()
	case ActionUnknown:
		return fmt.Sprintf("UNKNOWN(%02x %02x %02x %02x)",
			a.Raw[0], a.Raw[1], a.Raw[2], a.Raw[3])
	default:
		return fmt.Sprintf("%s(%02x %02x %02x)", a.Type, a.Params[0], a.Params[1], a.Params[2])
	}
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

// HexRGB renders a color as "#rrggbb".
func HexRGB(r, g, b byte) string {
	return fmt.Sprintf("#%02x%02x%02x", r, g, b)
}
