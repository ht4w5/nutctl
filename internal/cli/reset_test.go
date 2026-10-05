package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/ht4w5/nutctl/internal/fixture"
	"github.com/ht4w5/nutctl/internal/hidfake"
)

// --- `nutctl reset` ---
//
// Factory reset is the recovery action (ticket 08): `nutctl reset
// --keys|--lighting|--macros|--all` maps onto the firmware's reset scopes and
// is CLI-only behind a typed confirmation (spec user story 27). These tests
// are external behavior through the Transport seam: exit codes, stdout and
// stderr, and the byte-exact SET_FACTORY_RESET report the Device receives.

// resetFrame is the byte-exact SET_FACTORY_RESET report for one scope (the
// bundle's xn encoder, docs/protocol.md §3): `AA 0F <scope> …` on 64-byte
// reports.
func resetFrame(scopeWire byte) []byte {
	f := make([]byte, 64)
	f[0] = 0xAA
	f[1] = 0x0F
	f[2] = scopeWire
	return f
}

// resetFrames lists every factory reset report the fake received.
func resetFrames(d *hidfake.Device) [][]byte {
	var out [][]byte
	for _, r := range d.Sent() {
		if len(r) >= 2 && r[0] == 0xAA && r[1] == 0x0F {
			out = append(out, r)
		}
	}
	return out
}

// resetSession scripts one `reset` invocation: probe + pre-reset read pass,
// the factory reset report, and the read-back pass.
func resetSession(d *hidfake.Device, t *testing.T, scopeWire byte) {
	t.Helper()
	replayReadPath(d, t)
	d.Script(resetFrame(scopeWire))
	replayReadPath(d, t)
}

// patchedReadPath is the read-path fixture exchanges with bytes patched into
// one response report — the way a test makes the read-back pass differ from
// the pre-reset state.
func patchedReadPath(t *testing.T, f fault) []fixture.Exchange {
	t.Helper()
	xs := readPathFixtures(t)
	for i := range xs {
		if xs[i].Cmd != f.cmd {
			continue
		}
		report := bytes.Clone(xs[i].Responses[f.res])
		copy(report[f.off:], f.data)
		xs[i].Responses[f.res] = report
	}
	return xs
}

func replayBlocks(d *hidfake.Device, xs []fixture.Exchange) {
	for _, x := range xs {
		d.Replay(x)
	}
}

// Exactly one scope flag selects what is destroyed; zero, two or a
// positional argument is a usage error (exit 2) that touches nothing.
func TestResetNeedsExactlyOneScope(t *testing.T) {
	for _, args := range [][]string{
		{"reset"},
		{"reset", "--keys", "--all"},
		{"reset", "--lighting", "--macros"},
		{"reset", "keys"},
		{"reset", "--keys", "extra"},
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
		if frames := resetFrames(d); len(frames) != 0 || d.Opened() {
			t.Errorf("nutctl %s: touched the Device (%d reset reports, opened=%v)",
				strings.Join(args, " "), len(frames), d.Opened())
		}
	}
}

// The four CLI scopes map onto the firmware's reset sub-commands
// (docs/protocol.md §3: KEY_RESET 1, LIGHTING_RESET 2, MACRO_RESET 4,
// RESET_ALL 255) — byte-exact on the wire.
func TestResetMapsScopesOntoFirmwareScopes(t *testing.T) {
	for _, tc := range []struct {
		flag      string
		word      string
		scopeWire byte
		firmware  string
	}{
		{"--keys", "keys", 1, "KEY_RESET"},
		{"--lighting", "lighting", 2, "LIGHTING_RESET"},
		{"--macros", "macros", 4, "MACRO_RESET"},
		{"--all", "all", 255, "RESET_ALL"},
	} {
		t.Run(tc.flag, func(t *testing.T) {
			t.Chdir(t.TempDir())
			d := newNut87Fake(t, "/dev/hidraw3")
			resetSession(d, t, tc.scopeWire)
			enum := &hidfake.Enumerator{Devices: []*hidfake.Device{d}}

			code, out, errOut := runStdin(t, enum, "reset "+tc.word+"\nn\n", "reset", tc.flag)
			if code != 0 {
				t.Fatalf("exit = %d, want 0 (stderr: %s)", code, errOut)
			}
			frames := resetFrames(d)
			if len(frames) != 1 {
				t.Fatalf("factory reset reports sent = %d, want exactly 1", len(frames))
			}
			if !bytes.Equal(frames[0], resetFrame(tc.scopeWire)) {
				t.Errorf("reset report:\n got  %X\n want %X", frames[0], resetFrame(tc.scopeWire))
			}
			if !strings.Contains(out, tc.firmware) {
				t.Errorf("stdout missing the firmware scope name %q:\n%s", tc.firmware, out)
			}
		})
	}
}

// Reset is refused on a bootloader/firmware-recovery Device (ticket 08,
// spec user story 28) — before any prompt, before any report is sent.
func TestResetRefusesBootloaderDevice(t *testing.T) {
	boot := nut87Faulty(t, "/dev/hidraw3",
		fault{cmd: "GET_DEVICE_INFO", res: 0, off: 40, data: []byte{1}})
	enum := &hidfake.Enumerator{Devices: []*hidfake.Device{boot}}

	code, _, errOut := run(t, enum, "reset", "--all", "--i-know-what-im-doing")
	if code != 1 {
		t.Fatalf("exit = %d, want 1 (refused)", code)
	}
	for _, want := range []string{"refusing to write", "bootloader"} {
		if !strings.Contains(errOut, want) {
			t.Errorf("stderr missing %q:\n%s", want, errOut)
		}
	}
	if frames := resetFrames(boot); len(frames) != 0 {
		t.Errorf("factory reset reports sent = %d, want none — a bootloader Device is never reset", len(frames))
	}
}

// postResetPathFixtures are the read-path exchanges of a Device minutes
// after `nutctl reset` (recorded from real hardware, 2026-10-06 — the
// factory-reset state whose block tails read 0x00 0x00, docs/protocol.md
// §6.8).
func postResetPathFixtures(t *testing.T) []fixture.Exchange {
	t.Helper()
	return []fixture.Exchange{
		loadFixture(t, "get_device_info", "nut87_post_reset"),
		loadFixture(t, "get_key", "nut87_post_reset"),
		loadFixture(t, "get_fn_key", "nut87_post_reset"),
		loadFixture(t, "get_led_effect", "nut87_post_reset"),
		loadFixture(t, "get_custom_led_data", "nut87_post_reset"),
		loadFixture(t, "get_game_mode", "nut87_post_reset"),
	}
}

// A freshly factory-reset Device is a HEALTHY Device (the reset aftermath,
// ticket 12): the reset clears the block-tail bytes to 0x00 0x00 —
// legitimate state, recorded from real hardware — and the self-checks must
// accept it. Rejecting it made get/save/load refuse the very Device the
// reset was meant to recover.
func TestReadsWorkOnAFreshlyResetDevice(t *testing.T) {
	d := newNut87Fake(t, "/dev/hidraw3")
	for _, x := range postResetPathFixtures(t) {
		d.Replay(x)
	}
	enum := &hidfake.Enumerator{Devices: []*hidfake.Device{d}}

	code, out, errOut := run(t, enum, "get", "settings")
	if code != 0 {
		t.Fatalf("exit = %d, want 0 — a freshly reset Device must be readable (stderr: %s)", code, errOut)
	}
	if strings.Contains(errOut, "self-check failed") {
		t.Errorf("self-checks rejected the factory-reset state:\n%s", errOut)
	}
	if !strings.Contains(out, "Report rate") {
		t.Errorf("stdout missing the requested view:\n%s", out)
	}
}

// The typed confirmation names the scope being destroyed (spec user story
// 27): the prompt says what dies and demands the scope word back; anything
// else — including a bare Enter — refuses without sending anything.
func TestResetTypedConfirmationNamesTheScope(t *testing.T) {
	t.Chdir(t.TempDir())
	d := newNut87Fake(t, "/dev/hidraw3")
	resetSession(d, t, 1)
	enum := &hidfake.Enumerator{Devices: []*hidfake.Device{d}}

	code, out, errOut := runStdin(t, enum, "reset keys\nn\n", "reset", "--keys")
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr: %s)", code, errOut)
	}
	for _, want := range []string{`type "reset keys" to confirm`, "key bindings"} {
		if !strings.Contains(errOut, want) {
			t.Errorf("stderr missing the typed confirmation %q:\n%s", want, errOut)
		}
	}
	if !strings.Contains(out, "factory reset sent") {
		t.Errorf("stdout missing the success line:\n%s", out)
	}
	if frames := resetFrames(d); len(frames) != 1 {
		t.Errorf("factory reset reports sent = %d, want exactly 1", len(frames))
	}
}

func TestResetTypedConfirmationMismatchRefuses(t *testing.T) {
	for _, stdin := range []string{"keys\n", "reset KEY\n", "reset keys all\n", "\n", ""} {
		t.Chdir(t.TempDir())
		d := newNut87Fake(t, "/dev/hidraw3")
		replayReadPath(d, t) // probe + pre-reset read pass may run; nothing else may
		enum := &hidfake.Enumerator{Devices: []*hidfake.Device{d}}

		code, out, errOut := runStdin(t, enum, stdin, "reset", "--keys")
		if code != 1 {
			t.Errorf("stdin %q: exit = %d, want 1 (refused)", stdin, code)
		}
		if !strings.Contains(errOut, "nothing was reset") {
			t.Errorf("stdin %q: stderr missing the refusal:\n%s", stdin, errOut)
		}
		if frames := resetFrames(d); len(frames) != 0 {
			t.Errorf("stdin %q: factory reset reports sent = %d, want none", stdin, len(frames))
		}
		if out != "" {
			t.Errorf("stdin %q: stdout = %q, want empty", stdin, out)
		}
	}
}

// With no input at all the confirmation cannot be asked: the write stays
// refused and nothing is sent. The phrase can be piped on stdin, but its
// absence is never consent.
func TestResetWithoutInputRefuses(t *testing.T) {
	d := newNut87Fake(t, "/dev/hidraw3")
	replayReadPath(d, t)
	enum := &hidfake.Enumerator{Devices: []*hidfake.Device{d}}

	code, _, errOut := runStdin(t, enum, "", "reset", "--all")
	if code != 1 {
		t.Fatalf("exit = %d, want 1 (refused)", code)
	}
	for _, want := range []string{"nothing was reset", "stdin"} {
		if !strings.Contains(errOut, want) {
			t.Errorf("stderr missing %q:\n%s", want, errOut)
		}
	}
	if frames := resetFrames(d); len(frames) != 0 {
		t.Errorf("factory reset reports sent = %d, want none", len(frames))
	}
}

// The typed confirmation is the one gate with no flag around it (spec user
// story 27): --i-know-what-im-doing skips the golden read, never the phrase
// — scripts pipe it on stdin.
func TestResetTypedConfirmationHasNoSkipFlag(t *testing.T) {
	for _, stdin := range []string{"", "keys\n", "reset KEY\n"} {
		d := newNut87Fake(t, "/dev/hidraw3")
		replayReadPath(d, t)
		enum := &hidfake.Enumerator{Devices: []*hidfake.Device{d}}

		code, _, errOut := runStdin(t, enum, stdin, "reset", "--all", "--i-know-what-im-doing")
		if code != 1 {
			t.Errorf("stdin %q: exit = %d, want 1 (refused)", stdin, code)
		}
		if !strings.Contains(errOut, "nothing was reset") {
			t.Errorf("stdin %q: stderr missing the refusal:\n%s", stdin, errOut)
		}
		if frames := resetFrames(d); len(frames) != 0 {
			t.Errorf("stdin %q: factory reset reports sent = %d, want none", stdin, len(frames))
		}
	}
}

// The noisy skip flag jumps the golden-read prompt for scripts — loudly, and
// naming what it skipped (ADR-0003's escape hatch) — while the typed
// confirmation still asks.
func TestResetSkipFlagSkipsOnlyTheGoldenRead(t *testing.T) {
	t.Chdir(t.TempDir())
	d := newNut87Fake(t, "/dev/hidraw3")
	resetSession(d, t, 1)
	enum := &hidfake.Enumerator{Devices: []*hidfake.Device{d}}

	code, _, errOut := runStdin(t, enum, "reset keys\n", "reset", "--keys", "--i-know-what-im-doing")
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr: %s)", code, errOut)
	}
	if strings.Contains(errOut, "? [Y/n]") {
		t.Errorf("the skip flag must skip the golden-read prompt:\n%s", errOut)
	}
	if !strings.Contains(errOut, "to confirm") {
		t.Errorf("the typed confirmation must still ask:\n%s", errOut)
	}
	for _, want := range []string{"--i-know-what-im-doing", "golden read"} {
		if !strings.Contains(errOut, want) {
			t.Errorf("the skip flag must be noisy about it, stderr missing %q:\n%s", want, errOut)
		}
	}
	if files := goldenFiles(t); len(files) != 0 {
		t.Errorf("golden reads written = %v, want none", files)
	}
}

// The write gate's golden read (ADR-0003) is offered before the destruction:
// the state that is about to die can be saved first — offered, never forced
// (ADR-0005).
func TestResetGoldenReadOfferSavesBeforeDestruction(t *testing.T) {
	t.Chdir(t.TempDir())
	d := newNut87Fake(t, "/dev/hidraw3")
	resetSession(d, t, 1)
	enum := &hidfake.Enumerator{Devices: []*hidfake.Device{d}}

	code, _, errOut := runStdin(t, enum, "reset keys\ny\n", "reset", "--keys")
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr: %s)", code, errOut)
	}
	if !strings.Contains(errOut, "save current state to ./golden-") {
		t.Errorf("stderr missing the golden-read offer:\n%s", errOut)
	}
	files := goldenFiles(t)
	if len(files) != 1 {
		t.Fatalf("golden reads written = %v, want exactly one", files)
	}
	state := stateFileEnvelope(t, files[0]) // the golden read is a State File — one code path
	if len(state["base"].([]any)) != 128 {
		t.Error("golden read does not carry the full state")
	}
	if frames := resetFrames(d); len(frames) != 1 {
		t.Errorf("factory reset reports sent = %d, want exactly 1", len(frames))
	}
}

func TestResetGoldenReadDeclinedWritesNothingToDisk(t *testing.T) {
	t.Chdir(t.TempDir())
	d := newNut87Fake(t, "/dev/hidraw3")
	resetSession(d, t, 1)
	enum := &hidfake.Enumerator{Devices: []*hidfake.Device{d}}

	code, _, errOut := runStdin(t, enum, "reset keys\nn\n", "reset", "--keys")
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr: %s)", code, errOut)
	}
	if files := goldenFiles(t); len(files) != 0 {
		t.Errorf("golden reads written = %v, want none — a declined offer writes nothing", files)
	}
	if !strings.Contains(errOut, "skipped the golden read") {
		t.Errorf("stderr missing the dismissal:\n%s", errOut)
	}
}

// Reset is the recovery action: a Device whose configuration is garbage —
// the state that fails the self-checks — must still be resettable
// (ticket 08: "factory reset when the configuration is garbage").
func TestResetProceedsWhenSelfChecksFail(t *testing.T) {
	t.Chdir(t.TempDir())
	// Break the keymap block-tail marker: a misaligned/corrupt read fails
	// self-check 2 loudly — and must not block the recovery.
	d := nut87Faulty(t, "/dev/hidraw3",
		fault{cmd: "GET_KEY", res: 9, off: 8 + 6, data: []byte{0x00}})
	resetSession(d, t, 1)
	enum := &hidfake.Enumerator{Devices: []*hidfake.Device{d}}

	code, out, errOut := runStdin(t, enum, "reset keys\nn\n", "reset", "--keys")
	if code != 0 {
		t.Fatalf("exit = %d, want 0 — garbage state must not block the recovery (stderr: %s)", code, errOut)
	}
	if !strings.Contains(out, "factory reset sent") {
		t.Errorf("stdout missing the success line:\n%s", out)
	}
	if frames := resetFrames(d); len(frames) != 1 {
		t.Errorf("factory reset reports sent = %d, want exactly 1", len(frames))
	}
}

// The read-back proves what the reset changed: the blocks the scope covers
// are compared against the pre-reset state and counted — never claimed from
// the write itself.
func TestResetReadBackSummarizesWhatChanged(t *testing.T) {
	t.Chdir(t.TempDir())
	d := newNut87Fake(t, "/dev/hidraw3")
	replayReadPath(d, t) // probe + pre-reset pass
	d.Script(resetFrame(1))
	// The read-back pass reports slot 0 as DEFAULT where the pre-reset state
	// bound it to a key: exactly one Base Layer Key Slot changed.
	replayBlocks(d, patchedReadPath(t, fault{cmd: "GET_KEY", res: 0, off: 8, data: []byte{0x00}}))
	enum := &hidfake.Enumerator{Devices: []*hidfake.Device{d}}

	code, out, errOut := runStdin(t, enum, "reset keys\nn\n", "reset", "--keys")
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr: %s)", code, errOut)
	}
	for _, want := range []string{
		"read-back", "base: 1 of 128 Key Slots changed", "fn: unchanged",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "per-key") || strings.Contains(out, "lighting") {
		t.Errorf("the keys scope reports only the blocks it covers:\n%s", out)
	}
}

// A scope that the v0 read surface cannot see says so instead of pretending
// (Macros are deferred to v0.1).
func TestResetMacrosScopeSaysThereIsNoReadBack(t *testing.T) {
	t.Chdir(t.TempDir())
	d := newNut87Fake(t, "/dev/hidraw3")
	resetSession(d, t, 4)
	enum := &hidfake.Enumerator{Devices: []*hidfake.Device{d}}

	code, out, errOut := runStdin(t, enum, "reset macros\nn\n", "reset", "--macros")
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr: %s)", code, errOut)
	}
	if !strings.Contains(out, "macros: not readable") {
		t.Errorf("stdout missing the honest read-back note:\n%s", out)
	}
}

// When the Device will not answer the read-back, the reset that WAS sent
// still stands: a loud warning, exit 0. Recovery must not fail because its
// proof is unavailable.
func TestResetReadBackUnavailableStillSucceeds(t *testing.T) {
	t.Chdir(t.TempDir())
	d := newNut87Fake(t, "/dev/hidraw3")
	replayReadPath(d, t) // probe + pre-reset pass only: the read-back has nothing scripted
	d.Script(resetFrame(1))
	enum := &hidfake.Enumerator{Devices: []*hidfake.Device{d}}

	code, out, errOut := runStdin(t, enum, "reset keys\nn\n", "reset", "--keys")
	if code != 0 {
		t.Fatalf("exit = %d, want 0 — the reset was sent (stderr: %s)", code, errOut)
	}
	if !strings.Contains(errOut, "read-back unavailable") {
		t.Errorf("stderr missing the read-back warning:\n%s", errOut)
	}
	if !strings.Contains(out, "factory reset sent") {
		t.Errorf("stdout missing the success line:\n%s", out)
	}
	if frames := resetFrames(d); len(frames) != 1 {
		t.Errorf("factory reset reports sent = %d, want exactly 1", len(frames))
	}
}
