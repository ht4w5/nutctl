package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ht4w5/nutctl/internal/fixture"
	"github.com/ht4w5/nutctl/internal/hidfake"
)

// --- `nutctl fixtures record` (ticket 09) ---
//
// Recording becomes corpus-building: a live session against real hardware
// turns into committed fixtures that keep the offline tests honest (spec user
// story 34). These tests run the recorder through the Transport seam and
// require its output to be the corpus format byte for byte — recorded
// fixtures must drive the fake Device exactly like the committed ones do.

// recordedFixture loads one fixture the recorder or importer wrote under dir.
func recordedFixture(t *testing.T, dir, name string) fixture.Exchange {
	t.Helper()
	return recordedFixtureIn(t, "corpus", dir, name)
}

// recordedFixtureIn loads one such fixture from a chosen corpus directory.
func recordedFixtureIn(t *testing.T, corpus, dir, name string) fixture.Exchange {
	t.Helper()
	x, err := fixture.Load(filepath.Join(corpus, dir), name)
	if err != nil {
		t.Fatalf("recorded fixture %s/%s/%s: %v", corpus, dir, name, err)
	}
	return x
}

// assertSameWire checks a recorded exchange against the fixture it was
// recorded from: the wire bytes must be identical (the corpus format is the
// recording format — a drift here corrupts the corpus).
func assertSameWire(t *testing.T, recorded, committed fixture.Exchange) {
	t.Helper()
	if !reflect.DeepEqual(recorded.Requests, committed.Requests) {
		t.Errorf("%s: recorded request reports differ from the committed fixture", recorded.Case)
	}
	if !reflect.DeepEqual(recorded.Responses, committed.Responses) {
		t.Errorf("%s: recorded response reports differ from the committed fixture", recorded.Case)
	}
}

// The default session records the v0 read path (probe + one full checked
// read pass): one fixture per command, with the provenance metadata the
// ticket asks for — firmware version, connection type, capture method.
func TestFixturesRecordRecordsTheReadPath(t *testing.T) {
	t.Chdir(t.TempDir())
	d := newNut87Fake(t, "/dev/hidraw10")
	replayReadPath(d, t)
	enum := &hidfake.Enumerator{Devices: []*hidfake.Device{d}}

	code, out, errOut := run(t, enum, "fixtures", "record", "--out", "corpus")
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr: %s)", code, errOut)
	}
	if !strings.Contains(out, "recorded 6 exchange(s)") {
		t.Errorf("output does not report the recorded exchanges:\n%s", out)
	}

	for _, dir := range []string{
		"get_device_info", "get_key", "get_fn_key",
		"get_led_effect", "get_custom_led_data", "get_game_mode",
	} {
		got := recordedFixture(t, dir, "nut87") // default case name: the Model in lower case
		want := loadFixture(t, dir, "nut87")
		assertSameWire(t, got, want)
		if got.Cmd != want.Cmd {
			t.Errorf("%s: meta cmd = %q, want %q", dir, got.Cmd, want.Cmd)
		}
		for _, f := range []struct{ key, got, want string }{
			{"model", got.Meta.Model, "NUT87"},
			{"connection", got.Meta.Connection, "USB"},
			{"firmware", got.Meta.Firmware, "1.20"},
			{"captureMethod", got.Meta.CaptureMethod, "active-probing"},
		} {
			if f.got != f.want {
				t.Errorf("%s: meta %s = %q, want %q", dir, f.key, f.got, f.want)
			}
		}
		if !strings.Contains(got.Meta.Source, "docs/capture.md Method A") ||
			!strings.Contains(got.Meta.Source, "/dev/hidraw10") {
			t.Errorf("%s: meta source does not carry the capture provenance:\n%s", dir, got.Meta.Source)
		}
	}
}

// The default session's exit code follows the `save` precedent: a failing
// self-check is exactly when a recording is worth keeping, so the fixtures
// are written and the exit code is 1.
func TestFixturesRecordKeepsRecordingWhenSelfChecksFail(t *testing.T) {
	t.Chdir(t.TempDir())
	d := nut87Faulty(t, "/dev/hidraw3",
		fault{cmd: "GET_LED_EFFECT", res: 0, off: 22, data: []byte{0x12, 0x34}})
	replayReadPath(d, t)
	enum := &hidfake.Enumerator{Devices: []*hidfake.Device{d}}

	code, out, errOut := run(t, enum, "fixtures", "record", "--out", "corpus")
	if code != 1 {
		t.Fatalf("exit = %d, want 1 (self-check failed)", code)
	}
	if !strings.Contains(errOut, "self-check failed") {
		t.Errorf("stderr missing the self-check failure:\n%s", errOut)
	}
	if !strings.Contains(out, "recorded 6 exchange(s)") {
		t.Errorf("the fixtures must still be recorded:\n%s", out)
	}
	for _, dir := range []string{"get_key", "get_led_effect"} {
		recordedFixture(t, dir, "nut87")
	}
}

// Wrapping any command records its session — including the write path (the
// set_* exchanges `load` puts on the wire). A command seen twice in one
// session gets numbered cases in stream order.
func TestFixturesRecordWrapsACommandAndRecordsTheWritePath(t *testing.T) {
	t.Chdir(t.TempDir())
	d := newNut87Fake(t, "/dev/hidraw3")
	replayReadPath(d, t) // save
	replayReadPath(d, t) // load's probe + pre-write read pass
	replaySets(d, t)     // the batched writes
	replayReadPath(d, t) // load's read-back
	enum := &hidfake.Enumerator{Devices: []*hidfake.Device{d}}

	if code, _, errOut := run(t, enum, "save", "state.json"); code != 0 {
		t.Fatalf("save exit = %d, want 0 (stderr: %s)", code, errOut)
	}
	code, out, errOut := run(t, enum, "fixtures", "record", "--out", "corpus", "--case", "load",
		"load", "state.json", "--i-know-what-im-doing")
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr: %s)", code, errOut)
	}
	if !strings.Contains(out, "recorded 16 exchange(s)") {
		t.Errorf("output does not report the recorded exchanges:\n%s", out)
	}

	for _, dir := range []string{"set_key", "set_fn_key", "set_led_effect", "set_custom_led_data", "set_game_mode"} {
		got := recordedFixture(t, dir, "load")
		assertSameWire(t, got, loadFixture(t, dir, "nut87"))
		if got.Cmd != loadFixture(t, dir, "nut87").Cmd {
			t.Errorf("%s: meta cmd = %q", dir, got.Cmd)
		}
		if !strings.Contains(got.Meta.Source, "command: nutctl load state.json --i-know-what-im-doing") {
			t.Errorf("%s: meta source does not name the recorded session:\n%s", dir, got.Meta.Source)
		}
	}
	// The read-back pass reads GET_KEY a second time: numbered case.
	back := recordedFixture(t, "get_key", "load-2")
	assertSameWire(t, back, loadFixture(t, "get_key", "nut87"))
	// The probe reads GET_DEVICE_INFO exactly once.
	recordedFixture(t, "get_device_info", "load")
	if _, err := os.Stat(filepath.Join("corpus", "get_device_info", "load-2.meta.json")); err == nil {
		t.Error("GET_DEVICE_INFO was recorded twice; the probe is one exchange")
	}
}

// The round trip the ticket asks for: fixtures recorded from a live session
// drive the fake Device again and reproduce the same behavior — byte-for-byte
// the same State File and CLI output as the committed corpus does.
func TestRecordedFixturesDriveTheFakeDeviceRoundTrip(t *testing.T) {
	t.Chdir(t.TempDir())
	live := newNut87Fake(t, "/dev/hidraw10")
	replayReadPath(live, t)
	if code, _, errOut := run(t, &hidfake.Enumerator{Devices: []*hidfake.Device{live}},
		"fixtures", "record", "--out", "corpus"); code != 0 {
		t.Fatalf("record exit = %d, want 0 (stderr: %s)", code, errOut)
	}

	readOrder := []string{
		"get_device_info", "get_key", "get_fn_key",
		"get_led_effect", "get_custom_led_data", "get_game_mode",
	}
	dirs := []string{"./corpus", fixturesDir}
	files := make([][]byte, 2)
	outs := make([]string, 2)
	for i, dir := range dirs {
		d := newNut87Fake(t, "/dev/hidraw3")
		for _, name := range readOrder {
			x, err := fixture.Load(filepath.Join(dir, name), "nut87")
			if err != nil {
				t.Fatalf("load %s/%s: %v", dir, name, err)
			}
			d.Replay(x) // recorded or committed — the fake answers either way
		}
		for _, name := range readOrder {
			x, err := fixture.Load(filepath.Join(dir, name), "nut87")
			if err != nil {
				t.Fatalf("load %s/%s: %v", dir, name, err)
			}
			d.Replay(x) // a second pass for `save`
		}
		enum := &hidfake.Enumerator{Devices: []*hidfake.Device{d}}
		code, out, errOut := run(t, enum, "get", "settings", "--json")
		if code != 0 {
			t.Fatalf("%s: get settings exit = %d (stderr: %s)", dir, code, errOut)
		}
		outs[i] = out
		if code, _, errOut := run(t, enum, "save", "state.json"); code != 0 {
			t.Fatalf("%s: save exit = %d (stderr: %s)", dir, code, errOut)
		}
		raw, err := os.ReadFile("state.json")
		if err != nil {
			t.Fatal(err)
		}
		files[i] = raw
	}
	if outs[0] != outs[1] {
		t.Errorf("recorded fixtures drive `get settings --json` differently than the committed ones:\ngot:\n%s\nwant:\n%s", outs[0], outs[1])
	}
	if !bytes.Equal(files[0], files[1]) {
		t.Errorf("recorded fixtures produce a different State File than the committed ones:\n%s\n%s", files[0], files[1])
	}
}

// The recorder captures whatever the session does — including the recovery
// commands (ticket 08's follow-up: `fixtures record` records reset exchanges
// when a hardware reset run happens). SET_FACTORY_RESET is one
// fire-and-forget report: a fixture with request reports and no responses.
func TestFixturesRecordRecordsResetExchanges(t *testing.T) {
	t.Chdir(t.TempDir())
	d := newNut87Fake(t, "/dev/hidraw3")
	resetSession(d, t, 1)
	enum := &hidfake.Enumerator{Devices: []*hidfake.Device{d}}

	code, out, errOut := runStdin(t, enum, "reset keys\nn\n",
		"fixtures", "record", "--out", "corpus", "reset", "--keys")
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr: %s)", code, errOut)
	}
	got := recordedFixture(t, "set_factory_reset", "nut87")
	if got.Cmd != "SET_FACTORY_RESET" {
		t.Errorf("meta cmd = %q, want SET_FACTORY_RESET", got.Cmd)
	}
	if len(got.Requests) != 1 || len(got.Responses) != 0 {
		t.Fatalf("reset fixture = %d request reports, %d responses; want 1 and 0",
			len(got.Requests), len(got.Responses))
	}
	if !bytes.Equal(got.Requests[0], resetFrame(1)) {
		t.Errorf("recorded reset report:\n got % X\nwant % X", got.Requests[0], resetFrame(1))
	}
	if !strings.Contains(out, "set_factory_reset/nut87") {
		t.Errorf("output does not name the recorded exchange:\n%s", out)
	}
}

// Bad recorder input is a usage error (exit 2) that touches nothing.
func TestFixturesRecordUsageErrors(t *testing.T) {
	for _, args := range [][]string{
		{"fixtures"},
		{"fixtures", "import-pcap"}, // no capture file (import-pcap is its own subcommand)
		{"fixtures", "record", "--device", "/dev/hidraw3", "get", "keymap"},
	} {
		d := newNut87Fake(t, "/dev/hidraw3")
		enum := &hidfake.Enumerator{Devices: []*hidfake.Device{d}}
		code, _, errOut := run(t, enum, args...)
		if code != 2 {
			t.Errorf("nutctl %s: exit = %d, want 2 (usage error)", strings.Join(args, " "), code)
		}
		if !strings.Contains(errOut, "nutctl:") {
			t.Errorf("nutctl %s: stderr does not read like a usage error:\n%s", strings.Join(args, " "), errOut)
		}
		if d.Opened() {
			t.Errorf("nutctl %s: touched the Device", strings.Join(args, " "))
		}
	}
}
