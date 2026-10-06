// Package hidfake provides a scripted fake Device implementing hid.Transport
// and hid.Enumerator. It replays recorded exchanges (internal/fixture) and
// injects faults (dropped responses, garbage headers) so everything above the
// Transport seam runs without hardware.
package hidfake

import (
	"bytes"
	"errors"
	"fmt"
	"sync"

	"github.com/ht4w5/nutctl/internal/fixture"
	"github.com/ht4w5/nutctl/internal/hid"
)

// step is one scripted exchange: a request report and how the fake answers it.
type step struct {
	req  []byte // expected request report
	res  [][]byte
	junk []byte // if set, respond with this garbage instead
	drop bool   // if true, swallow the request (dropped response)
}

// Device is a scripted fake HID device.
type Device struct {
	Info hid.Info

	// OpenErr, if set, makes the enumerator's Open fail with it.
	OpenErr error
	// SendErr, if set, makes SendReport fail with it.
	SendErr error

	mu      sync.Mutex
	script  []step
	sent    [][]byte
	reports chan []byte
	closed  bool
	opened  bool
}

// New returns a fake Device with the given identity. Report length defaults to
// 32 when Info.ReportLength is 0.
func New(info hid.Info) *Device {
	if info.ReportLength == 0 {
		info.ReportLength = 32
	}
	return &Device{Info: info, reports: make(chan []byte, 256)}
}

// Script queues one exchange: expect req and answer with res reports.
func (d *Device) Script(req []byte, res ...[]byte) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.script = append(d.script, step{req: req, res: res})
}

// ScriptDrop queues a dropped-response fault: accept req, never answer.
func (d *Device) ScriptDrop(req []byte) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.script = append(d.script, step{req: req, drop: true})
}

// ScriptGarbage queues a garbage-header fault: answer req with junk.
func (d *Device) ScriptGarbage(req, junk []byte) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.script = append(d.script, step{req: req, junk: junk})
}

// Replay scripts a recorded exchange: request report i is answered with
// response report i. An exchange without requests (unsolicited input —
// device notify traffic) is injected as the Device pushed it.
func (d *Device) Replay(x fixture.Exchange) {
	if len(x.Requests) == 0 {
		d.Inject(x.Responses...)
		return
	}
	for i, req := range x.Requests {
		if i < len(x.Responses) {
			d.Script(req, x.Responses[i])
		} else {
			d.Script(req)
		}
	}
}

// Sent reports every request report the fake received, in order.
func (d *Device) Sent() [][]byte {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([][]byte, len(d.sent))
	copy(out, d.sent)
	return out
}

// Opened reports whether the enumerator opened this device.
func (d *Device) Opened() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.opened
}

// ReportLength implements hid.Transport.
func (d *Device) ReportLength() int { return d.Info.ReportLength }

// SendReport implements hid.Transport. The response (if any) is delivered on
// Reports().
func (d *Device) SendReport(_ uint8, report []byte) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return errors.New("hidfake: device closed")
	}
	if d.SendErr != nil {
		return d.SendErr
	}
	d.sent = append(d.sent, bytes.Clone(report))

	var s step
	found := false
	for i := range d.script {
		if d.script[i].req == nil || bytes.Equal(d.script[i].req, report) {
			s = d.script[i]
			d.script = append(d.script[:i], d.script[i+1:]...)
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("hidfake: unexpected request report %X (not in script)", report)
	}
	if s.drop {
		return nil
	}
	if s.junk != nil {
		d.reports <- bytes.Clone(s.junk)
		return nil
	}
	for _, res := range s.res {
		d.reports <- bytes.Clone(res)
	}
	return nil
}

// Reports implements hid.Transport.
func (d *Device) Reports() <-chan []byte { return d.reports }

// Inject delivers unsolicited input reports (device notify traffic) as if
// the Device had pushed them — the seam's lever for `nutctl watch` and the
// notification stream. Reports injected before a session starts sit in the
// buffer until it reads them; injecting into a closed Device is a no-op.
func (d *Device) Inject(reports ...[]byte) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return
	}
	for _, r := range reports {
		d.reports <- bytes.Clone(r)
	}
}

// Close implements hid.Transport.
func (d *Device) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.closed {
		d.closed = true
		close(d.reports)
	}
	return nil
}

// Enumerator is a fake hid.Enumerator over scripted devices.
type Enumerator struct {
	Devices      []*Device
	EnumerateErr error
}

// Enumerate implements hid.Enumerator.
func (e *Enumerator) Enumerate() ([]hid.Info, error) {
	if e.EnumerateErr != nil {
		return nil, e.EnumerateErr
	}
	infos := make([]hid.Info, 0, len(e.Devices))
	for _, d := range e.Devices {
		infos = append(infos, d.Info)
	}
	return infos, nil
}

// Open implements hid.Enumerator. Each Open is a fresh session (a new
// Reports channel, the device no longer closed) — a test script can drive
// several CLI invocations against one fake, exactly as each real invocation
// opens the hidraw node anew. Scripted exchanges carry over between
// sessions; Sent() keeps accumulating.
func (e *Enumerator) Open(info hid.Info) (hid.Transport, error) {
	for _, d := range e.Devices {
		if d.Info.Path == info.Path {
			d.mu.Lock()
			defer d.mu.Unlock()
			d.opened = true
			if d.OpenErr != nil {
				return nil, d.OpenErr
			}
			if d.closed {
				d.closed = false
				d.reports = make(chan []byte, 256)
			}
			return d, nil
		}
	}
	return nil, fmt.Errorf("hidfake: no device at %s: %w", info.Path, hid.ErrNotFound)
}
