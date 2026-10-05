package tui

import (
	"fmt"
	"strings"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/ht4w5/nutctl/internal/device"
	"github.com/ht4w5/nutctl/internal/protocol"
)

// The Keys screen (ticket 06): remapping as the everyday workflow — find a
// Key Slot in the table beside the static ASCII TKL map, open the rebind
// picker, pick a Key Action, and apply it through the shared edit mechanics
// (edit.go). The Base Layer and the Fn Layer toggle in place; the Knob's
// three gestures are rows like any Key Slot. The table is the Model's
// layout table (internal/device/layouts) rendered over the session's edit
// buffer — a row is a physical key or Knob gesture, never the
// firmware-matrix extraSlots (those have no key to find).

// keyLayer is which Layer the screen shows and edits (CONTEXT.md: the NUT87
// has a Base layer and an Fn layer).
type keyLayer int

const (
	layerBase keyLayer = iota
	layerFn
)

func (l keyLayer) String() string {
	if l == layerFn {
		return "Fn Layer"
	}
	return "Base Layer"
}

// keymap selects the Layer's Key Slot table out of a State — the one
// selection every Keys screen call site shares.
func (l keyLayer) keymap(st *device.State) *protocol.Keymap {
	if l == layerFn {
		return &st.Fn
	}
	return &st.Base
}

// keyRow is one table row: the Key Slot, its display name, whether the
// Model's layout table marks it Fn-disabled, and the Key Action the edits
// currently hold.
type keyRow struct {
	slot     int
	name     string
	disabled bool
	action   protocol.KeyAction
}

// Table geometry: the name and Key Action columns, the window the 90-row
// table scrolls in, and where the map pane starts.
const (
	keyNameW   = 22
	keyActionW = 24
	keyWindow  = 20
	keyMapX    = 57
)

// keysMap is the static ASCII TKL map (spec user story 9): the physical
// rows of the Model's layout table — Esc row, number row, QWERTY, home
// row, shift row, bottom row — with the nav cluster and arrows at the
// right. It is static art for orientation: it never reflects the cursor,
// the Layer or the edits. Labels follow the layout table's names in short
// form (Es=Esc, Tb=Tab, Cp=Caps, Sh=Shift, Ct=Ctrl, Wn=Win, Al=Alt,
// Sp=Spacebar, Mn=Menu, En=Enter, Bk=Backspace).
var keysMap = []string{
	"Es F1 F2 F3 F4 F5 F6 F7 F8 F9 F10 F11 F12  Prt Scr Pau",
	"`~ 1  2  3  4  5  6  7  8  9  0  -  =  Bk  Ins Hm  PgUp",
	"Tb Q  W  E  R  T  Y  U  I  O  P  [  ]  \\   Del End PgDn",
	"Cp A  S  D  F  G  H  J  K  L  ;  '  En",
	"Sh Z  X  C  V  B  N  M  ,  .  /  Sh                  ↑",
	"Ct Wn Al Sp                Al Fn Mn Ct     ←   ↓   →",
}

// keyRows is the table: every physical key of the Model's layout table in
// display order, then the Knob's three gestures as rows like any Key Slot
// (spec user story 13). On the Fn Layer a Key Slot the layout table marks
// Fn-disabled says so in its name (the CLI's "(fn-disabled)" spelling).
func (m *Model) keyRows() []keyRow {
	layout, err := device.LayoutFor(m.session.Model)
	if err != nil {
		return nil
	}
	var rows []keyRow
	km := m.keyLayer.keymap(&m.local)
	for _, k := range layout.Keys() {
		rows = append(rows, keyRow{slot: k.Slot, name: k.Name, action: (*km)[k.Slot]})
	}
	for _, g := range layout.Knob() {
		rows = append(rows, keyRow{slot: g.Slot, name: "knob " + g.Gesture, action: (*km)[g.Slot]})
	}
	if m.keyLayer == layerFn {
		for _, slot := range layout.FnDisabledSlots() {
			for i := range rows {
				if rows[i].slot == slot {
					rows[i].disabled = true
				}
			}
		}
	}
	return rows
}

// keysBody renders the Keys screen: the title naming the Layer shown, the
// key hints, and the table beside the static map. A row's `*` marks an edit
// that differs from the Device — the same truth the status bar's pending
// line states (spec user story 20).
func (m *Model) keysBody() []string {
	rows := m.keyRows()
	m.keyRow = clampRow(m.keyRow, len(rows))
	m.keyTop = keepVisible(m.keyTop, m.keyRow, keyWindow, len(rows))

	out := []string{
		"Keys — " + m.keyLayer.String(),
		"  ↑/↓ select · pgup/pgdn page · enter rebind · space Layer · * = differs from the Device — a applies, r reverts",
		"",
	}
	end := min(m.keyTop+keyWindow, len(rows))
	for i := m.keyTop; i < end; i++ {
		line := m.keyLine(rows[i], i == m.keyRow)
		if j := i - m.keyTop; j < len(keysMap) {
			line = padRight(line, keyMapX) + keysMap[j]
		}
		out = append(out, strings.TrimRight(line, " "))
	}
	return out
}

// keyLine renders one table row: cursor, Key Slot id, display name and the
// Key Action the edits hold, with `*` on rows that differ from the Device
// and "(fn-disabled)" on the Fn Layer's unbindable Key Slots (the CLI's
// spelling, nutctl get keymap --layer fn).
func (m *Model) keyLine(r keyRow, cursor bool) string {
	prefix := "  "
	if cursor {
		prefix = "> "
	}
	mark := ""
	shown := m.keyLayer.keymap(&m.local)
	actual := m.keyLayer.keymap(&m.state)
	if shown[r.slot].Raw != actual[r.slot].Raw {
		mark = " *"
	}
	name := r.name
	if r.disabled {
		name += " (fn-disabled)"
	}
	return fmt.Sprintf("%s%3d %s %s%s",
		prefix, r.slot, padRight(clip(name, keyNameW), keyNameW),
		padRight(clip(protocol.KeyActionText(r.action), keyActionW), keyActionW), mark)
}

// updateKeys drives the table: the row cursor (↑/↓, pgup/pgdn), the Layer
// toggle (space), and `enter` to rebind the selected Key Slot (bind.go).
func (m *Model) updateKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	rows := m.keyRows()
	switch msg.String() {
	case " ":
		m.keyLayer = 1 - m.keyLayer
	case "up":
		m.keyRow--
	case "down":
		m.keyRow++
	case "pgup":
		m.keyRow -= keyWindow
	case "pgdown":
		m.keyRow += keyWindow
	case "enter":
		if len(rows) > 0 {
			return m.startBind(rows[clampRow(m.keyRow, len(rows))])
		}
	}
	m.keyRow = clampRow(m.keyRow, len(rows))
	return m, nil
}

// clampRow keeps a cursor inside a list of n rows.
func clampRow(i, n int) int {
	if i < 0 {
		return 0
	}
	if i >= n {
		return n - 1
	}
	return i
}

// keepVisible scrolls a window of n rows so the cursor shows, without
// jumping further than it must: the table and the picker share one scroll
// behaviour.
func keepVisible(top, cursor, n, total int) int {
	if cursor < top {
		top = cursor
	}
	if cursor >= top+n {
		top = cursor - n + 1
	}
	if top > total-n {
		top = total - n
	}
	if top < 0 {
		top = 0
	}
	return top
}

// padRight widens s to at least n columns (the table pane the map sits
// beside) — counted in runes, so a clipped "…" still aligns the map.
func padRight(s string, n int) string {
	w := utf8.RuneCountInString(s)
	if w >= n {
		return s
	}
	return s + strings.Repeat(" ", n-w)
}

// clip shortens s to n runes with an ellipsis — the table's Key Action
// column stays a column; the picker and the pending line carry the full
// name.
func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
