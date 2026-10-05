package tui

import (
	"context"
	"errors"
	"fmt"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/ht4w5/nutctl/internal/device"
	"github.com/ht4w5/nutctl/internal/protocol"
)

// The Device screen: the Device introduces itself (connection state, Device
// Identity, firmware version, Report Rate, charge/battery status) and hosts
// the explicit Save and Load State File actions (spec user stories 6, 7).
// Loading is the write path — it goes through the write gate (ADR-0003) and
// ends in read-back verification, exactly like `nutctl load`.

// deviceBody renders the Device screen: what the Device reports about
// itself, and the two State File actions.
func (m *Model) deviceBody() []string {
	s := m.session
	load := "[l] load a State File onto the Device"
	if m.readOnlyReason() != "" {
		load = "[l] load — refused while read-only"
	}
	return []string{
		"Device",
		fmt.Sprintf("  Model:           %s", s.Model.Name),
		fmt.Sprintf("  Connection:      %s — connected (%s)", s.Model.Connection, s.Info.Path),
		fmt.Sprintf("  Device Identity: %s %q (manufacturer %d, product %d)",
			device.IdentityFromUSB(s.Info).USBID(), s.Info.ProductName,
			s.DeviceInfo.Manufacturer, s.DeviceInfo.Product),
		fmt.Sprintf("  Firmware:        %s", s.DeviceInfo.Version),
		fmt.Sprintf("  Firmware status: %s", protocol.FirmwareStatusText(s.DeviceInfo.FirmwareStatus)),
		fmt.Sprintf("  Report Rate:     %s", m.state.Settings.ReportRate),
		fmt.Sprintf("  Battery:         %d%% (charge status %d)", s.DeviceInfo.BatteryLevel, s.DeviceInfo.ChargeStatus),
		"",
		"  State Files: [s] save current state   " + load,
	}
}

// saveBody is the Save prompt: a path the user typed, then the file (ADR-0005
// — written only where they ask).
func (m *Model) saveBody() []string {
	return []string{
		"Save state to a State File",
		"  written only where you ask (ADR-0005); the Device stays the source of truth",
		"",
		m.ti.View(),
	}
}

// loadBody is the Load prompt. Loading writes to the Device, so the write
// gate (ADR-0003) gets its say before this session's first write.
func (m *Model) loadBody() []string {
	return []string{
		"Load a State File onto the Device",
		"  the write gate asks before this session's first write (ADR-0003)",
		"",
		m.ti.View(),
	}
}

// gateBody is the write gate's golden-read prompt (ADR-0003), the TUI twin
// of the CLI's — one spelling of the offer (device.GoldenPrompt).
func (m *Model) gateBody() []string {
	return []string{
		"Write gate (ADR-0003) — the session's first write",
		"  " + device.GoldenPrompt(m.golden),
		"  y — save the golden read, then load the State File",
		"  n — skip the golden read, load the State File anyway",
		"  esc — cancel the load, write nothing",
	}
}

// updatePath drives the State File path prompt: esc cancels without touching
// anything, enter is the user action that saves or loads.
func (m *Model) updatePath(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.mode = modeNormal
		m.notice = nil
		m.status = "cancelled — nothing was read or written"
		return m, nil
	case "enter":
		path := m.ti.Value()
		if path == "" {
			m.status = "error: needs a State File path"
			return m, nil
		}
		saving := m.mode == modeSavePath
		m.mode = modeNormal
		m.busy = true
		if saving {
			return m, m.saveCmd(m.session, m.state, path)
		}
		return m, m.loadCmd(m.session, path)
	}
	var cmd tea.Cmd
	m.ti, cmd = m.ti.Update(msg)
	return m, cmd
}

// updateGate drives the golden-read prompt: y saves the golden read and
// loads, n loads without saving (the dismissible escape hatch of spec user
// story 5), esc cancels the load. Nothing happens without an answer.
func (m *Model) updateGate(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "y", "enter":
		req := m.pending
		req.golden = m.golden
		req.note = "golden read saved to ./" + m.golden
		m.mode = modeNormal
		m.busy = true
		return m, m.applyCmd(m.session, m.state, req)
	case "n":
		req := m.pending
		req.note = "skipped the golden read — the Device's current state is not saved (the offer stands next session)"
		m.mode = modeNormal
		m.busy = true
		return m, m.applyCmd(m.session, m.state, req)
	case "esc":
		m.mode = modeNormal
		m.notice = nil
		m.status = "load cancelled — nothing was written"
	}
	return m, nil
}

// --- the actions, run as commands off the event loop ---
//
// Each command takes what it needs by value (the Session and the state
// snapshot): the event loop stays the only writer of the Model, one command
// runs at a time (busy), and the wire has one owner (the Session).

// loadRequest is one Load action on its way to the Device: the checked State
// File plus what the write gate decided (the golden read) and what to tell
// the user afterwards.
type loadRequest struct {
	path    string
	sf      device.StateFile
	golden  string // golden State File to write before the load ("" = none)
	note    string // what the gate did with the golden read
	warning string // firmware-mismatch warning from the file check
}

// savedMsg is the result of the Save action.
type savedMsg struct {
	path string
	err  error
}

// loadCheckedMsg is a State File read from disk and checked against this
// Device: a wrong-Model file is refused here (spec user story 25), a
// firmware mismatch only warns (26).
type loadCheckedMsg struct {
	req loadRequest
	err error
}

// refreshedMsg is the fresh checked read behind the write gate (ADR-0003):
// what the golden read saves and whether this session may write at all must
// rest on what the Device reports NOW, not what it said at startup.
type refreshedMsg struct {
	req   loadRequest
	st    device.State
	check *device.CheckError
	err   error
}

// appliedMsg is the result of the load's write path: the batched writes and
// the read-back verification diff (spec user story 22).
type appliedMsg struct {
	req   loadRequest
	back  device.State
	diffs []string
	err   error
	wrote bool // the Device was written (the gate passed for this session)
}

// saveCmd writes the Device's current state to a State File — the one write
// the Save action ever does, and only on the user's enter (ADR-0005).
func (m *Model) saveCmd(s *device.Session, st device.State, path string) tea.Cmd {
	return func() tea.Msg {
		sf := device.StateFileFor(s.Model, s.DeviceInfo.Version, st)
		return savedMsg{path: path, err: device.SaveStateFile(path, sf)}
	}
}

// loadCmd reads a State File and checks it against this Device.
func (m *Model) loadCmd(s *device.Session, path string) tea.Cmd {
	return func() tea.Msg {
		sf, err := device.LoadStateFile(path)
		if err != nil {
			return loadCheckedMsg{req: loadRequest{path: path}, err: err}
		}
		warning, err := sf.Check(s.Model, s.DeviceInfo.Version)
		return loadCheckedMsg{req: loadRequest{path: path, sf: sf, warning: warning}, err: err}
	}
}

// freshReadCmd makes the write gate's check half (ADR-0003): one fresh
// checked read of the Device, right before the session's first write.
func (m *Model) freshReadCmd(s *device.Session, req loadRequest) tea.Cmd {
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

// applyCmd is the write path of a load: the golden read first (when the gate
// asked for one — a failed golden save refuses the write, like the CLI),
// then the batched writes verified by reading the Device back (the proof is
// the read-back, never the write itself).
func (m *Model) applyCmd(s *device.Session, current device.State, req loadRequest) tea.Cmd {
	return func() tea.Msg {
		if req.golden != "" {
			golden := device.StateFileFor(s.Model, s.DeviceInfo.Version, current)
			if err := device.SaveStateFile(req.golden, golden); err != nil {
				return appliedMsg{req: req, err: fmt.Errorf("golden read not saved, refusing to write: %w", err)}
			}
		}
		back, diffs, err := s.ApplyVerified(context.Background(), req.sf.State)
		return appliedMsg{req: req, back: back, diffs: diffs, err: err, wrote: true}
	}
}

// updateSaved reports where the State File went (or why it did not).
func (m *Model) updateSaved(msg savedMsg) (tea.Model, tea.Cmd) {
	m.busy = false
	m.notice = nil
	if msg.err != nil {
		m.status = "error: " + msg.err.Error()
		return m, nil
	}
	m.status = fmt.Sprintf("saved %s state (firmware %s) to %s",
		m.session.Model.Name, m.session.DeviceInfo.Version, msg.path)
	return m, nil
}

// updateLoadChecked routes a checked State File: refusal stays a refusal,
// otherwise the write gate asks before this session's first write — behind a
// fresh checked read of the Device (ADR-0003).
func (m *Model) updateLoadChecked(msg loadCheckedMsg) (tea.Model, tea.Cmd) {
	m.busy = false
	m.notice = nil
	if msg.err != nil {
		m.status = "error: " + msg.err.Error()
		return m, nil
	}
	if m.wrote {
		// The gate already passed for this session: no prompt, no new
		// golden read (it is the session's FIRST write that is gated).
		m.busy = true
		return m, m.applyCmd(m.session, m.state, msg.req)
	}
	m.busy = true
	return m, m.freshReadCmd(m.session, msg.req)
}

// updateRefreshed routes the fresh checked read: a Device that fails its
// self-checks is never written (ADR-0003), and what it just reported becomes
// the state on screen — and the golden read's content.
func (m *Model) updateRefreshed(msg refreshedMsg) (tea.Model, tea.Cmd) {
	m.busy = false
	m.notice = nil
	if msg.err != nil {
		m.status = "error: refusing to load: " + msg.err.Error()
		return m, nil
	}
	m.state = msg.st
	m.checkErr = msg.check
	if msg.check != nil {
		m.status = "refusing to load: the Device must pass its self-checks before the first write (ADR-0003)"
		return m, nil
	}
	m.pending = msg.req
	m.golden = device.GoldenName(m.deps.Now())
	m.mode = modeGate
	return m, nil
}

// updateApplied reports the load: the read-back diff (or its absence) and
// the Device's actual state as the new truth on screen.
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
	m.state = msg.back // the Device is the source of truth
	notice := []string{}
	for _, line := range []string{msg.req.warning, msg.req.note} {
		if line != "" {
			notice = append(notice, line)
		}
	}
	switch {
	case len(msg.diffs) > 0:
		notice = append(notice, device.DiffHeader(len(msg.diffs)))
		const shown = 8
		for i, d := range msg.diffs {
			if i == shown {
				notice = append(notice, fmt.Sprintf("  … and %d more", len(msg.diffs)-shown))
				break
			}
			notice = append(notice, "  "+d)
		}
		m.status = fmt.Sprintf("loaded %s onto the %s at %s (firmware %s) — read-back verification FAILED",
			msg.req.path, m.session.Model.Name, m.session.Info.Path, m.session.DeviceInfo.Version)
	default:
		m.status = fmt.Sprintf("loaded %s onto the %s at %s (firmware %s) — read-back verified: the Device matches the State File",
			msg.req.path, m.session.Model.Name, m.session.Info.Path, m.session.DeviceInfo.Version)
	}
	m.notice = notice
	return m, nil
}
