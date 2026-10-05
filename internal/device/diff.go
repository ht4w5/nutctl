package device

import (
	"fmt"

	"github.com/ht4w5/nutctl/internal/protocol"
)

// DiffHeader introduces the read-back verification diff — the one spelling
// of the line every UI shows above it (spec user story 22), naming what was
// sent to the Device ("State File", "your edits") before the arrow.
func DiffHeader(n int, from string) string {
	return fmt.Sprintf("read-back verification: %d difference(s) (%s → Device):", n, from)
}

// StateDiffs names every difference between two States in from → to order.
// The read-back verification diff (spec user story 22) is
// StateDiffs(sent, read-back); the pending-changes line of an edit screen is
// StateDiffs(Device state, local edits). Wire markers are never compared:
// the SET format forces them and a State File does not carry them
// (StateFile). Key Slots are named from the Model's layout table where it
// has a name.
func StateDiffs(from, to State, m Model) []string {
	var diffs []string
	add := func(format string, a ...any) { diffs = append(diffs, fmt.Sprintf(format, a...)) }

	name := func(slot int) string {
		if l, err := LayoutFor(m); err == nil {
			if n, ok := l.Name(slot); ok {
				return fmt.Sprintf("Key Slot %d (%s)", slot, n)
			}
		}
		return fmt.Sprintf("Key Slot %d", slot)
	}
	for _, layer := range []struct {
		label string
		from  protocol.Keymap
		to    protocol.Keymap
	}{{"base", from.Base, to.Base}, {"fn", from.Fn, to.Fn}} {
		for slot := range layer.from {
			if layer.from[slot].Raw == layer.to[slot].Raw {
				continue
			}
			add("%s %s: %s → %s", layer.label, name(slot),
				protocol.KeyActionText(layer.from[slot]), protocol.KeyActionText(layer.to[slot]))
		}
	}

	for _, f := range []struct {
		label string
		from  any
		to    any
	}{
		{"lighting mode", from.Lighting.Mode, to.Lighting.Mode},
		{"lighting primary color", protocol.HexRGB(from.Lighting.RGB[0], from.Lighting.RGB[1], from.Lighting.RGB[2]), protocol.HexRGB(to.Lighting.RGB[0], to.Lighting.RGB[1], to.Lighting.RGB[2])},
		{"lighting secondary color", protocol.HexRGB(from.Lighting.SecondaryRGB[0], from.Lighting.SecondaryRGB[1], from.Lighting.SecondaryRGB[2]), protocol.HexRGB(to.Lighting.SecondaryRGB[0], to.Lighting.SecondaryRGB[1], to.Lighting.SecondaryRGB[2])},
		{"lighting color mode", from.Lighting.ColorMode, to.Lighting.ColorMode},
		{"lighting brightness", from.Lighting.Brightness, to.Lighting.Brightness},
		{"lighting speed", from.Lighting.Speed, to.Lighting.Speed},
		{"lighting direction", from.Lighting.Direction, to.Lighting.Direction},
		{"lighting effect mode type", from.Lighting.EffectModeType, to.Lighting.EffectModeType},
	} {
		if f.from != f.to {
			add("%s: %v → %v", f.label, f.from, f.to)
		}
	}

	for i := range from.PerKey {
		w, g := from.PerKey[i], to.PerKey[i]
		// Colors only: the ledId byte is the entry index on the wire (derived
		// on write, StateFile) and is never state to compare.
		if w.R == g.R && w.G == g.G && w.B == g.B {
			continue
		}
		add("per-key RGB entry %d: %s → %s", i, protocol.HexRGB(w.R, w.G, w.B), protocol.HexRGB(g.R, g.G, g.B))
	}

	for _, f := range []struct {
		label string
		from  any
		to    any
	}{
		{"report rate", from.Settings.ReportRate, to.Settings.ReportRate},
		{"game mode", from.Settings.GameMode, to.Settings.GameMode},
		{"Fn switch", from.Settings.FnSwitch, to.Settings.FnSwitch},
		{"sleep time", from.Settings.SleepTime, to.Settings.SleepTime},
		{"key delay", from.Settings.KeyDelay, to.Settings.KeyDelay},
		{"system mode", from.Settings.SystemMode, to.Settings.SystemMode},
		{"TFT display time", from.Settings.TFTDisplayTime, to.Settings.TFTDisplayTime},
		{"top dead zone", from.Settings.TopDeadZone, to.Settings.TopDeadZone},
		{"bottom dead zone", from.Settings.BottomDeadZone, to.Settings.BottomDeadZone},
		{"stability mode", from.Settings.StabilityMode, to.Settings.StabilityMode},
		{"auto calibration", from.Settings.AutoCalibration, to.Settings.AutoCalibration},
		{"single key wakeup", from.Settings.SingleKeyWakeup, to.Settings.SingleKeyWakeup},
		{"push button mode", from.Settings.PushButtonMode, to.Settings.PushButtonMode},
		{"NKRO switch", from.Settings.NKROSwitch, to.Settings.NKROSwitch},
		{"wireless report rate", from.Settings.WirelessReportRate, to.Settings.WirelessReportRate},
		{"power mode", from.Settings.PowerMode, to.Settings.PowerMode},
	} {
		if f.from != f.to {
			add("settings %s: %v → %v", f.label, f.from, f.to)
		}
	}
	return diffs
}
