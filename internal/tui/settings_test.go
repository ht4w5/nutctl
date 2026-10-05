package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/ht4w5/nutctl/internal/hidfake"
	"github.com/ht4w5/nutctl/internal/protocol"
)

// The Settings screen and the edit mechanics every later edit screen shares
// (ticket 05): one local edit buffer over the Device's state, pending changes
// in the status bar, `a` to apply through the write gate with read-back
// verification, `r` to revert from the Device's actual state. Tests run
// through the two agreed seams: key messages in / frames out, and every byte
// that reaches the fake Device.

// cmdSetGameMode is the SET command of the Settings block (docs/protocol.md
// §3); the other SET commands pin that an apply writes ONLY edited blocks.
const (
	cmdSetGameMode       = 33
	cmdSetKey            = 34
	cmdSetLEDEffect      = 35
	cmdSetCustomLEDData  = 36
	cmdSetFnKey          = 38
	settingsReportRateAt = 13 // data offset 5 after the 8-byte request header
)

// gameModeFault makes every scripted GET_GAME_MODE answer report rate: the
// fake Device holds no state, so what it "reports back" is what the test
// scripts.
func gameModeFault(rate protocol.ReportRate) fault {
	return fault{cmd: "GET_GAME_MODE", res: 0, off: settingsReportRateAt, data: []byte{byte(rate)}}
}

// scriptApplyWrite scripts the write half of one apply of Settings edits: the
// SET_GAME_MODE exchange (wildcard request — the tests assert the bytes that
// go out) and a read-back pass reporting backRate.
func scriptApplyWrite(d *hidfake.Device, t *testing.T, backRate protocol.ReportRate) {
	t.Helper()
	x := loadFixture(t, "set_game_mode", "nut87")
	d.Script(nil, x.Responses[0])
	replayReadPath(d, t, gameModeFault(backRate))
}

// scriptApply scripts one whole apply behind the write gate (ADR-0003): the
// fresh checked read reporting haveRate, then the write half reporting
// backRate on read-back.
func scriptApply(d *hidfake.Device, t *testing.T, haveRate, backRate protocol.ReportRate) {
	t.Helper()
	replayReadPath(d, t, gameModeFault(haveRate))
	scriptApplyWrite(d, t, backRate)
}

// setGameModePayload is the Settings payload of the SET_GAME_MODE request the
// fake received — the bytes the wire encoding acceptance is asserted on.
func setGameModePayload(t *testing.T, d *hidfake.Device) []byte {
	t.Helper()
	for _, r := range d.Sent() {
		if len(r) > settingsReportRateAt && r[1] == cmdSetGameMode {
			return r[8:]
		}
	}
	t.Fatal("no SET_GAME_MODE request reached the Device")
	return nil
}

// --- the Settings screen ---

// The Settings screen shows the four editable settings with the values the
// Device reports (golden frame, spec user story 36).
func TestSettingsScreenFrame(t *testing.T) {
	d := nut87(t, "/dev/hidraw3")
	m := newModel(t, &hidfake.Enumerator{Devices: []*hidfake.Device{d}})

	drive(t, m, key("4"))
	wantFrame(t, m, `
nutctl — NUT87 at /dev/hidraw3 (firmware 1.20)
1 Device  2 Keys  3 Lighting  [4 Settings]

Settings
  ↑/↓ select · ←/→ change · * = differs from the Device — a applies, r reverts
> Report Rate   8K
  Key delay     3
  Sleep         5 min
  Fn switch     off

status: ready
help: 1-4/tab switch screen · s save · l load · a apply · r revert · q quit`)
}

// Every row edits locally: Report Rate cycles the Model's offered set, key
// delay and sleep step inside the vendor bundle's domains (1..5 levels,
// 0..30 minutes), the Fn switch toggles. The frame is the golden proof — and
// nothing touches the wire (spec user story 19).
func TestSettingsEditAllFourSettings(t *testing.T) {
	d := nut87(t, "/dev/hidraw3")
	m := newModel(t, &hidfake.Enumerator{Devices: []*hidfake.Device{d}})

	drive(t, m, key("4"))
	before := len(d.Sent())
	drive(t, m,
		key("right"),              // Report Rate 8K → 1K (wraps the Model's set)
		key("down"), key("right"), // key delay 3 → 4
		key("down"), key("right"), // sleep 5 → 6 minutes
		key("down"), key("right"), // Fn switch off → on
	)

	wantFrame(t, m, `
nutctl — NUT87 at /dev/hidraw3 (firmware 1.20)
1 Device  2 Keys  3 Lighting  [4 Settings]

Settings
  ↑/↓ select · ←/→ change · * = differs from the Device — a applies, r reverts
  Report Rate   1K *
  Key delay     4 *
  Sleep         6 min *
> Fn switch     on *

pending: 4 changes — settings report rate: 8K → 1K · settings Fn switch: 0 → 1 · settings sleep time: 5 → 6 · and 1 more
status: ready
help: 1-4/tab switch screen · s save · l load · a apply · r revert · q quit`)
	if got := len(d.Sent()); got != before {
		t.Errorf("editing locally must not touch the wire: %d → %d reports", before, got)
	}
}

// The editors stay inside the domains the vendor bundle's own settings UI
// uses (docs/protocol.md §4): key delay is a level 1..5, sleep is minutes
// 0..30 with 0 = never.
func TestSettingsSteppingStaysInDomain(t *testing.T) {
	d := nut87(t, "/dev/hidraw3")
	m := newModel(t, &hidfake.Enumerator{Devices: []*hidfake.Device{d}})

	drive(t, m, key("4"))
	drive(t, m, key("down"), key("right"), key("right"), key("right")) // key delay 3 → 5 (clamped)
	drive(t, m, key("down"), key("left"), key("left"), key("left"))    // sleep 5 → 2
	drive(t, m, key("left"), key("left"), key("left"))                 // sleep 2 → 0 (never), clamped

	got := frame(m)
	for _, want := range []string{"Key delay     5 *", "Sleep         off *"} {
		if !strings.Contains(got, want) {
			t.Errorf("frame missing %q:\n%s", want, got)
		}
	}
}

// Edits survive switching screens — they are the session's one edit buffer,
// not per-screen scratch (ticket 05 acceptance: never silently lost when
// switching screens).
func TestSettingsEditsSurviveScreenSwitch(t *testing.T) {
	d := nut87(t, "/dev/hidraw3")
	m := newModel(t, &hidfake.Enumerator{Devices: []*hidfake.Device{d}})

	drive(t, m, key("4"), key("right")) // Report Rate 8K → 1K

	drive(t, m, key("1"))
	got := frame(m)
	if !strings.Contains(got, "[1 Device]") || !strings.Contains(got, "pending: 1 change — settings report rate: 8K → 1K") {
		t.Errorf("the Device screen must still show the pending change:\n%s", got)
	}

	drive(t, m, key("4"))
	if got := frame(m); !strings.Contains(got, "> Report Rate   1K *") {
		t.Errorf("the edit must survive the screen switch:\n%s", got)
	}
}

// --- apply (`a`): write gate, wire encoding, read-back verification ---

// `a` runs the same write gate as a load (ADR-0003) before the session's
// first write — golden frame, so the gate wording is pinned for this write
// too.
func TestApplyGoesThroughWriteGate(t *testing.T) {
	t.Chdir(t.TempDir())
	d := nut87(t, "/dev/hidraw3")
	m := newModel(t, &hidfake.Enumerator{Devices: []*hidfake.Device{d}})
	replayReadPath(d, t, gameModeFault(protocol.ReportRate8K)) // the gate's fresh checked read

	drive(t, m, key("4"), key("left")) // Report Rate 8K → 4K
	drive(t, m, key("a"))

	wantFrame(t, m, `
nutctl — NUT87 at /dev/hidraw3 (firmware 1.20)
1 Device  2 Keys  3 Lighting  [4 Settings]

Write gate (ADR-0003) — the session's first write
  save current state to ./golden-20261005-193000.json? [Y/n]
  y — save the golden read, then apply your pending edits
  n — skip the golden read, apply your pending edits anyway
  esc — cancel the apply, write nothing

pending: 1 change — settings report rate: 8K → 4K
status: ready
help: y/enter save the golden read and apply · n skip the golden read · esc cancel · ctrl+c quit`)
}

// An applied Report Rate uses the correct wire encoding and reads back as
// set (ticket 05 acceptance): only the edited Settings block is written
// (never the untouched blocks), the payload carries the wire enum
// (1K/4K/8K → 3/5/6), the read-back verifies clean, and the pending changes
// clear.
func TestApplyReportRateWireEncodingAndReadBack(t *testing.T) {
	cases := []struct {
		name  string
		have  protocol.ReportRate // what every read before the write reports
		press string              // one edit from have
		want  protocol.ReportRate // the edited rate
		wire  byte                // its wire enum (docs/protocol.md §4)
		back  protocol.ReportRate // what the read-back reports
		row   string
	}{
		{"8K → 1K", protocol.ReportRate8K, "right", protocol.ReportRate1K, 3, protocol.ReportRate1K,
			"> Report Rate   1K"},
		{"8K → 4K", protocol.ReportRate8K, "left", protocol.ReportRate4K, 5, protocol.ReportRate4K,
			"> Report Rate   4K"},
		{"4K → 8K", protocol.ReportRate4K, "right", protocol.ReportRate8K, 6, protocol.ReportRate8K,
			"> Report Rate   8K"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			d := nut87(t, "/dev/hidraw3", gameModeFault(tc.have))
			scriptApply(d, t, tc.have, tc.back)
			m := newModel(t, &hidfake.Enumerator{Devices: []*hidfake.Device{d}})

			drive(t, m, key("4"), key(tc.press), key("a"), key("y"))

			if got := setGameModePayload(t, d)[5]; got != tc.wire {
				t.Errorf("SET_GAME_MODE reportRate byte = %d, want %d (wire enum for %s)", got, tc.wire, tc.want)
			}
			for _, cmd := range []byte{cmdSetKey, cmdSetLEDEffect, cmdSetCustomLEDData, cmdSetFnKey} {
				if sawCommand(d, cmd) {
					t.Errorf("an apply of Settings edits must not send command %d", cmd)
				}
			}
			wantStatus(t, m, "applied your pending edits to the NUT87 at /dev/hidraw3 (firmware 1.20) — read-back verified: the Device matches your edits")
			got := frame(m)
			if !strings.Contains(got, tc.row+"\n") {
				t.Errorf("frame missing the applied row %q:\n%s", tc.row, got)
			}
			if strings.Contains(got, tc.row+" *") || strings.Contains(got, "pending:") {
				t.Errorf("a verified apply clears the pending changes:\n%s", got)
			}
			if _, err := os.Stat(goldenName); err != nil {
				t.Errorf("y must save the golden read to ./%s: %v", goldenName, err)
			}
		})
	}
}

// The gate's fresh read rebases the edit buffer (spec user story 19): an
// apply writes the user's changes onto what the Device reports NOW — a field
// the user never touched is never written back from a stale copy of an older
// read (that would be a change they never made, silently written).
func TestApplyWritesEditsOntoTheFreshestState(t *testing.T) {
	t.Chdir(t.TempDir())
	const keyDelayAt = 12 // data offset 4 after the 8-byte request header
	d := nut87(t, "/dev/hidraw3")
	// The fresh checked read behind the gate reports key delay 7 — a field
	// the user never edited (the startup read reported 3).
	keyDelayDrift := fault{cmd: "GET_GAME_MODE", res: 0, off: keyDelayAt, data: []byte{7}}
	replayReadPath(d, t, gameModeFault(protocol.ReportRate8K), keyDelayDrift)
	x := loadFixture(t, "set_game_mode", "nut87")
	d.Script(nil, x.Responses[0])
	replayReadPath(d, t, gameModeFault(protocol.ReportRate1K), keyDelayDrift)
	m := newModel(t, &hidfake.Enumerator{Devices: []*hidfake.Device{d}})

	drive(t, m, key("4"), key("right"), key("a"), key("y")) // edit Report Rate 8K → 1K

	payload := setGameModePayload(t, d)
	if payload[5] != 3 {
		t.Errorf("the user's edit must land (reportRate byte = %d, want 3)", payload[5])
	}
	if payload[4] != 7 {
		t.Errorf("an untouched field must follow the Device (keyDelay byte = %d, want 7, never the stale 3)", payload[4])
	}
	if got := frame(m); !strings.Contains(got, "read-back verified: the Device matches your edits") {
		t.Errorf("frame missing the verified apply:\n%s", got)
	}
}

// A read-back that does NOT report what was sent fails verification loudly —
// and the edits stay pending, never silently lost (ticket 05: unsaved
// changes are never silently lost; `r` is how they are dropped).
func TestApplyReadBackMismatchKeepsEditsPending(t *testing.T) {
	t.Chdir(t.TempDir())
	d := nut87(t, "/dev/hidraw3")
	scriptApply(d, t, protocol.ReportRate8K, protocol.ReportRate8K) // the Device reports the old rate back
	m := newModel(t, &hidfake.Enumerator{Devices: []*hidfake.Device{d}})

	drive(t, m, key("4"), key("right"), key("a"), key("y")) // 8K → 1K

	wantStatus(t, m, "apply to the NUT87 at /dev/hidraw3 (firmware 1.20) — read-back verification FAILED, your edits are still pending")
	got := frame(m)
	for _, want := range []string{
		"read-back verification: 1 difference(s) (your edits → Device):",
		"settings report rate: 1K → 8K",
		"pending: 1 change — settings report rate: 8K → 1K",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("frame missing %q:\n%s", want, got)
		}
	}
}

// The gate guards the session's FIRST write only: once passed, later applies
// write directly (and a declined golden read leaves no file behind).
func TestApplyAfterFirstWriteSkipsTheGate(t *testing.T) {
	t.Chdir(t.TempDir())
	d := nut87(t, "/dev/hidraw3")
	scriptApply(d, t, protocol.ReportRate8K, protocol.ReportRate1K)
	m := newModel(t, &hidfake.Enumerator{Devices: []*hidfake.Device{d}})

	drive(t, m, key("4"), key("right"), key("a"), key("n")) // first apply through the gate
	if files, _ := filepath.Glob("golden-*.json"); len(files) != 0 {
		t.Fatalf("a declined golden read must write no file, found %v", files)
	}

	scriptApplyWrite(d, t, protocol.ReportRate8K) // second apply: write + read-back only
	drive(t, m, key("left"), key("a"), key("y"))  // `y` must find no gate to answer

	wantStatus(t, m, "applied your pending edits to the NUT87 at /dev/hidraw3 (firmware 1.20) — read-back verified: the Device matches your edits")
	if files, _ := filepath.Glob("golden-*.json"); len(files) != 0 {
		t.Errorf("the second apply must not open the gate again (found %v)", files)
	}
}

// esc at the gate cancels the apply: nothing is written and the edits stay
// exactly as they were.
func TestApplyEscAtGateWritesNothing(t *testing.T) {
	t.Chdir(t.TempDir())
	d := nut87(t, "/dev/hidraw3")
	m := newModel(t, &hidfake.Enumerator{Devices: []*hidfake.Device{d}})
	replayReadPath(d, t, gameModeFault(protocol.ReportRate8K))

	drive(t, m, key("4"), key("right"), key("a"), key("esc"))

	wantStatus(t, m, "apply cancelled — nothing was written")
	if sawCommand(d, cmdSetGameMode) {
		t.Error("a cancelled apply must not write to the Device")
	}
	if got := frame(m); !strings.Contains(got, "pending: 1 change — settings report rate: 8K → 1K") {
		t.Errorf("a cancelled apply must keep the edits:\n%s", got)
	}
}

// Nothing to apply is a no-op with a saying status bar — and no gate, no
// write (unsaved changes are never silently written).
func TestApplyWithNothingPendingWritesNothing(t *testing.T) {
	t.Chdir(t.TempDir())
	d := nut87(t, "/dev/hidraw3")
	m := newModel(t, &hidfake.Enumerator{Devices: []*hidfake.Device{d}})

	before := len(d.Sent())
	drive(t, m, key("4"), key("a"))
	wantStatus(t, m, "nothing to apply — no pending changes")
	if got := len(d.Sent()); got != before {
		t.Errorf("an empty apply touched the wire: %d → %d reports", before, got)
	}
	if files, _ := filepath.Glob("*.json"); len(files) != 0 {
		t.Errorf("an empty apply must write no file, found %v", files)
	}
}

// A read-only Device is never written (ADR-0003) — the refusal names the
// reason, exactly like a refused load.
func TestApplyRefusedWhileReadOnly(t *testing.T) {
	d := nut87(t, "/dev/hidraw3", fault{cmd: "GET_DEVICE_INFO", res: 0, off: 40, data: []byte{1}})
	m := newModel(t, &hidfake.Enumerator{Devices: []*hidfake.Device{d}})

	drive(t, m, key("4"), key("right"))
	before := len(d.Sent())
	drive(t, m, key("a"))

	wantStatus(t, m, "refusing to apply: READ-ONLY")
	if got := len(d.Sent()); got != before {
		t.Errorf("a refused apply touched the wire: %d → %d reports", before, got)
	}
}

// --- revert (`r`) and the guards around unsaved changes ---

// `r` reverts from the Device's actual state: the edit buffer is dropped,
// nothing is written, and the frame shows the Device's values again.
func TestRevertRestoresDeviceState(t *testing.T) {
	d := nut87(t, "/dev/hidraw3")
	m := newModel(t, &hidfake.Enumerator{Devices: []*hidfake.Device{d}})

	drive(t, m, key("4"), key("left")) // Report Rate 8K → 4K
	before := len(d.Sent())
	drive(t, m, key("r"))

	wantStatus(t, m, "reverted 1 change from the Device's actual state")
	if got := len(d.Sent()); got != before {
		t.Errorf("revert must not touch the wire: %d → %d reports", before, got)
	}
	got := frame(m)
	if strings.Contains(got, "pending:") || !strings.Contains(got, "Report Rate   8K") {
		t.Errorf("after revert the frame shows the Device's state:\n%s", got)
	}
}

// Nothing to revert says so — `r` is never a silent no-op and never a write.
func TestRevertWithNothingPending(t *testing.T) {
	d := nut87(t, "/dev/hidraw3")
	m := newModel(t, &hidfake.Enumerator{Devices: []*hidfake.Device{d}})

	before := len(d.Sent())
	drive(t, m, key("4"), key("r"))
	wantStatus(t, m, "nothing to revert — no pending changes")
	if got := len(d.Sent()); got != before {
		t.Errorf("an empty revert touched the wire: %d → %d reports", before, got)
	}
}

// A load over pending edits would silently lose them — so it is refused
// until they are applied or reverted (ticket 05: never silently lost).
func TestLoadRefusedWhileChangesPending(t *testing.T) {
	d := nut87(t, "/dev/hidraw3")
	m := newModel(t, &hidfake.Enumerator{Devices: []*hidfake.Device{d}})

	drive(t, m, key("4"), key("right"))
	drive(t, m, key("l"))

	wantStatus(t, m, "refusing to load: 1 pending change — apply or revert it first")
	if got := frame(m); strings.Contains(got, "path:") {
		t.Errorf("the path prompt must not open while changes are pending:\n%s", got)
	}
}

// Quitting with unsaved changes is never silent: the first `q` asks, only the
// second discards.
func TestQuitAsksBeforeDiscardingChanges(t *testing.T) {
	d := nut87(t, "/dev/hidraw3")
	m := newModel(t, &hidfake.Enumerator{Devices: []*hidfake.Device{d}})
	drive(t, m, key("4"), key("right"))

	any, cmd := m.Update(key("q"))
	m = any.(*Model)
	if cmd != nil {
		t.Fatal("q with unsaved changes must ask before quitting")
	}
	wantStatus(t, m, "unsaved changes will be lost — press q again to discard them, or r to revert")
	if got := frame(m); !strings.Contains(got, "pending: 1 change") {
		t.Errorf("the first q must keep the edits:\n%s", got)
	}

	any, cmd = m.Update(key("q"))
	m = any.(*Model)
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Error("the second q must quit")
	}
}

// While a command runs off the event loop the keyboard waits — and the frame
// says so (the busy indicator ticket 04 deferred to 05).
func TestBusyIndicatorWhileApplying(t *testing.T) {
	d := nut87(t, "/dev/hidraw3")
	m := newModel(t, &hidfake.Enumerator{Devices: []*hidfake.Device{d}})
	drive(t, m, key("4"), key("right"))

	any, cmd := m.Update(key("a")) // start the apply, do not run it yet
	m = any.(*Model)
	if cmd == nil {
		t.Fatal("a must start the apply as a command")
	}
	if got := frame(m); !strings.Contains(got, "status: ready — working…") {
		t.Errorf("frame missing the busy indicator:\n%s", got)
	}
}

// The busy guard swallows keys while a command owns the wire (ticket 04's
// one-action-at-a-time rule), but never ctrl+c.
func TestBusySwallowsKeysButNotCtrlC(t *testing.T) {
	d := nut87(t, "/dev/hidraw3")
	m := newModel(t, &hidfake.Enumerator{Devices: []*hidfake.Device{d}})
	drive(t, m, key("4"), key("right"))

	any, cmd := m.Update(key("a"))
	m = any.(*Model)
	if any, cmd := m.Update(key("1")); cmd != nil {
		t.Error("keys must wait while a command runs")
	} else if got := any.(*Model).View(); !strings.Contains(got, "[4 Settings]") {
		t.Errorf("a swallowed key must not switch screens:\n%s", got)
	}
	any, cmd = m.Update(key("ctrl+c"))
	m = any.(*Model)
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Error("ctrl+c must still quit while busy")
	}
}

// A State File snapshots the Device's actual state (CONTEXT.md) — with
// edits pending, the file says so instead of leaving a silent surprise.
func TestSaveSnapshotsTheDeviceNotTheEdits(t *testing.T) {
	t.Chdir(t.TempDir())
	d := nut87(t, "/dev/hidraw3")
	m := newModel(t, &hidfake.Enumerator{Devices: []*hidfake.Device{d}})

	drive(t, m, key("4"), key("right")) // Report Rate 8K → 1K, locally
	saveStateFile(t, m, "state.json")

	wantStatus(t, m, "saved NUT87 state (firmware 1.20) to state.json — note: 1 pending change not saved (the file snapshots the Device)")
	raw, err := os.ReadFile("state.json")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"reportRate": "8K"`) {
		t.Errorf("the State File must snapshot the Device (8K), not the edit:\n%s", raw)
	}
}

// The fake keeps the wire honest even in setup: an unscripted request fails
// the test, so this sanity check pins that the apply tests really script
// what they claim (guards a silently-passing suite).
func TestApplyScriptCoversTheWire(t *testing.T) {
	t.Chdir(t.TempDir())
	d := nut87(t, "/dev/hidraw3")
	scriptApply(d, t, protocol.ReportRate8K, protocol.ReportRate1K)
	m := newModel(t, &hidfake.Enumerator{Devices: []*hidfake.Device{d}})

	drive(t, m, key("4"), key("right"), key("a"), key("y"))
	if !sawCommand(d, cmdSetGameMode) {
		t.Error("the scripted apply never reached the Device")
	}
	if _, err := os.Stat(goldenName); err != nil {
		t.Errorf("y must save the golden read: %v", err)
	}
}
