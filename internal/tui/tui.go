// Package tui is the interactive front door of nutctl (ADR-0004): a Bubble
// Tea program with four tabbed screens — Device, Keys, Lighting, Settings —
// over internal/device state. The Device screen introduces the Device and is
// where State Files are explicitly saved and loaded (ADR-0005); read-only
// states and errors are visible, never silent (ADR-0003).
//
// The TUI is a view over the device layer, not a second source of truth: it
// renders the state one read pass produced plus ONE local edit buffer, and
// emits intents (save, load, apply) that go through the same
// device.Session, write gate and read-back verification as the CLI. The
// edit mechanics — local buffer, pending changes in the status bar, `a`
// apply, `r` revert — are shared by every edit screen (edit.go). Every
// screen renders as a plain frame, so tests drive it with key messages
// against the fake Device and assert golden frames (the spec's second test
// seam).
package tui

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/cursor"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/ht4w5/nutctl/internal/device"
	"github.com/ht4w5/nutctl/internal/hid"
	"github.com/ht4w5/nutctl/internal/protocol"
)

// Deps carries the seams the TUI runs against.
type Deps struct {
	Devices hid.Enumerator
	Stdin   io.Reader        // tea input (nil: the terminal)
	Stdout  io.Writer        // tea output (nil: the terminal)
	Stderr  io.Writer        // only for failures to start the program itself
	Now     func() time.Time // clock for the golden-read filename (tests pin it)
}

// The four screens (PLAN Phase 5), in `1`–`4` order: the Device screen, the
// Keys screen, the Lighting screen (both halves of lighting, ticket 07) and
// the Settings screen — the shell, the Device screen, the State File
// actions and every edit screen's shared edit mechanics are this build's
// surface.
type screen int

const (
	screenDevice screen = iota
	screenKeys
	screenLighting
	screenSettings
)

var screenNames = []string{"Device", "Keys", "Lighting", "Settings"}

// mode is what the keyboard currently drives: the normal screen, a State
// File path prompt, the write gate's golden-read prompt, the Keys screen's
// rebind picker, or the Lighting screen's color editor.
type mode int

const (
	modeNormal mode = iota
	modeSavePath
	modeLoadPath
	modeGate
	modeBind
	modeColor
)

// Styles. Frames render plain under a non-color terminal (and in tests),
// where the brackets on the active tab and the banner words still say
// everything the color says.
var (
	headerStyle = lipgloss.NewStyle().Bold(true)
	tabStyle    = lipgloss.NewStyle().Bold(true).Reverse(true)
	bannerStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("1"))
)

// Model is the TUI: one session's state plus what the keyboard is doing to
// it. Mutating Update on *Model keeps one instance for the whole program
// (tea.Model).
type Model struct {
	deps Deps

	session  *device.Session    // nil: no Device (openErr names why)
	state    device.State       // the last full read pass (the Device's actual state)
	local    device.State       // the local edit buffer: pending until `a` (edit.go)
	checkErr *device.CheckError // self-check failure, shown and gating writes
	openErr  error              // why there is no session

	screen screen
	mode   mode

	ti      textinput.Model // the State File path prompt
	request writeRequest    // the write waiting at (or past) the gate
	golden  string          // filename the write gate offers for the golden read
	sel     settingRow      // the Settings screen's row cursor

	keyLayer keyLayer  // the Keys screen's Layer shown and edited
	keyRow   int       // the Keys screen's row cursor
	keyTop   int       // first table row in the window
	bind     bindState // the rebind picker's state (bind.go)

	light lightState // the Lighting screen's halves and cursors (lighting.go)
	color colorState // the color editor's state (lighting.go)

	notice []string // supporting lines of the last action (warnings, diffs)
	status string   // the status bar: what just happened / what is true now
	wrote  bool     // this session has passed the write gate (ADR-0003)
	busy   bool     // an action is running off the event loop: keys wait

	quitConfirm bool // `q` was pressed once with unsaved changes
	quitting    bool
}

// New opens the Device and makes ONE checked full read pass (the session
// startup every UI shares). A Device that cannot be opened or read becomes
// the error screen — never a silent exit; failed self-checks leave the state
// visible and mark the session read-only.
func New(deps Deps) *Model {
	if deps.Now == nil {
		deps.Now = time.Now
	}
	m := &Model{deps: deps, status: "ready"}
	m.ti = textinput.New()
	m.ti.Prompt = "  path: "
	m.ti.Cursor.SetMode(cursor.CursorStatic) // a fixed caret: no blink commands

	s, err := device.Open(deps.Devices, "")
	if err != nil {
		m.openErr = err
		return m
	}
	st, err := s.ReadChecked()
	if err != nil {
		var ce *device.CheckError
		if !errors.As(err, &ce) {
			s.Close()
			m.openErr = err
			return m
		}
		m.checkErr = ce
	}
	m.session = s
	m.state = st
	m.local = st
	return m
}

// Run opens the Device and runs the TUI until the user quits. The exit code
// is 0 for a session that ran, 1 when no Device could be opened (the reason
// is on screen the whole time) or the program itself failed.
func Run(deps Deps) int {
	m := New(deps)
	opts := []tea.ProgramOption{tea.WithOutput(deps.Stdout)}
	if deps.Stdin != nil {
		opts = append(opts, tea.WithInput(deps.Stdin))
	}
	if _, err := tea.NewProgram(m, opts...).Run(); err != nil {
		if deps.Stderr != nil {
			fmt.Fprintf(deps.Stderr, "nutctl: %v\n", err)
		}
		return 1
	}
	if m.openErr != nil {
		return 1
	}
	return 0
}

// Init starts the program. The session's read pass already ran in New, so
// every later action is a command of its own — nothing runs unattended.
func (m *Model) Init() tea.Cmd { return nil }

// Update is the one event loop (ADR-0004): key messages drive the shell, the
// path prompts and the write gate; result messages carry what the save/load
// commands did off the wire and to the disk.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			m.quitting = true
			return m, tea.Quit
		}
		if m.openErr != nil {
			if msg.String() == "q" {
				m.quitting = true
				return m, tea.Quit
			}
			return m, nil
		}
		// One action at a time: while a command runs off the event loop the
		// keyboard waits (ctrl+c above still quits) — one owner of the wire
		// (spec, Session model) and no overlapping applies.
		if m.busy {
			return m, nil
		}
		switch m.mode {
		case modeNormal:
			return m.updateNormal(msg)
		case modeSavePath, modeLoadPath:
			return m.updatePath(msg)
		case modeGate:
			return m.updateGate(msg)
		case modeBind:
			return m.updateBind(msg)
		case modeColor:
			return m.updateColor(msg)
		}
	case savedMsg:
		return m.updateSaved(msg)
	case loadCheckedMsg:
		return m.updateLoadChecked(msg)
	case refreshedMsg:
		return m.updateRefreshed(msg)
	case appliedMsg:
		return m.updateApplied(msg)
	}
	return m, nil
}

// updateNormal is the shell: screens with `1`–`4` and `tab`, the Device
// screen's Save/Load actions, the edit mechanics' `a`/`r` (edit.go), `q` to
// quit — then the active screen's own keys.
func (m *Model) updateNormal(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.String() != "q" {
		m.quitConfirm = false
	}
	switch msg.String() {
	case "q":
		if len(m.pendingDiffs()) > 0 && !m.quitConfirm {
			// Unsaved changes are never silently lost — not even on quit.
			m.quitConfirm = true
			m.notice = nil
			m.status = "unsaved changes will be lost — press q again to discard them, or r to revert"
			return m, nil
		}
		m.quitting = true
		return m, tea.Quit
	case "1", "2", "3", "4":
		m.screen = screen(msg.String()[0] - '1')
	case "tab":
		m.screen = (m.screen + 1) % screen(len(screenNames))
	case "shift+tab":
		m.screen = (m.screen + screen(len(screenNames)) - 1) % screen(len(screenNames))
	case "s":
		m.enterPath(modeSavePath)
	case "l":
		if reason := m.readOnlyReason(); reason != "" {
			m.status = "refusing to load: " + reason
			m.notice = nil
			return m, nil
		}
		if n := len(m.pendingDiffs()); n > 0 {
			// A load over pending edits would silently lose them (ticket 05).
			m.status = "refusing to load: " + plural(n, "pending change") + " — apply or revert it first"
			m.notice = nil
			return m, nil
		}
		m.enterPath(modeLoadPath)
	case "a":
		return m.startApply()
	case "r":
		m.revert()
		return m, nil
	default:
		switch m.screen {
		case screenKeys:
			return m.updateKeys(msg)
		case screenLighting:
			return m.updateLighting(msg)
		case screenSettings:
			return m.updateSettings(msg)
		}
	}
	return m, nil
}

// enterPath opens a State File path prompt. Nothing is written or read until
// the user types a path and presses enter (ADR-0005).
func (m *Model) enterPath(md mode) {
	m.mode = md
	m.ti.SetValue("")
	m.ti.Focus()
}

// --- rendering ---

// View renders one frame. Every screen renders here, so a test can assert it
// as a golden string (the second test seam).
func (m *Model) View() string {
	if m.quitting {
		return ""
	}
	if m.openErr != nil {
		return m.errorView()
	}
	lines := []string{
		headerStyle.Render(fmt.Sprintf("nutctl — %s at %s (firmware %s)",
			m.session.Model.Name, m.session.Info.Path, m.session.DeviceInfo.Version)),
		m.tabBar(),
		"",
	}
	if reason := m.readOnlyReason(); reason != "" {
		lines = append(lines, bannerStyle.Render(reason))
		lines = append(lines, m.readOnlyDetail()...)
		lines = append(lines, "")
	}
	lines = append(lines, m.body()...)
	lines = append(lines, "")
	lines = append(lines, m.notice...)
	if p := m.pendingLine(); p != "" {
		lines = append(lines, p)
	}
	status := "status: " + m.status
	if m.busy {
		status += " — working…"
	}
	lines = append(lines, status, "help: "+m.help())
	return strings.Join(lines, "\n")
}

// tabBar renders the four screens; the active one is bracketed (and styled)
// so the frame says which screen it is without color.
func (m *Model) tabBar() string {
	parts := make([]string, len(screenNames))
	for i, name := range screenNames {
		item := fmt.Sprintf("%d %s", i+1, name)
		if screen(i) == m.screen {
			item = tabStyle.Render("[" + item + "]")
		}
		parts[i] = item
	}
	return strings.Join(parts, "  ")
}

// readOnlyReason is why this session must not write to the Device (ADR-0003:
// reads before writes), or "" when it may. The bootloader/firmware-recovery
// state is always read-only; until the self-checks pass, so is every other
// Device.
func (m *Model) readOnlyReason() string {
	switch {
	case m.session.DeviceInfo.FirmwareStatus == protocol.FirmwareBootloader:
		return "READ-ONLY: the Device reports bootloader/firmware-recovery state — writes are refused (ADR-0003)"
	case m.session.DeviceInfo.FirmwareStatus != protocol.FirmwareOK:
		return fmt.Sprintf("READ-ONLY: the Device reports firmware status %s — writes are refused (ADR-0003)",
			protocol.FirmwareStatusText(m.session.DeviceInfo.FirmwareStatus))
	case m.checkErr != nil:
		return "READ-ONLY: the Device failed its self-checks — writes are refused until they pass (ADR-0003)"
	}
	return ""
}

// readOnlyDetail is the evidence under the banner: every self-check failure
// line, verbatim (errors are visible, never silent).
func (m *Model) readOnlyDetail() []string {
	if m.checkErr == nil {
		return nil
	}
	return strings.Split(m.checkErr.Error(), "\n")
}

// body renders the active screen — or the prompt that currently owns the
// keyboard.
func (m *Model) body() []string {
	switch m.mode {
	case modeSavePath:
		return m.saveBody()
	case modeLoadPath:
		return m.loadBody()
	case modeGate:
		return m.gateBody()
	case modeBind:
		return m.bindBody()
	case modeColor:
		return m.colorBody()
	}
	switch m.screen {
	case screenDevice:
		return m.deviceBody()
	case screenKeys:
		return m.keysBody()
	case screenLighting:
		return m.lightingBody()
	case screenSettings:
		return m.settingsBody()
	}
	return nil
}

func (m *Model) help() string {
	switch m.mode {
	case modeSavePath, modeLoadPath:
		return "enter confirm · esc cancel · ctrl+c quit"
	case modeGate:
		return "y/enter save the golden read and " + m.request.kind.verb + " · n skip the golden read · esc cancel · ctrl+c quit"
	case modeBind:
		return "enter bind · esc cancel · ctrl+c quit"
	case modeColor:
		return "enter set · esc cancel · ctrl+c quit"
	}
	return "1-4/tab switch screen · s save · l load · a apply · r revert · q quit"
}

// plural spells counts for the status bar: "1 change" / "2 changes".
func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

// errorView is what a Device that cannot be opened looks like: the error and
// its actionable hint, the whole time (spec user story 29).
func (m *Model) errorView() string {
	lines := []string{
		headerStyle.Render("nutctl — no Device"),
		"",
		"error: " + m.openErr.Error(),
	}
	if hint := hid.Hint(m.openErr); hint != "" {
		lines = append(lines, hint)
	}
	lines = append(lines, "", "press q to quit")
	return strings.Join(lines, "\n")
}
