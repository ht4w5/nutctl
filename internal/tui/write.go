package tui

import (
	"context"
	"errors"
	"fmt"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/ht4w5/nutctl/internal/device"
)

// The write path every action shares (ADR-0003): before the session's FIRST
// write the gate offers the golden read — behind a fresh checked read of the
// Device, never the startup snapshot — and every write ends in read-back
// verification (spec user story 22). Loading a State File and applying the
// session's local edits are two kinds of one write; the Device screen's Load
// (device.go) and the edit mechanics' `a` (edit.go) both land here.

// writeKind is which kind of write is waiting at the gate — and the words
// every sentence about it uses. The phrasings live here so the handlers stay
// sentences, not switches; wholeState says what goes on the wire: a load
// sends every block, an apply sends only the blocks its edits touched.
type writeKind struct {
	verb       string // "load" / "apply"
	action     string // "load the State File" / "apply your pending edits"
	sentLabel  string // the left side the read-back diff names ("State File" → Device)
	wholeState bool
}

var (
	writeLoad  = writeKind{verb: "load", action: "load the State File", sentLabel: "State File", wholeState: true}
	writeApply = writeKind{verb: "apply", action: "apply your pending edits", sentLabel: "your edits"}
)

// writeRequest is one write on its way to the Device: the State to send,
// what the gate decided about the golden read, and what to tell the user
// afterwards.
type writeRequest struct {
	kind    writeKind
	path    string // State File path (writeLoad)
	want    device.State
	golden  string // golden State File to write before the write ("" = none)
	note    string // what the gate did with the golden read
	warning string // firmware-mismatch warning from the file check
}

// gateBody is the write gate's golden-read prompt (ADR-0003), the TUI twin
// of the CLI's — one spelling of the offer (device.GoldenPrompt) whatever
// the write is.
func (m *Model) gateBody() []string {
	return []string{
		"Write gate (ADR-0003) — the session's first write",
		"  " + device.GoldenPrompt(m.golden),
		"  y — save the golden read, then " + m.request.kind.action,
		"  n — skip the golden read, " + m.request.kind.action + " anyway",
		"  esc — cancel the " + m.request.kind.verb + ", write nothing",
	}
}

// updateGate drives the golden-read prompt: y saves the golden read and
// writes, n writes without saving (the dismissible escape hatch of spec user
// story 5), esc cancels. Nothing happens without an answer.
func (m *Model) updateGate(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "y", "enter", "n":
		req := m.request
		if msg.String() == "n" {
			req.note = "skipped the golden read — the Device's current state is not saved (the offer stands next session)"
		} else {
			req.golden = m.golden
			req.note = "golden read saved to ./" + m.golden
		}
		m.mode = modeNormal
		m.busy = true
		return m, m.writeCmd(m.session, m.state, req)
	case "esc":
		m.mode = modeNormal
		m.notice = nil
		m.status = m.request.kind.verb + " cancelled — nothing was written"
	}
	return m, nil
}

// refreshedMsg is the fresh checked read behind the write gate (ADR-0003):
// what the golden read saves and whether this session may write at all must
// rest on what the Device reports NOW, not what it said at startup.
type refreshedMsg struct {
	req   writeRequest
	st    device.State
	check *device.CheckError
	err   error
}

// appliedMsg is the result of the write: the batched writes and the read-back
// verification diff (spec user story 22).
type appliedMsg struct {
	req   writeRequest
	back  device.State
	diffs []string
	err   error
	wrote bool // the Device was written (the gate passed for this session)
}

// freshReadCmd makes the write gate's check half (ADR-0003): one fresh
// checked read of the Device, right before the session's first write.
func (m *Model) freshReadCmd(s *device.Session, req writeRequest) tea.Cmd {
	return func() tea.Msg {
		st, err := s.ReadChecked()
		msg := refreshedMsg{req: req, st: st}
		if err != nil {
			var ce *device.CheckError
			if errors.As(err, &ce) {
				msg.check = ce
			} else {
				msg.err = err
			}
		}
		return msg
	}
}

// writeOrGate sends the write now if this session already passed the write
// gate, or queues it behind the gate's fresh checked read (ADR-0003: the
// session's FIRST write is the gated one).
func (m *Model) writeOrGate(req writeRequest) (tea.Model, tea.Cmd) {
	m.busy = true
	if m.wrote {
		return m, m.writeCmd(m.session, m.state, req)
	}
	return m, m.freshReadCmd(m.session, req)
}

// writeCmd is the write itself: the golden read first (when the gate asked
// for one — a failed golden save refuses the write, like the CLI), then the
// batched writes verified by reading the Device back (the proof is the
// read-back, never the write itself). Only blocks the write actually changes
// go out: an apply of local edits never rewrites untouched blocks.
func (m *Model) writeCmd(s *device.Session, current device.State, req writeRequest) tea.Cmd {
	return func() tea.Msg {
		if req.golden != "" {
			golden := device.StateFileFor(s.Model, s.DeviceInfo.Version, current)
			if err := device.SaveStateFile(req.golden, golden); err != nil {
				return appliedMsg{req: req, err: fmt.Errorf("golden read not saved, refusing to write: %w", err)}
			}
		}
		var (
			back  device.State
			diffs []string
			err   error
		)
		if req.kind.wholeState {
			back, diffs, err = s.ApplyVerified(context.Background(), req.want)
		} else {
			back, diffs, err = s.ApplyChangesVerified(context.Background(), current, req.want)
		}
		return appliedMsg{req: req, back: back, diffs: diffs, err: err, wrote: true}
	}
}

// updateRefreshed routes the fresh checked read: a Device that fails its
// self-checks is never written (ADR-0003), and what it just reported becomes
// the state on screen — and the golden read's content. The fresh read also
// rebases the edit buffer AND the payload of the write waiting at the gate
// (device.Rebase): the user's edits keep their values, everything they did
// not touch follows the Device — so the write sends the user's changes and
// nothing stale.
func (m *Model) updateRefreshed(msg refreshedMsg) (tea.Model, tea.Cmd) {
	m.busy = false
	m.notice = nil
	if msg.err != nil {
		m.status = "error: refusing to " + msg.req.kind.verb + ": " + msg.err.Error()
		return m, nil
	}
	merged := device.Rebase(m.state, msg.st, m.local)
	m.local = merged
	m.state = msg.st
	m.checkErr = msg.check
	if msg.check != nil {
		m.status = "refusing to " + msg.req.kind.verb + ": the Device must pass its self-checks before the first write (ADR-0003)"
		return m, nil
	}
	m.request = msg.req
	m.request.want = merged
	m.golden = device.GoldenName(m.deps.Now())
	m.mode = modeGate
	return m, nil
}

// updateApplied reports the write: the read-back diff (or its absence) and
// the Device's actual state as the new truth on screen. An apply whose
// read-back failed verification keeps its edits pending — they did not land,
// and nothing is lost silently; `r` drops them.
func (m *Model) updateApplied(msg appliedMsg) (tea.Model, tea.Cmd) {
	m.busy = false
	m.mode = modeNormal
	if msg.wrote {
		m.wrote = true // the gate passed for the rest of the session
	}
	if msg.err != nil {
		m.notice = nil
		m.status = "error: " + msg.err.Error()
		return m, nil
	}
	old := m.state
	m.state = msg.back // the Device is the source of truth
	if msg.req.kind == writeLoad || len(msg.diffs) == 0 {
		m.local = msg.back
	} else {
		// The edits that did not land stay pending (never silently lost),
		// rebased onto what the Device reports now so a retry writes only
		// the user's changes.
		m.local = device.Rebase(old, msg.back, m.local)
	}
	notice := []string{}
	for _, line := range []string{msg.req.warning, msg.req.note} {
		if line != "" {
			notice = append(notice, line)
		}
	}
	if len(msg.diffs) > 0 {
		notice = append(notice, device.DiffHeader(len(msg.diffs), msg.req.kind.sentLabel))
		const shown = 8
		for i, d := range msg.diffs {
			if i == shown {
				notice = append(notice, fmt.Sprintf("  … and %d more", len(msg.diffs)-shown))
				break
			}
			notice = append(notice, "  "+d)
		}
		m.status = m.writeStatus(msg.req, false)
	} else {
		m.status = m.writeStatus(msg.req, true)
	}
	m.notice = notice
	return m, nil
}

// writeStatus is the status bar's sentence for a write's ending: what was
// written to which Device, and what the read-back proved (or did not).
func (m *Model) writeStatus(req writeRequest, verified bool) string {
	dev := fmt.Sprintf("the %s at %s (firmware %s)",
		m.session.Model.Name, m.session.Info.Path, m.session.DeviceInfo.Version)
	if req.kind == writeApply {
		if verified {
			return "applied your pending edits to " + dev + " — read-back verified: the Device matches your edits"
		}
		return "apply to " + dev + " — read-back verification FAILED, your edits are still pending"
	}
	if verified {
		return fmt.Sprintf("loaded %s onto %s — read-back verified: the Device matches the State File", req.path, dev)
	}
	return fmt.Sprintf("loaded %s onto %s — read-back verification FAILED", req.path, dev)
}
