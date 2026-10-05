package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/ht4w5/nutctl/internal/device"
)

// The edit mechanics every edit screen shares (ticket 05 — and the reason
// they live here, not on one screen): one local edit buffer over the
// Device's state, pending changes visible in the status bar, `a` to apply
// through the write path (write.go) and `r` to revert from the Device's
// actual state. Edits belong to the session, not to a screen: switching
// screens never loses them (spec user story 19).

// pendingDiffs is what differs between the Device's actual state and the
// local edits — what `a` would write and `r` would throw away (spec user
// story 20: the Device and the edits disagree, visibly).
func (m *Model) pendingDiffs() []string {
	if m.session == nil {
		return nil
	}
	return device.StateDiffs(m.state, m.local, m.session.Model)
}

// pendingLine is the status bar's pending-changes line: every difference,
// Device value → edit, capped so the line stays a line.
func (m *Model) pendingLine() string {
	diffs := m.pendingDiffs()
	if len(diffs) == 0 {
		return ""
	}
	const shown = 3
	items, more := diffs, ""
	if len(items) > shown {
		items = items[:shown]
		more = fmt.Sprintf(" · and %d more", len(diffs)-shown)
	}
	return fmt.Sprintf("pending: %s — %s%s", plural(len(diffs), "change"), strings.Join(items, " · "), more)
}

// startApply is `a`: apply the pending edits through the write gate
// (ADR-0003) and read-back verification. Nothing pending, or a read-only
// Device, refuses with a saying status bar and no wire traffic — unsaved
// changes are never silently written.
func (m *Model) startApply() (tea.Model, tea.Cmd) {
	m.notice = nil
	if len(m.pendingDiffs()) == 0 {
		m.status = "nothing to apply — no pending changes"
		return m, nil
	}
	if reason := m.readOnlyReason(); reason != "" {
		m.status = "refusing to apply: " + reason
		return m, nil
	}
	req := writeRequest{kind: writeApply, want: m.local}
	return m.writeOrGate(req)
}

// revert is `r`: drop the local edits and show the Device's actual state
// again (spec user story 21). It never touches the wire — the edits were
// never on the Device, so there is nothing to undo there.
func (m *Model) revert() {
	m.notice = nil
	n := len(m.pendingDiffs())
	if n == 0 {
		m.status = "nothing to revert — no pending changes"
		return
	}
	m.local = m.state
	m.status = fmt.Sprintf("reverted %s from the Device's actual state", plural(n, "change"))
}
