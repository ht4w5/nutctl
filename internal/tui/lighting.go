package tui

import (
	"fmt"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/ht4w5/nutctl/internal/device"
	"github.com/ht4w5/nutctl/internal/protocol"
)

// The Lighting screen (ticket 07): both halves of lighting are configurable
// here — the ambient Lighting Effect (mode, primary/secondary colors,
// brightness, speed, direction) as a form, and the Per-Key RGB table as a
// slot grid of per-slot colors (CONTEXT.md: an entry IS a Key Slot — the
// bundle colors key i at customLedData[i], docs/protocol.md §4). `space`
// switches halves in place, like the Keys screen's Layer toggle. Every edit
// lands in the session's local edit buffer (edit.go): the wire hears about
// it only through `a`.
//
// Value domains are the Model's advertised capability set (internal/device):
// effect modes from its mode table, brightness and speed inside its ranges.
// The mode table also says which of the block's fields a mode animates — a
// row the current mode ignores says so and stays honest about it, and the
// direction row only offers the pair of directions the vendor's own
// direction buttons write for that mode (never a value the firmware would
// be left guessing about).

// lightHalf is which half of the Lighting screen shows: the Lighting Effect
// form, or the Per-Key RGB grid.
type lightHalf int

const (
	halfEffect lightHalf = iota
	halfPerKey
)

// other is the half `space` switches to — the Keys screen's Layer toggle's
// twin.
func (h lightHalf) other() lightHalf {
	if h == halfEffect {
		return halfPerKey
	}
	return halfEffect
}

// effectRowLabel names one row of the Lighting Effect form — the one
// spelling the form, the color editor's title and the status bar share.
func effectRowLabel(r effectRow) string {
	switch r {
	case effectMode:
		return "Mode"
	case effectPrimary:
		return "Primary color"
	case effectSecondary:
		return "Secondary color"
	case effectBrightness:
		return "Brightness"
	case effectSpeed:
		return "Speed"
	default:
		return "Direction"
	}
}

// effectRow is one editable row of the Lighting Effect form, in display
// order (the ticket's list: mode, primary/secondary colors, brightness 1–6,
// speed 1–6, direction).
type effectRow int

const (
	effectMode effectRow = iota
	effectPrimary
	effectSecondary
	effectBrightness
	effectSpeed
	effectDirection
	effectRowCount
)

// The Per-Key RGB grid geometry: 128 entries (CONTEXT.md) in four columns,
// windowed like every other long table on the TUI.
const (
	perKeyCols   = 4
	perKeyGrid   = 128 / perKeyCols // grid rows
	perKeyWindow = 8                // grid rows visible at once
)

// lightState is the Lighting screen's own state: which half shows and where
// each half's cursor is. Edits live in the session's buffer, not here.
type lightState struct {
	half  lightHalf
	row   effectRow // the form's row cursor
	entry int       // the grid's entry cursor (a Per-Key RGB entry = Key Slot)
	top   int       // first grid row in the window
}

// colorTarget is where a color the editor commits lands in the edit buffer:
// the effect's primary or secondary color, or one Per-Key RGB entry.
type colorTarget int

const (
	colorPrimary colorTarget = iota
	colorSecondary
	colorEntry
)

// colorState is the color editor's own state (opened from the effect form's
// color rows and from the grid): the target, the color being edited and the
// color it replaced, the channel cursor, and the digits typed for it.
type colorState struct {
	target colorTarget
	entry  int // Per-Key RGB entry (colorEntry only)
	was    [3]uint8
	rgb    [3]uint8
	ch     int    // channel cursor: 0 red, 1 green, 2 blue
	typed  string // digits typed for the current channel ("" = stepped, not typed)
}

// lightingBody renders the active half of the Lighting screen.
func (m *Model) lightingBody() []string {
	if m.light.half == halfPerKey {
		return m.perKeyBody()
	}
	return m.effectBody()
}

// effectBody renders the Lighting Effect form: the six editable rows, the
// cursor on the selected one, and `*` on every row that differs from the
// Device — the same truth the status bar's pending line states (spec user
// story 20).
func (m *Model) effectBody() []string {
	e, d := m.local.Lighting, m.state.Lighting
	caps := m.session.Model.Lighting
	rows := []struct {
		label string
		value string
		dirty bool
		note  string
	}{
		{effectRowLabel(effectMode), protocol.LightingModeText(e.Mode), e.Mode != d.Mode, ""},
		{effectRowLabel(effectPrimary), swatch(e.RGB[0], e.RGB[1], e.RGB[2]), e.RGB != d.RGB, ""},
		{effectRowLabel(effectSecondary), swatch(e.SecondaryRGB[0], e.SecondaryRGB[1], e.SecondaryRGB[2]), e.SecondaryRGB != d.SecondaryRGB, ""},
		{effectRowLabel(effectBrightness), protocol.RangeText(e.Brightness, caps.BrightnessMin, caps.BrightnessMax), e.Brightness != d.Brightness, ""},
		{effectRowLabel(effectSpeed), protocol.RangeText(e.Speed, caps.SpeedMin, caps.SpeedMax), e.Speed != d.Speed, ""},
		{effectRowLabel(effectDirection), protocol.DirectionText(e.Direction), e.Direction != d.Direction, ""},
	}
	// A field the current effect mode does not animate is marked, never
	// hidden and never silently rewritten (the mode table is the vendor's
	// own isShowSpeed/isShowDirection/isShowColor). A mode no table names
	// gets no claim either way.
	if mode, known := protocol.LightingModeFor(e.Mode); known {
		const unused = "not used by this mode"
		if !mode.ShowsColor {
			rows[effectPrimary].note, rows[effectSecondary].note = unused, unused
		}
		if mode.Value == protocol.CustomLightingMode {
			const custom = "the Per-Key RGB table carries this mode's colors"
			rows[effectPrimary].note, rows[effectSecondary].note = custom, custom
		}
		if !mode.ShowsSpeed {
			rows[effectSpeed].note = unused
		}
		if !mode.ShowsDirection {
			rows[effectDirection].note = unused
		}
	}

	out := []string{
		"Lighting — Lighting Effect",
		"  ↑/↓ select · ←/→ change · enter edit color · space Per-Key RGB · * = differs from the Device — a applies, r reverts",
		"",
	}
	for i, r := range rows {
		prefix, mark, note := "  ", "", ""
		if effectRow(i) == m.light.row {
			prefix = "> "
		}
		if r.dirty {
			mark = " *"
		}
		if r.note != "" {
			note = " — " + r.note
		}
		out = append(out, fmt.Sprintf("%s%-15s %s%s%s", prefix, r.label, r.value, mark, note))
	}
	return out
}

// perKeyBody renders the Per-Key RGB half: the slot grid of per-slot colors
// (a Key Slot per entry, named from the Model's layout table where it has a
// name), windowed like the Keys table, with `*` on entries that differ from
// the Device.
func (m *Model) perKeyBody() []string {
	m.light.entry = clampRow(m.light.entry, 128)
	m.light.top = keepVisible(m.light.top, m.light.entry/perKeyCols, perKeyWindow, perKeyGrid)

	out := []string{
		"Lighting — Per-Key RGB",
		"  ↑/↓/←/→ select · enter edit color · pgup/pgdn page · space Lighting Effect · * = differs from the Device — a applies, r reverts",
		"",
	}
	end := min(m.light.top+perKeyWindow, perKeyGrid)
	for r := m.light.top; r < end; r++ {
		cells := make([]string, 0, perKeyCols)
		for c := 0; c < perKeyCols; c++ {
			i := r*perKeyCols + c
			cells = append(cells, m.perKeyCell(i, i == m.light.entry))
		}
		out = append(out, strings.TrimRight(strings.Join(cells, "  "), " "))
	}

	i := m.light.entry
	e := m.local.PerKey[i]
	out = append(out, "", fmt.Sprintf("  entry %d%s — %s · visible in effect mode %s",
		i, m.slotTag(i), swatch(e.R, e.G, e.B), protocol.LightingModeText(protocol.CustomLightingMode)))
	return out
}

// perKeyCell renders one grid cell: the entry number, a color swatch and
// its hex, `*` where the edit differs from the Device, and brackets on the
// cursor. Every cell is the same width — cursor or not — so the grid stays
// a grid (and the frame stays a golden string).
func (m *Model) perKeyCell(i int, cursor bool) string {
	e, d := m.local.PerKey[i], m.state.PerKey[i]
	mark := " "
	if e.R != d.R || e.G != d.G || e.B != d.B {
		mark = "*"
	}
	core := fmt.Sprintf("%3d %s%s", i, swatch(e.R, e.G, e.B), mark)
	if cursor {
		return "[" + core + "]"
	}
	return " " + core + " "
}

// updateLighting drives the active half: the form's row cursor and value
// editors, the grid's entry cursor — and `space` to switch halves in place
// (cursor and edits stay where they were).
func (m *Model) updateLighting(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.String() == " " {
		m.light.half = m.light.half.other()
		return m, nil
	}
	if m.light.half == halfPerKey {
		return m.updatePerKey(msg)
	}
	return m.updateEffect(msg)
}

// updateEffect drives the Lighting Effect form. Every change is local: the
// wire hears about it only through `a` (spec user story 19).
func (m *Model) updateEffect(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	e := &m.local.Lighting
	caps := m.session.Model.Lighting
	switch msg.String() {
	case "up":
		m.light.row = (m.light.row + effectRowCount - 1) % effectRowCount
	case "down":
		m.light.row = (m.light.row + 1) % effectRowCount
	case "enter":
		switch m.light.row {
		case effectPrimary:
			return m.startColor(colorPrimary, 0)
		case effectSecondary:
			return m.startColor(colorSecondary, 0)
		}
	case "left", "right":
		fwd := msg.String() == "right"
		switch m.light.row {
		case effectMode:
			values := make([]uint8, 0, len(caps.Modes))
			for _, mode := range caps.Modes {
				values = append(values, mode.Value)
			}
			e.Mode = stepValue(e.Mode, values, fwd)
		case effectPrimary:
			return m.startColor(colorPrimary, 0)
		case effectSecondary:
			return m.startColor(colorSecondary, 0)
		case effectBrightness:
			e.Brightness = stepUint8(e.Brightness, caps.BrightnessMin, caps.BrightnessMax, fwd)
		case effectSpeed:
			e.Speed = stepUint8(e.Speed, caps.SpeedMin, caps.SpeedMax, fwd)
		case effectDirection:
			// Only the pair the mode animates — the two values the
			// vendor's own direction buttons write for it. A mode with no
			// direction edits nothing (its byte is not its business).
			if mode, ok := protocol.LightingModeFor(e.Mode); ok {
				if pair := mode.DirectionValues(); pair != nil {
					if fwd {
						e.Direction = pair[1]
					} else {
						e.Direction = pair[0]
					}
				}
			}
		}
	}
	return m, nil
}

// updatePerKey drives the slot grid: arrows move the entry cursor (across
// row edges too), pgup/pgdn page, `enter` opens the color editor for the
// selected entry.
func (m *Model) updatePerKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "left":
		m.light.entry--
	case "right":
		m.light.entry++
	case "up":
		m.light.entry -= perKeyCols
	case "down":
		m.light.entry += perKeyCols
	case "pgup":
		m.light.entry -= perKeyCols * perKeyWindow
	case "pgdown":
		m.light.entry += perKeyCols * perKeyWindow
	case "enter":
		return m.startColor(colorEntry, m.light.entry)
	}
	m.light.entry = clampRow(m.light.entry, 128)
	return m, nil
}

// startColor opens the color editor for one target of the edit buffer — the
// effect's primary/secondary color or one Per-Key RGB entry (spec user
// story 17: highlight specific keys). Nothing changes until enter.
func (m *Model) startColor(target colorTarget, entry int) (tea.Model, tea.Cmd) {
	m.notice = nil
	cur := m.currentColor(target, entry)
	m.color = colorState{target: target, entry: entry, was: cur, rgb: cur}
	m.mode = modeColor
	return m, nil
}

// currentColor reads one target's color out of the edit buffer.
func (m *Model) currentColor(target colorTarget, entry int) [3]uint8 {
	switch target {
	case colorPrimary:
		return m.local.Lighting.RGB
	case colorSecondary:
		return m.local.Lighting.SecondaryRGB
	default:
		e := m.local.PerKey[entry]
		return [3]uint8{e.R, e.G, e.B}
	}
}

// colorLabel names what the editor is setting — the one spelling shared
// with the status bar and the pending line.
func (m *Model) colorLabel() string {
	switch m.color.target {
	case colorPrimary:
		return effectRowLabel(effectPrimary)
	case colorSecondary:
		return effectRowLabel(effectSecondary)
	default:
		return fmt.Sprintf("Per-Key RGB entry %d%s", m.color.entry, m.slotTag(m.color.entry))
	}
}

// slotTag names a Key Slot from the Model's layout table in its
// parenthetical spelling (` (Esc)`) — the same words the pending line and
// the read-back diffs name it in (device.SlotTag, one spelling).
func (m *Model) slotTag(slot int) string {
	if m.session == nil {
		return ""
	}
	return device.SlotTag(m.session.Model, slot)
}

// updateColor drives the color editor: the channel cursor (↑/↓), stepping
// (←/→ ±1, pgup/pgdn ±16), and typing the channel's value as digits.
// `enter` sets the color into the edit buffer, `esc` changes nothing.
func (m *Model) updateColor(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	c := &m.color
	switch s := msg.String(); s {
	case "esc":
		m.mode = modeNormal
		m.status = "color edit cancelled — nothing changed"
		return m, nil
	case "enter":
		return m.finishColor()
	case "up":
		c.ch = (c.ch + 2) % 3
		c.typed = ""
	case "down":
		c.ch = (c.ch + 1) % 3
		c.typed = ""
	case "left", "right":
		c.typed = ""
		c.rgb[c.ch] = stepUint8(c.rgb[c.ch], 0, 255, s == "right")
	case "pgup", "pgdown":
		c.typed = ""
		step := 16
		if s == "pgdown" {
			step = -16
		}
		c.rgb[c.ch] = clampByte(int(c.rgb[c.ch]) + step)
	case "backspace":
		if c.typed != "" {
			c.typed = c.typed[:len(c.typed)-1]
			c.rgb[c.ch] = typedByte(c.typed)
		}
	default:
		if msg.Type == tea.KeyRunes {
			for _, r := range msg.Runes {
				if r < '0' || r > '9' {
					continue
				}
				if len(c.typed) < 3 {
					c.typed += string(r)
				}
				c.rgb[c.ch] = typedByte(c.typed)
			}
		}
	}
	return m, nil
}

// finishColor is `enter`: the edited color lands in the local edit buffer
// and the editor closes — the wire hears about it only through `a`.
func (m *Model) finishColor() (tea.Model, tea.Cmd) {
	m.mode = modeNormal
	c := m.color
	switch c.target {
	case colorPrimary:
		m.local.Lighting.RGB = c.rgb
	case colorSecondary:
		m.local.Lighting.SecondaryRGB = c.rgb
	default:
		e := &m.local.PerKey[c.entry]
		e.R, e.G, e.B = c.rgb[0], c.rgb[1], c.rgb[2]
	}
	m.status = fmt.Sprintf("set %s to %s — a applies it to the Device",
		m.colorLabel(), protocol.HexRGB(c.rgb[0], c.rgb[1], c.rgb[2]))
	return m, nil
}

// colorBody renders the color editor: what is being set, the color it was,
// and the three channels being edited.
func (m *Model) colorBody() []string {
	c := m.color
	out := []string{
		"Edit color — " + m.colorLabel(),
		"  ↑/↓ channel · ←/→ ±1 · pgup/pgdn ±16 · digits type · enter set · esc cancel",
		"",
		fmt.Sprintf("  was %s · now %s",
			swatch(c.was[0], c.was[1], c.was[2]), swatch(c.rgb[0], c.rgb[1], c.rgb[2])),
	}
	for i, ch := range []string{"red", "green", "blue"} {
		prefix := "  "
		if i == c.ch {
			prefix = "> "
		}
		out = append(out, fmt.Sprintf("%s%-8s%3d", prefix, ch, c.rgb[i]))
	}
	return out
}

// typedByte is the channel value digits spell: decimal, clamped to a byte —
// an edit never leaves the domain (docs/protocol.md §4).
func typedByte(s string) uint8 {
	if s == "" {
		return 0
	}
	v, err := strconv.Atoi(s)
	if err != nil || v > 255 {
		return 255
	}
	return uint8(v)
}

// clampByte clamps a computed channel value to a byte.
func clampByte(v int) uint8 {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return uint8(v)
}

// swatch renders a color: a filled block (a real swatch in a color
// terminal, plain glyphs under a non-color one) beside its hex — one
// spelling of a color, the same hex the diffs and the CLI print.
func swatch(r, g, b byte) string {
	hex := protocol.HexRGB(r, g, b)
	st := lipgloss.NewStyle().Foreground(lipgloss.Color(hex)).Background(lipgloss.Color(hex))
	return st.Render("██") + " " + hex
}
