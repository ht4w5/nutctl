package device

import (
	"fmt"

	"github.com/ht4w5/nutctl/internal/protocol"
)

// DiffHeader introduces the read-back verification diff — the one spelling
// of the line every UI shows above it (spec user story 22).
func DiffHeader(n int) string {
	return fmt.Sprintf("read-back verification: %d difference(s) (State File → Device):", n)
}

// StateDiffs names every difference between a wanted State (a State File
// being loaded) and the Device's read-back (got) — the read-back
// verification diff every apply shows (spec user story 22). Wire markers are
// never compared: the SET format forces them and the State File does not
// carry them (StateFile). Key Slots are named from the Model's layout table
// where it has a name.
func StateDiffs(want, got State, m Model) []string {
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
		want  protocol.Keymap
		got   protocol.Keymap
	}{{"base", want.Base, got.Base}, {"fn", want.Fn, got.Fn}} {
		for slot := range layer.want {
			if layer.want[slot].Raw == layer.got[slot].Raw {
				continue
			}
			add("%s %s: %s → %s", layer.label, name(slot),
				protocol.KeyActionText(layer.want[slot]), protocol.KeyActionText(layer.got[slot]))
		}
	}

	for _, f := range []struct {
		label string
		want  any
		got   any
	}{
		{"lighting mode", want.Lighting.Mode, got.Lighting.Mode},
		{"lighting primary color", protocol.HexRGB(want.Lighting.RGB[0], want.Lighting.RGB[1], want.Lighting.RGB[2]), protocol.HexRGB(got.Lighting.RGB[0], got.Lighting.RGB[1], got.Lighting.RGB[2])},
		{"lighting secondary color", protocol.HexRGB(want.Lighting.SecondaryRGB[0], want.Lighting.SecondaryRGB[1], want.Lighting.SecondaryRGB[2]), protocol.HexRGB(got.Lighting.SecondaryRGB[0], got.Lighting.SecondaryRGB[1], got.Lighting.SecondaryRGB[2])},
		{"lighting color mode", want.Lighting.ColorMode, got.Lighting.ColorMode},
		{"lighting brightness", want.Lighting.Brightness, got.Lighting.Brightness},
		{"lighting speed", want.Lighting.Speed, got.Lighting.Speed},
		{"lighting direction", want.Lighting.Direction, got.Lighting.Direction},
		{"lighting effect mode type", want.Lighting.EffectModeType, got.Lighting.EffectModeType},
	} {
		if f.want != f.got {
			add("%s: %v → %v", f.label, f.want, f.got)
		}
	}

	for i := range want.PerKey {
		w, g := want.PerKey[i], got.PerKey[i]
		// Colors only: the ledId byte is the entry index on the wire (derived
		// on write, StateFile) and is never state to compare.
		if w.R == g.R && w.G == g.G && w.B == g.B {
			continue
		}
		add("per-key RGB entry %d: %s → %s", i, protocol.HexRGB(w.R, w.G, w.B), protocol.HexRGB(g.R, g.G, g.B))
	}

	for _, f := range []struct {
		label string
		want  any
		got   any
	}{
		{"report rate", want.Settings.ReportRate, got.Settings.ReportRate},
		{"game mode", want.Settings.GameMode, got.Settings.GameMode},
		{"Fn switch", want.Settings.FnSwitch, got.Settings.FnSwitch},
		{"sleep time", want.Settings.SleepTime, got.Settings.SleepTime},
		{"key delay", want.Settings.KeyDelay, got.Settings.KeyDelay},
		{"system mode", want.Settings.SystemMode, got.Settings.SystemMode},
		{"TFT display time", want.Settings.TFTDisplayTime, got.Settings.TFTDisplayTime},
		{"top dead zone", want.Settings.TopDeadZone, got.Settings.TopDeadZone},
		{"bottom dead zone", want.Settings.BottomDeadZone, got.Settings.BottomDeadZone},
		{"stability mode", want.Settings.StabilityMode, got.Settings.StabilityMode},
		{"auto calibration", want.Settings.AutoCalibration, got.Settings.AutoCalibration},
		{"single key wakeup", want.Settings.SingleKeyWakeup, got.Settings.SingleKeyWakeup},
		{"push button mode", want.Settings.PushButtonMode, got.Settings.PushButtonMode},
		{"NKRO switch", want.Settings.NKROSwitch, got.Settings.NKROSwitch},
		{"wireless report rate", want.Settings.WirelessReportRate, got.Settings.WirelessReportRate},
		{"power mode", want.Settings.PowerMode, got.Settings.PowerMode},
	} {
		if f.want != f.got {
			add("settings %s: %v → %v", f.label, f.want, f.got)
		}
	}
	return diffs
}
