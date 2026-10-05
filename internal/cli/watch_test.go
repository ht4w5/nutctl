package cli

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ht4w5/nutctl/internal/hid"
	"github.com/ht4w5/nutctl/internal/hidfake"
)

// --- `nutctl watch` ---
//
// Observability (ticket 08): stream the Device's notify traffic live so
// wireless quirks are debuggable (spec user story 30). watch never writes to
// the Device; these tests drive it through the Transport seam, injecting the
// unsolicited input reports the Device would push.

// syncBuffer is a test writer a watch goroutine and the assertions share.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (w *syncBuffer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.Write(p)
}

func (w *syncBuffer) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.String()
}

// watchRun runs `nutctl watch` in the background. It waits until waitFor's
// condition on the output holds (the test's synchronization with the
// stream), runs after (e.g. to unplug the Device), and returns the exit code
// and output once the command ends — by its own means or by cancellation.
func watchRun(t *testing.T, enum hid.Enumerator, waitFor func(out string) bool, after func()) (int, string, string) {
	t.Helper()
	out, errOut := &syncBuffer{}, &syncBuffer{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan int, 1)
	go func() {
		done <- Run([]string{"watch"}, Deps{
			Devices: enum,
			Stdout:  out,
			Stderr:  errOut,
			Context: ctx,
		})
	}()
	deadline := time.Now().Add(5 * time.Second)
	for !waitFor(out.String()) {
		if time.Now().After(deadline) {
			cancel()
			<-done
			t.Fatalf("watch output never reached the expected state:\n%s", out.String())
		}
		time.Sleep(time.Millisecond)
	}
	if after == nil {
		// The test ends the watch through its context (Ctrl-C).
		cancel()
		return <-done, out.String(), errOut.String()
	}
	// The test ends the watch itself (e.g. unplug): the command must notice
	// and exit on its own — the deadline only stops a hung test.
	after()
	select {
	case code := <-done:
		return code, out.String(), errOut.String()
	case <-time.After(5 * time.Second):
		cancel()
		<-done
		t.Fatalf("watch did not end on its own:\n%s\n%s", out.String(), errOut.String())
		return -1, "", ""
	}
}

// A notify frame as the Device pushes it (docs/protocol.md §3): response
// magic, notify command, subtype.
func notifyFrame(cmd, typ byte) []byte {
	return []byte{0x55, cmd, typ, 0, 0, 0, 0, 0}
}

func containsAll(s string, wants ...string) bool {
	for _, w := range wants {
		if !strings.Contains(s, w) {
			return false
		}
	}
	return true
}

// `nutctl watch` streams the device notify commands live (spec user story
// 30): each report is printed decoded and whole, notify traffic named from
// the bundle's listeners and anything else kept visible as raw bytes.
func TestWatchStreamsDeviceNotifyTraffic(t *testing.T) {
	d := newNut87Fake(t, "/dev/hidraw3")
	d.Replay(loadFixture(t, "get_device_info", "nut87")) // watch only probes
	disconnect := notifyFrame(252, 4)
	reset := notifyFrame(252, 5)
	sleep := notifyFrame(252, 6)
	wake := []byte{0xA6, 0xFF, 0x01}
	state := []byte{0x55, 250, 6, 1, 0, 0, 0, 0, 0xFF}
	garbage := []byte{0x01, 0x02, 0x03}
	d.Inject(disconnect, reset, sleep, wake, state, garbage)
	enum := &hidfake.Enumerator{Devices: []*hidfake.Device{d}}

	wants := []string{
		"watching NUT87 at /dev/hidraw3 (firmware 1.20)",
		"2.4G disconnect", "55 fc 04",
		"device reset", "55 fc 05",
		"2.4G sleep", "55 fc 06",
		"2.4G wake", "a6 ff 01",
		"device notify (type 6)", "55 fa 06",
		"input report", "01 02 03",
	}
	code, out, errOut := watchRun(t, enum,
		func(out string) bool { return containsAll(out, wants...) }, nil)
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr: %s)", code, errOut)
	}
	if !containsAll(out, wants...) {
		t.Errorf("stdout missing decoded notify lines:\n%s", out)
	}
	if sent := d.Sent(); len(sent) != 1 {
		t.Errorf("watch sent %d reports, want 1 (the probe only) — watch never writes", len(sent))
	}
}

// The stream ends when the Device disappears: a watch cut short by the very
// quirk it was chasing says so and exits nonzero.
func TestWatchStopsWhenTheDeviceDisappears(t *testing.T) {
	d := newNut87Fake(t, "/dev/hidraw3")
	d.Replay(loadFixture(t, "get_device_info", "nut87"))
	enum := &hidfake.Enumerator{Devices: []*hidfake.Device{d}}

	code, out, errOut := watchRun(t, enum,
		func(out string) bool { return strings.Contains(out, "watching NUT87") },
		func() { d.Close() }) // unplug: the input stream closes under watch
	if code != 1 {
		t.Fatalf("exit = %d, want 1 (the stream ended with the Device, stderr: %s)", code, errOut)
	}
	if !strings.Contains(errOut, "disconnected") {
		t.Errorf("stderr missing the disconnect line:\n%s", errOut)
	}
	if !strings.Contains(out, "watching NUT87") {
		t.Errorf("stdout missing the watch header:\n%s", out)
	}
}

// Bad input is a usage error (exit 2) that opens nothing.
func TestWatchUsageErrors(t *testing.T) {
	for _, args := range [][]string{
		{"watch", "extra"},
		{"watch", "--json"},
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
			t.Errorf("nutctl %s: opened the Device", strings.Join(args, " "))
		}
	}
}
