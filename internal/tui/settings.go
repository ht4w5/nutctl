package tui

import (
	"fmt"
	"slices"
	"strconv"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/ht4w5/nutctl/internal/protocol"
)

// The Settings screen (ticket 05): the Device's persistent behaviour block
// (CONTEXT.md) — Report Rate (the Model's offered set), key delay, sleep and
// the Fn switch — edited row by row into the session's local edit buffer
// (edit.go). The value domains are the ones the vendor bundle's own settings
// UI uses (docs/protocol.md §4): key delay is a level 1..5, sleep is minutes
// 0..30 with 0 = never, the Fn switch is a plain 0/1 switch.

// settingRow is one editable row of the Settings screen, in display order.
type settingRow int

const (
	rowReportRate settingRow = iota
	rowKeyDelay
	rowSleep
	rowFnSwitch
	settingRowCount
)

// settingsBody renders the Settings screen: the four editable settings, the
// cursor on the selected row, and `*` on every row that differs from the
// Device — the same truth the status bar's pending line states.
func (m *Model) settingsBody() []string {
	s, d := m.local.Settings, m.state.Settings
	rows := []struct {
		label string
		value string
		dirty bool
	}{
		{"Report Rate", s.ReportRate.String(), s.ReportRate != d.ReportRate},
		{"Key delay", strconv.Itoa(int(s.KeyDelay)), s.KeyDelay != d.KeyDelay},
		{"Sleep", sleepText(s.SleepTime), s.SleepTime != d.SleepTime},
		{"Fn switch", switchText(s.FnSwitch), s.FnSwitch != d.FnSwitch},
	}
	out := []string{
		"Settings",
		"  ↑/↓ select · ←/→ change · * = differs from the Device — a applies, r reverts",
	}
	for i, r := range rows {
		prefix, mark := "  ", ""
		if settingRow(i) == m.sel {
			prefix = "> "
		}
		if r.dirty {
			mark = " *"
		}
		out = append(out, fmt.Sprintf("%s%-13s %s%s", prefix, r.label, r.value, mark))
	}
	return out
}

// updateSettings drives the row cursor (↑/↓) and the value editors (←/→).
// Every change is local: the wire hears about it only through `a`.
func (m *Model) updateSettings(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	s := &m.local.Settings
	switch msg.String() {
	case "up":
		m.sel = (m.sel + settingRowCount - 1) % settingRowCount
	case "down":
		m.sel = (m.sel + 1) % settingRowCount
	case "left", "right":
		fwd := msg.String() == "right"
		switch m.sel {
		case rowReportRate:
			s.ReportRate = stepReportRate(s.ReportRate, m.session.Model.ReportRates, fwd)
		case rowKeyDelay:
			s.KeyDelay = stepUint8(s.KeyDelay, protocol.KeyDelayMin, protocol.KeyDelayMax, fwd)
		case rowSleep:
			s.SleepTime = stepUint8(s.SleepTime, 0, protocol.SleepMinutesMax, fwd)
		case rowFnSwitch:
			if fwd {
				s.FnSwitch = 1
			} else {
				s.FnSwitch = 0
			}
		}
	}
	return m, nil
}

// stepReportRate cycles the Report Rate over the Model's offered set
// (CONTEXT.md: the NUT87 offers 1K/4K/8K). A value outside the set — read
// from a Device configured by other tools — steps in at an end; it is shown
// as read until the user changes it.
func stepReportRate(cur protocol.ReportRate, set []protocol.ReportRate, fwd bool) protocol.ReportRate {
	if len(set) == 0 {
		return cur
	}
	i := slices.Index(set, cur)
	switch {
	case i < 0 && fwd:
		return set[0]
	case i < 0:
		return set[len(set)-1]
	case fwd:
		return set[(i+1)%len(set)]
	default:
		return set[(i+len(set)-1)%len(set)]
	}
}

// stepUint8 steps a value inside [min, max] and clamps at the ends — an edit
// never leaves the domain the wire accepts (docs/protocol.md §4).
func stepUint8(v, min, max uint8, fwd bool) uint8 {
	if fwd {
		if v >= max {
			return max
		}
		return v + 1
	}
	if v <= min {
		return min
	}
	return v - 1
}

// sleepText renders the sleep time as what it is: minutes, or never (0).
func sleepText(v uint8) string {
	if v == 0 {
		return "off"
	}
	return fmt.Sprintf("%d min", v)
}

// switchText renders a 0/1 switch value; anything else is shown as read,
// never relabelled.
func switchText(v uint8) string {
	switch v {
	case 0:
		return "off"
	case 1:
		return "on"
	default:
		return fmt.Sprintf("unknown(%d)", v)
	}
}
