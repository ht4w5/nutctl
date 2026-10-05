package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/ht4w5/nutctl/internal/protocol"
)

// The rebind picker (ticket 06): what a Key Slot can be bound to — a
// keyboard key, consumer key, mouse button or Function (CONTEXT.md's kinds
// of Key Action; spec user story 14), the Key Action catalog's offering for
// each kind. It is one more mode of the keyboard (like the path prompts and
// the write gate), it edits only the local buffer, and it never offers a
// binding the Model's layout table forbids: a Fn-disabled Key Slot cannot
// be rebound on the Fn Layer at all (spec user story 15).

// The picker's window of catalog entries, and the width entry names render
// in (the table pane's width beside the map).
const (
	bindWindow = 12
	bindNameW  = keyMapX - 2
)

// bindState is the picker's own state: which Key Slot is being rebound
// (the table's row, for its name), which kind of Key Action is showing,
// where the cursor is, and what the typed filter narrows the offering to.
type bindState struct {
	row    keyRow
	kind   protocol.ActionKind
	cursor int
	top    int
	filter string
}

// choices is the kind's catalog entries the filter still matches — the
// picker's offering right now.
func (b *bindState) choices() []protocol.ActionChoice {
	all := protocol.Catalog(b.kind)
	if b.filter == "" {
		return all
	}
	filter := strings.ToLower(b.filter)
	var out []protocol.ActionChoice
	for _, ch := range all {
		if strings.Contains(strings.ToLower(ch.Name), filter) {
			out = append(out, ch)
		}
	}
	return out
}

// startBind is `enter` on a table row: the picker opens for that Key Slot —
// unless the Model's layout table marks it Fn-disabled on the Fn Layer,
// where the firmware would reject any binding (the refusal names the row).
func (m *Model) startBind(r keyRow) (tea.Model, tea.Cmd) {
	m.notice = nil
	if r.disabled {
		m.status = fmt.Sprintf(
			"refusing to rebind Key Slot %d (%s) on the %s: Fn-disabled in the Model's layout table",
			r.slot, r.name, m.keyLayer)
		return m, nil
	}
	m.bind = bindState{row: r, kind: protocol.KindKeyboard}
	m.mode = modeBind
	return m, nil
}

// updateBind drives the picker: the kind bar (←/→), the cursor (↑/↓), the
// filter (typing, backspace), `enter` to bind and `esc` to cancel. Nothing
// is bound without enter; a cancelled picker changes nothing.
func (m *Model) updateBind(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch s := msg.String(); s {
	case "esc":
		m.mode = modeNormal
		m.status = "rebind cancelled — nothing changed"
		return m, nil
	case "enter":
		return m.finishBind(m.bind.choices())
	case "left":
		m.bind.kind = stepKind(m.bind.kind, false)
		m.bind.cursor, m.bind.top = 0, 0
	case "right":
		m.bind.kind = stepKind(m.bind.kind, true)
		m.bind.cursor, m.bind.top = 0, 0
	case "up":
		m.bind.cursor--
	case "down":
		m.bind.cursor++
	case "backspace":
		if r := []rune(m.bind.filter); len(r) > 0 {
			m.bind.filter = string(r[:len(r)-1])
			m.bind.cursor, m.bind.top = 0, 0
		}
	default:
		if msg.Type == tea.KeyRunes {
			m.bind.filter += string(msg.Runes)
			m.bind.cursor, m.bind.top = 0, 0
		}
	}
	choices := m.bind.choices()
	m.bind.cursor = clampRow(m.bind.cursor, len(choices))
	m.bind.top = keepVisible(m.bind.top, m.bind.cursor, bindWindow, len(choices))
	return m, nil
}

// stepKind cycles the picker's kind of Key Action (CONTEXT.md's list).
func stepKind(cur protocol.ActionKind, fwd bool) protocol.ActionKind {
	kinds := protocol.ActionKinds
	i := int(cur) % len(kinds)
	if fwd {
		return kinds[(i+1)%len(kinds)]
	}
	return kinds[(i+len(kinds)-1)%len(kinds)]
}

// finishBind is `enter` on a catalog entry: the chosen Key Action lands in
// the local edit buffer and the picker closes — the wire hears about it
// only through `a` (spec user story 19).
func (m *Model) finishBind(choices []protocol.ActionChoice) (tea.Model, tea.Cmd) {
	m.mode = modeNormal
	if len(choices) == 0 {
		m.status = "nothing to bind — no entry matches the filter"
		return m, nil
	}
	ch := choices[m.bind.cursor]
	km := m.keyLayer.keymap(&m.local)
	km[m.bind.row.slot] = ch.Action
	m.status = fmt.Sprintf("bound Key Slot %d (%s) on the %s to %s — a applies it to the Device",
		m.bind.row.slot, m.bind.row.name, m.keyLayer, protocol.KeyActionText(ch.Action))
	return m, nil
}

// bindBody renders the picker: what is being rebound and what it is bound
// to now, the kind bar, and the offering the filter matches.
func (m *Model) bindBody() []string {
	km := m.keyLayer.keymap(&m.local)
	out := []string{
		fmt.Sprintf("Bind Key Slot %d (%s) on the %s — now: %s",
			m.bind.row.slot, m.bind.row.name, m.keyLayer, protocol.KeyActionText(km[m.bind.row.slot])),
		"  ←/→ kind · ↑/↓ move · type to filter · enter bind · esc cancel",
		"",
		"  " + m.kindBar(),
	}
	choices := m.bind.choices()
	if len(choices) == 0 {
		out = append(out, "  (no entry matches the filter)")
		return out
	}
	end := min(m.bind.top+bindWindow, len(choices))
	for i := m.bind.top; i < end; i++ {
		prefix := "  "
		if i == m.bind.cursor {
			prefix = "> "
		}
		out = append(out, prefix+clip(choices[i].Name, bindNameW))
	}
	if m.bind.filter != "" {
		out = append(out, fmt.Sprintf("  filter %q — %d of %d entries",
			m.bind.filter, len(choices), len(protocol.Catalog(m.bind.kind))))
	}
	return out
}

// kindBar renders the kinds of Key Action, the active one bracketed (the
// tab bar's spelling): what the picker is offering is on screen without
// color.
func (m *Model) kindBar() string {
	parts := make([]string, len(protocol.ActionKinds))
	for i, k := range protocol.ActionKinds {
		item := fmt.Sprintf("%s %d", k, len(protocol.Catalog(k)))
		if k == m.bind.kind {
			item = "[" + item + "]"
		}
		parts[i] = item
	}
	return strings.Join(parts, "  ")
}
