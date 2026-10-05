package tui

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/ht4w5/nutctl/internal/device"
	"github.com/ht4w5/nutctl/internal/fixture"
	"github.com/ht4w5/nutctl/internal/hid"
	"github.com/ht4w5/nutctl/internal/hidfake"
)

// The TUI is tested through both of the spec's seams at once: the fake
// Device carries every wire exchange (fixtures recorded from real hardware),
// and the frames the screens render are asserted as golden strings. A
// rendered frame is external behavior — layout regressions are invisible
// through the Transport seam alone.

// fixturesDir is resolved to an absolute path up front: the tests chdir into
// scratch directories (State Files are written to the CWD).
var fixturesDir = func() string {
	abs, err := filepath.Abs("../../testdata/captures")
	if err != nil {
		panic(err)
	}
	return abs
}()

func loadFixture(t *testing.T, cmd, name string) fixture.Exchange {
	t.Helper()
	x, err := fixture.Load(fixturesDir+"/"+cmd, name)
	if err != nil {
		t.Fatalf("load fixture %s/%s: %v", cmd, name, err)
	}
	return x
}

// readPathFixtures are the fixture exchanges of the full v0 read pass, in
// read order: identity, both keymaps, the Lighting Effect, Per-Key RGB and
// Settings.
func readPathFixtures(t *testing.T) []fixture.Exchange {
	t.Helper()
	return []fixture.Exchange{
		loadFixture(t, "get_device_info", "nut87"),
		loadFixture(t, "get_key", "nut87"),
		loadFixture(t, "get_fn_key", "nut87"),
		loadFixture(t, "get_led_effect", "nut87"),
		loadFixture(t, "get_custom_led_data", "nut87"),
		loadFixture(t, "get_game_mode", "nut87"),
	}
}

// setFixtures are the recorded write exchanges of one full batched apply, in
// the order `load` writes the blocks.
func setFixtures(t *testing.T) []fixture.Exchange {
	t.Helper()
	return []fixture.Exchange{
		loadFixture(t, "set_key", "nut87"),
		loadFixture(t, "set_fn_key", "nut87"),
		loadFixture(t, "set_led_effect", "nut87"),
		loadFixture(t, "set_custom_led_data", "nut87"),
		loadFixture(t, "set_game_mode", "nut87"),
	}
}

// fault corrupts bytes of one response report in a fixture exchange, e.g. to
// break the lighting check code or the firmware status.
type fault struct {
	cmd  string // fixture directory name (the exchange's cmd)
	res  int    // response report index within the exchange
	off  int    // byte offset within the report
	data []byte // replacement bytes
}

// nut87 returns a fake NUT87 that answers the full v0 read pass from the
// fixtures recorded off real hardware (64-byte reports), with optional
// response faults patched in.
func nut87(t *testing.T, path string, faults ...fault) *hidfake.Device {
	t.Helper()
	d := hidfake.New(hid.Info{
		Path:         path,
		VendorID:     0x0C45,
		ProductID:    0x880C,
		ProductName:  "NUT87",
		Manufacturer: "hfdic",
		UsagePage:    0xFF68,
		ReportLength: 64,
	})
	replayReadPath(d, t, faults...)
	return d
}

// replayReadPath scripts one full read pass (probe + the five blocks), with
// optional faults patched into the responses.
func replayReadPath(d *hidfake.Device, t *testing.T, faults ...fault) {
	t.Helper()
	for _, x := range readPathFixtures(t) {
		for _, f := range faults {
			if f.cmd != x.Cmd {
				continue
			}
			report := bytes.Clone(x.Responses[f.res])
			copy(report[f.off:], f.data)
			x.Responses[f.res] = report
		}
		d.Replay(x)
	}
}

// replaySets scripts one full batched apply: the recorded set_* exchanges,
// in the order a load writes the blocks.
func replaySets(d *hidfake.Device, t *testing.T) {
	t.Helper()
	for _, x := range setFixtures(t) {
		d.Replay(x)
	}
}

// loadSession scripts what one Load action runs against the Device: the
// fresh checked read behind the write gate (ADR-0003), the batched writes,
// and the read-back pass.
func loadSession(d *hidfake.Device, t *testing.T) {
	t.Helper()
	replayReadPath(d, t)
	replaySets(d, t)
	replayReadPath(d, t)
}

// testNow pins the clock the golden-read filename comes from, so the write
// gate's prompt is a golden string.
func testNow() time.Time { return time.Date(2026, 10, 5, 19, 30, 0, 0, time.UTC) }

const goldenName = "golden-20261005-193000.json"

func newModel(t *testing.T, enum hid.Enumerator) *Model {
	t.Helper()
	return New(Deps{
		Devices: enum,
		Stdin:   strings.NewReader(""),
		Stdout:  io.Discard,
		Stderr:  io.Discard,
		Now:     testNow,
	})
}

// key builds the key message a user press produces.
func key(s string) tea.Msg {
	switch s {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
	case "backspace":
		return tea.KeyMsg{Type: tea.KeyBackspace}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

// typeText types a string one key press at a time.
func typeText(s string) []tea.Msg {
	msgs := make([]tea.Msg, 0, len(s))
	for _, r := range s {
		msgs = append(msgs, key(string(r)))
	}
	return msgs
}

// drive sends messages through the model, running every command it returns
// synchronously and feeding the command's message back — one deterministic
// session, with every wire exchange running against the fake Device.
func drive(t *testing.T, m *Model, msgs ...tea.Msg) {
	t.Helper()
	for _, msg := range msgs {
		any, cmd := m.Update(msg)
		m = any.(*Model)
		runCmd(t, m, cmd)
	}
}

func runCmd(t *testing.T, m *Model, cmd tea.Cmd) {
	t.Helper()
	for cmd != nil {
		msg := cmd()
		if msg == nil {
			return
		}
		switch msg := msg.(type) {
		case tea.QuitMsg:
			return
		case tea.BatchMsg:
			for _, c := range msg {
				runCmd(t, m, c)
			}
			return
		}
		var next tea.Cmd
		var any tea.Model
		any, next = m.Update(msg)
		m = any.(*Model)
		cmd = next
	}
}

var ansiRE = regexp.MustCompile("\x1b\\[[0-9;:?]*[a-zA-Z]")

// frame is the rendered frame with ANSI styling stripped: what the frame
// SAYS, independent of terminal color support. (One caret survives the
// strip: textinput's cursor is a reversed space after the typed value, so a
// prompt line ends with one space.)
func frame(m *Model) string { return ansiRE.ReplaceAllString(m.View(), "") }

// wantFrame asserts the model renders exactly this frame.
func wantFrame(t *testing.T, m *Model, want string) {
	t.Helper()
	got := frame(m)
	want = strings.TrimPrefix(want, "\n")
	if got != want {
		t.Errorf("frame:\ngot:\n%q\nwant:\n%q\n--- got ---\n%s\n--- want ---\n%s", got, want, got, want)
	}
}

// wantStatus asserts the status line contains s (for flows where the frame
// around it is asserted elsewhere).
func wantStatus(t *testing.T, m *Model, s string) {
	t.Helper()
	if !strings.Contains(frame(m), "status: "+s) {
		t.Errorf("status line missing %q:\n%s", s, frame(m))
	}
}

// --- the shell ---

// Bare frames are golden strings: the header, the tab bar (the active screen
// bracketed), the screen body, the status bar and the help line (spec user
// story 36 — layout regressions are caught here, not through the Transport
// seam).
func TestDeviceScreenFrame(t *testing.T) {
	d := nut87(t, "/dev/hidraw3")
	m := newModel(t, &hidfake.Enumerator{Devices: []*hidfake.Device{d}})

	wantFrame(t, m, `
nutctl — NUT87 at /dev/hidraw3 (firmware 1.20)
[1 Device]  2 Keys  3 Lighting  4 Settings

Device
  Model:           NUT87
  Connection:      USB — connected (/dev/hidraw3)
  Device Identity: 0c45:880c "NUT87" (manufacturer 32, product 2)
  Firmware:        1.20
  Firmware status: ok
  Report Rate:     8K
  Battery:         0% (charge status 2)

  State Files: [s] save current state   [l] load a State File onto the Device

status: ready
help: 1-4/tab switch screen · s save · l load · a apply · r revert · q quit`)
}

// The four screens are navigable with `1`–`4` and `tab` (spec acceptance);
// `tab` cycles through all four and wraps.
func TestScreensAreNavigable(t *testing.T) {
	d := nut87(t, "/dev/hidraw3")
	m := newModel(t, &hidfake.Enumerator{Devices: []*hidfake.Device{d}})

	drive(t, m, key("2"))
	// The Keys screen's own frames live in keys_test.go; here the shell's
	// contract is the tab bar and the navigation.
	if got := frame(m); !strings.Contains(got, "1 Device  [2 Keys]  3 Lighting  4 Settings") {
		t.Errorf("`2` must jump to the Keys screen:\n%s", got)
	}

	drive(t, m, key("4"))
	if got := frame(m); !strings.Contains(got, "1 Device  2 Keys  3 Lighting  [4 Settings]") {
		t.Errorf("`4` must jump to the Settings screen:\n%s", got)
	}
	drive(t, m, key("tab")) // wraps back to Device
	if got := frame(m); !strings.Contains(got, "[1 Device]") {
		t.Errorf("`tab` must wrap from Settings to Device:\n%s", got)
	}
	drive(t, m, key("tab"), key("tab"))
	if got := frame(m); !strings.Contains(got, "[3 Lighting]") {
		t.Errorf("`tab` must cycle screens:\n%s", got)
	}
}

func TestQuit(t *testing.T) {
	d := nut87(t, "/dev/hidraw3")
	m := newModel(t, &hidfake.Enumerator{Devices: []*hidfake.Device{d}})

	// `q` quits: the model's output to the program is the Quit message —
	// its exit code (the program-level exit is pinned by
	// TestBareOpensTheTUI in internal/cli).
	any, cmd := m.Update(key("q"))
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Error("q must quit the TUI")
	}
	if got := any.(*Model).View(); got != "" {
		t.Errorf("a quitting TUI renders nothing, got:\n%s", got)
	}
}

// --- read-only states (ADR-0003: visible, never silent) ---

// A Device that fails its self-checks is read-only — and the state stays on
// screen with every failure line, because that is what diagnosis needs.
func TestSelfCheckFailureBanner(t *testing.T) {
	d := hidfake.New(hid.Info{
		Path: "/dev/hidraw3", VendorID: 0x0C45, ProductID: 0x880C,
		ProductName: "NUT87", Manufacturer: "hfdic", UsagePage: 0xFF68, ReportLength: 64,
	})
	// Lighting Effect offsets 14..15 must be 0xaa 0x55 or 0x00 0x00.
	replayReadPath(d, t, fault{cmd: "GET_LED_EFFECT", res: 0, off: 22, data: []byte{0xAB, 0x01}})
	m := newModel(t, &hidfake.Enumerator{Devices: []*hidfake.Device{d}})

	wantFrame(t, m, `
nutctl — NUT87 at /dev/hidraw3 (firmware 1.20)
[1 Device]  2 Keys  3 Lighting  4 Settings

READ-ONLY: the Device failed its self-checks — writes are refused until they pass (ADR-0003)
self-check failed: lighting check code is 0xAB 0x01 at offsets 14..15, want 0xAA 0x55 (written by the vendor app's SET path) or 0x00 0x00 (factory/unwritten) — the read is misaligned or the block is corrupt

Device
  Model:           NUT87
  Connection:      USB — connected (/dev/hidraw3)
  Device Identity: 0c45:880c "NUT87" (manufacturer 32, product 2)
  Firmware:        1.20
  Firmware status: ok
  Report Rate:     8K
  Battery:         0% (charge status 2)

  State Files: [s] save current state   [l] load — refused while read-only

status: ready
help: 1-4/tab switch screen · s save · l load · a apply · r revert · q quit`)
}

// A bootloader Device is clearly read-only (spec acceptance): the banner
// says so, Load is refused with that reason, and nothing is written.
func TestBootloaderDeviceIsReadOnly(t *testing.T) {
	d := hidfake.New(hid.Info{
		Path: "/dev/hidraw3", VendorID: 0x0C45, ProductID: 0x880C,
		ProductName: "NUT87", Manufacturer: "hfdic", UsagePage: 0xFF68, ReportLength: 64,
	})
	// firmwareStatus lives at data offset 32 = report offset 40 of the
	// GET_DEVICE_INFO response.
	replayReadPath(d, t, fault{cmd: "GET_DEVICE_INFO", res: 0, off: 40, data: []byte{1}})
	enum := &hidfake.Enumerator{Devices: []*hidfake.Device{d}}
	m := newModel(t, enum)

	wantFrame(t, m, `
nutctl — NUT87 at /dev/hidraw3 (firmware 1.20)
[1 Device]  2 Keys  3 Lighting  4 Settings

READ-ONLY: the Device reports bootloader/firmware-recovery state — writes are refused (ADR-0003)

Device
  Model:           NUT87
  Connection:      USB — connected (/dev/hidraw3)
  Device Identity: 0c45:880c "NUT87" (manufacturer 32, product 2)
  Firmware:        1.20
  Firmware status: bootloader (writes will be refused)
  Report Rate:     8K
  Battery:         0% (charge status 2)

  State Files: [s] save current state   [l] load — refused while read-only

status: ready
help: 1-4/tab switch screen · s save · l load · a apply · r revert · q quit`)

	before := len(d.Sent())
	drive(t, m, key("l"))
	wantStatus(t, m, "refusing to load: READ-ONLY")
	if got := len(d.Sent()); got != before {
		t.Errorf("a refused load touched the wire: %d → %d reports", before, got)
	}
}

// --- State Files: explicit Save and Load (ADR-0005) ---

// Save writes the State File only on enter — the prompt writes nothing.
func TestSaveWritesStateFileOnEnter(t *testing.T) {
	t.Chdir(t.TempDir())
	d := nut87(t, "/dev/hidraw3")
	m := newModel(t, &hidfake.Enumerator{Devices: []*hidfake.Device{d}})

	drive(t, m, key("s"))
	drive(t, m, typeText("state.json")...)
	wantFrame(t, m, `
nutctl — NUT87 at /dev/hidraw3 (firmware 1.20)
[1 Device]  2 Keys  3 Lighting  4 Settings

Save state to a State File
  written only where you ask (ADR-0005); the Device stays the source of truth

  path: state.json 

status: ready
help: enter confirm · esc cancel · ctrl+c quit`)

	if _, err := os.Stat("state.json"); !os.IsNotExist(err) {
		t.Fatalf("the prompt must not write the file (stat err = %v)", err)
	}

	drive(t, m, key("enter"))
	if _, err := os.Stat("state.json"); err != nil {
		t.Fatalf("enter must write the State File: %v", err)
	}
	sf, err := device.LoadStateFile("state.json")
	if err != nil {
		t.Fatalf("saved State File does not load: %v", err)
	}
	if sf.Model != "NUT87" || sf.Firmware != "1.20" || sf.Schema != device.SchemaCurrent {
		t.Errorf("State File envelope = %s / %s / %d, want NUT87 / 1.20 / %d",
			sf.Model, sf.Firmware, sf.Schema, device.SchemaCurrent)
	}
	if len(sf.State.Base) != 128 {
		t.Error("the State File must carry the full state")
	}
	wantStatus(t, m, "saved NUT87 state (firmware 1.20) to state.json")
}

// The write gate's golden-read prompt (ADR-0003) is the TUI twin of the
// CLI's: it offers to save the current state before the session's first
// write, and the name it offers is shown.
func TestLoadGatePromptFrame(t *testing.T) {
	t.Chdir(t.TempDir())
	d := nut87(t, "/dev/hidraw3")
	m := newModel(t, &hidfake.Enumerator{Devices: []*hidfake.Device{d}})
	saveStateFile(t, m, "state.json")
	replayReadPath(d, t) // the fresh checked read behind the gate

	drive(t, m, key("l"))
	drive(t, m, typeText("state.json")...)
	drive(t, m, key("enter"))

	wantFrame(t, m, `
nutctl — NUT87 at /dev/hidraw3 (firmware 1.20)
[1 Device]  2 Keys  3 Lighting  4 Settings

Write gate (ADR-0003) — the session's first write
  save current state to ./golden-20261005-193000.json? [Y/n]
  y — save the golden read, then load the State File
  n — skip the golden read, load the State File anyway
  esc — cancel the load, write nothing

status: saved NUT87 state (firmware 1.20) to state.json
help: y/enter save the golden read and load · n skip the golden read · esc cancel · ctrl+c quit`)
}

// y saves the golden read and loads: a golden State File appears, the
// batched writes reach the Device, and the read-back verification is what
// the status bar reports (spec user story 22).
func TestLoadWithGoldenRead(t *testing.T) {
	t.Chdir(t.TempDir())
	d := nut87(t, "/dev/hidraw3")
	enum := &hidfake.Enumerator{Devices: []*hidfake.Device{d}}
	m := newModel(t, enum)
	saveStateFile(t, m, "state.json")
	loadSession(d, t)

	drive(t, m, key("l"))
	drive(t, m, typeText("state.json")...)
	drive(t, m, key("enter"), key("y"))

	if _, err := os.Stat(goldenName); err != nil {
		t.Fatalf("y must save the golden read to ./%s: %v", goldenName, err)
	}
	if !sawCommand(d, 34 /* CmdSetKey */) {
		t.Error("the load never wrote to the Device")
	}
	wantStatus(t, m, "loaded state.json onto the NUT87 at /dev/hidraw3 (firmware 1.20) — read-back verified: the Device matches the State File")
	if got := frame(m); !strings.Contains(got, "golden read saved to ./"+goldenName) {
		t.Errorf("frame missing where the golden read went:\n%s", got)
	}
}

// n dismisses the golden read and loads anyway — the dismissible escape
// hatch of spec user story 5: a declined offer writes no file.
func TestLoadDeclinedGoldenRead(t *testing.T) {
	t.Chdir(t.TempDir())
	d := nut87(t, "/dev/hidraw3")
	m := newModel(t, &hidfake.Enumerator{Devices: []*hidfake.Device{d}})
	saveStateFile(t, m, "state.json")
	loadSession(d, t)

	drive(t, m, key("l"))
	drive(t, m, typeText("state.json")...)
	drive(t, m, key("enter"), key("n"))

	if _, err := os.Stat(goldenName); !os.IsNotExist(err) {
		t.Errorf("a declined offer must write no file (stat err = %v)", err)
	}
	if !sawCommand(d, 34) {
		t.Error("n must still load the State File")
	}
	if got := frame(m); !strings.Contains(got, "skipped the golden read") {
		t.Errorf("frame missing the dismissal:\n%s", got)
	}
}

// esc at the gate cancels the load: no file, no write (spec user story 7 —
// state files are things the user requests).
func TestLoadEscAtGateCancels(t *testing.T) {
	t.Chdir(t.TempDir())
	d := nut87(t, "/dev/hidraw3")
	m := newModel(t, &hidfake.Enumerator{Devices: []*hidfake.Device{d}})
	saveStateFile(t, m, "state.json")
	replayReadPath(d, t) // the fresh checked read still runs before the gate

	drive(t, m, key("l"))
	drive(t, m, typeText("state.json")...)
	drive(t, m, key("enter"), key("esc"))

	wantStatus(t, m, "load cancelled — nothing was written")
	if sawCommand(d, 34 /* CmdSetKey */) {
		t.Error("a cancelled load must not write to the Device")
	}
	if files, _ := filepath.Glob("*.json"); len(files) != 1 { // only state.json
		t.Errorf("a cancelled load must write no file, found %v", files)
	}
}

// A State File from another Model is refused before anything is written
// (spec user story 25) — and the refusal is on screen.
func TestLoadRefusesStateFileFromAnotherModel(t *testing.T) {
	t.Chdir(t.TempDir())
	d := nut87(t, "/dev/hidraw3")
	m := newModel(t, &hidfake.Enumerator{Devices: []*hidfake.Device{d}})
	// The Save action's own output, renamed to another Model: a valid State
	// File for the wrong board (spec user story 25).
	saveStateFile(t, m, "wrong.json")
	rewriteFile(t, "wrong.json", `"model": "NUT87"`, `"model": "NUT75"`)

	before := len(d.Sent())
	drive(t, m, key("l"))
	drive(t, m, typeText("wrong.json")...)
	drive(t, m, key("enter"))

	wantStatus(t, m, "error: wrong Model: this State File is for a NUT75, this Device is a NUT87 — refusing to load")
	if got := len(d.Sent()); got != before {
		t.Errorf("a refused load touched the wire: %d → %d reports", before, got)
	}
}

// A State File saved from another firmware warns but proceeds (spec user
// story 26) — the warning is on screen through the load.
func TestLoadWarnsOnFirmwareMismatchButProceeds(t *testing.T) {
	t.Chdir(t.TempDir())
	d := nut87(t, "/dev/hidraw3")
	m := newModel(t, &hidfake.Enumerator{Devices: []*hidfake.Device{d}})
	saveStateFile(t, m, "old.json")
	rewriteFile(t, "old.json", `"firmware": "1.20"`, `"firmware": "1.19"`)
	loadSession(d, t)

	drive(t, m, key("l"))
	drive(t, m, typeText("old.json")...)
	drive(t, m, key("enter"), key("y"))

	if got := frame(m); !strings.Contains(got, "warning: State File was saved from firmware 1.19, this Device reports 1.20 — proceeding anyway") {
		t.Errorf("frame missing the firmware-mismatch warning:\n%s", got)
	}
	wantStatus(t, m, "loaded old.json onto the NUT87 at /dev/hidraw3 (firmware 1.20) — read-back verified: the Device matches the State File")
}

// The session's first write requires the self-checks passing (ADR-0003): a
// Device that fails them is never written to — and the failure is visible.
func TestLoadFirstWriteRequiresSelfChecks(t *testing.T) {
	t.Chdir(t.TempDir())
	d := nut87(t, "/dev/hidraw3")
	m := newModel(t, &hidfake.Enumerator{Devices: []*hidfake.Device{d}})
	saveStateFile(t, m, "state.json")
	// The fresh checked read behind the gate comes back corrupt: the
	// lighting check code is neither written nor factory/unwritten.
	replayReadPath(d, t, fault{cmd: "GET_LED_EFFECT", res: 0, off: 22, data: []byte{0xAB, 0x01}})

	drive(t, m, key("l"))
	drive(t, m, typeText("state.json")...)
	drive(t, m, key("enter"))

	wantStatus(t, m, "refusing to load: the Device must pass its self-checks before the first write (ADR-0003)")
	if got := frame(m); !strings.Contains(got, "READ-ONLY: the Device failed its self-checks") {
		t.Errorf("frame missing the read-only banner:\n%s", got)
	}
	if sawCommand(d, 34 /* CmdSetKey */) {
		t.Error("a Device failing its self-checks must not be written")
	}
}

// --- errors: visible and actionable (spec user story 29) ---

func TestPermissionErrorScreen(t *testing.T) {
	d := hidfake.New(hid.Info{
		Path: "/dev/hidraw3", VendorID: 0x0C45, ProductID: 0x880C,
		ProductName: "NUT87", UsagePage: 0xFF68, ReportLength: 64,
	})
	d.OpenErr = hid.ErrPermission
	m := newModel(t, &hidfake.Enumerator{Devices: []*hidfake.Device{d}})

	wantFrame(t, m, `
nutctl — no Device

error: open /dev/hidraw3: permission denied opening hidraw device
hint: install the udev rule so an unprivileged user can open the Device:
  sudo cp udev/60-nut87.rules /etc/udev/rules.d/
  sudo udevadm control --reload && sudo udevadm trigger
then re-plug the keyboard

press q to quit`)
}

func TestDeviceBusyErrorScreen(t *testing.T) {
	d := hidfake.New(hid.Info{
		Path: "/dev/hidraw3", VendorID: 0x0C45, ProductID: 0x880C,
		ProductName: "NUT87", UsagePage: 0xFF68, ReportLength: 64,
	})
	d.OpenErr = hid.ErrBusy
	m := newModel(t, &hidfake.Enumerator{Devices: []*hidfake.Device{d}})

	wantFrame(t, m, `
nutctl — no Device

error: open /dev/hidraw3: device busy
hint: another nutctl session (or another tool) is reading this Device — close it and retry (also quit the vendor app if it is running)

press q to quit`)
}

func TestWrongModelDeviceScreen(t *testing.T) {
	d := hidfake.New(hid.Info{
		Path: "/dev/hidraw4", VendorID: 0x0C45, ProductID: 0x880C,
		ProductName: "NUT75", UsagePage: 0xFF68, ReportLength: 64,
	})
	m := newModel(t, &hidfake.Enumerator{Devices: []*hidfake.Device{d}})

	wantFrame(t, m, `
nutctl — no Device

error: wrong Model: this is a NUT75 (USB 0c45:880c "NUT75"); this build only supports NUT87 — refusing to configure

press q to quit`)
}

// --- helpers over the fake ---

// saveStateFile puts the Device's current state in a State File at path via
// the Save action — the seed a load test loads.
func saveStateFile(t *testing.T, m *Model, path string) {
	t.Helper()
	drive(t, m, key("s"))
	drive(t, m, typeText(path)...)
	drive(t, m, key("enter"))
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("Save action did not write %s: %v", path, err)
	}
}

// rewriteFile swaps one string in a file: the way these tests forge State
// Files (another Model's name, another firmware) without reaching into the
// model.
func rewriteFile(t *testing.T, path, old, replacement string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte(old)) {
		t.Fatalf("%s does not contain %q", path, old)
	}
	if err := os.WriteFile(path, bytes.Replace(raw, []byte(old), []byte(replacement), 1), 0o644); err != nil {
		t.Fatal(err)
	}
}

// sawCommand reports whether the fake received a request report for cmd
// (the protocol command byte at offset 1 of the request header).
func sawCommand(d *hidfake.Device, cmd byte) bool {
	for _, r := range d.Sent() {
		if len(r) > 1 && r[1] == cmd {
			return true
		}
	}
	return false
}
