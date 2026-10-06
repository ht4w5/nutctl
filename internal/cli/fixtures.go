package cli

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ht4w5/nutctl/internal/device"
	"github.com/ht4w5/nutctl/internal/fixture"
	"github.com/ht4w5/nutctl/internal/hid"
	"github.com/ht4w5/nutctl/internal/protocol"
)

// --- `nutctl fixtures record` ---
//
// Corpus building is probing (spec user story 34, docs/capture.md Method A):
// any live session against real hardware turns into committed fixtures that
// keep the offline tests honest. The recorder is a passive observer on the
// Transport seam — it never touches the wire itself; whatever the recorded
// session does (its own write gate included), the recorder stores request and
// response pairs plus provenance metadata. Nothing on the input side is
// dropped: device notify traffic and unparseable input reports are recorded
// as observed. A REQUEST that does not parse is a bug in the sender, and
// fails the recording loudly instead (protocol.SplitTransfers).
//
// One exchange is one transfer on the wire (internal/fixture): fixtures land
// in <out>/<cmd>/<case>.{req,res}.hex + meta.json, one case per command per
// session; a command seen more than once gets <case>-2, <case>-3, …
// Re-recording a case replaces it (refreshing the corpus is the point).

// runFixtures dispatches the `fixtures` subcommands: `record` (live
// sessions) and `import-pcap` (kernel captures, docs/capture.md Method B).
func runFixtures(args []string, deps Deps) int {
	if len(args) == 0 {
		return usageError(deps, fmt.Errorf("fixtures needs a subcommand: record, import-pcap"))
	}
	switch args[0] {
	case "record":
		return runFixturesRecord(args[1:], deps)
	case "import-pcap":
		return runFixturesImportPcap(args[1:], deps)
	}
	return usageError(deps, fmt.Errorf("fixtures needs a subcommand: record, import-pcap"))
}

func runFixturesRecord(args []string, deps Deps) int {
	fs := flag.NewFlagSet("fixtures record", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	outDir := fs.String("out", "testdata/captures", "corpus directory to record into")
	caseName := fs.String("case", "", "fixture case name (default: the Model name in lower case)")
	selector := fs.String("device", "", "hidraw path of the Device to use (default session only)")
	if err := fs.Parse(args); err != nil {
		return usageError(deps, err)
	}
	inner := fs.Args()
	if len(inner) > 0 && *selector != "" {
		return usageError(deps, fmt.Errorf("--device selects the Device for the recorded default session; give --device to the recorded command instead"))
	}

	rec := newRecorder()
	recDeps := deps
	recDeps.Devices = recordEnumerator{inner: deps.Devices, rec: rec}

	session := "default read session (probe + one full checked read pass)"
	code := 0
	if len(inner) > 0 {
		session = "command: nutctl " + strings.Join(inner, " ")
		code = Run(inner, recDeps)
	} else {
		if _, err := readFullState(recDeps, *selector); err != nil {
			var checkErr *device.CheckError
			if errors.As(err, &checkErr) {
				// The `get`/`save` precedent: a failing self-check is exactly
				// when a recording is worth keeping. The fixtures are
				// recorded either way; the checks gate writes, not records.
				fmt.Fprintln(deps.Stderr, checkErr)
				code = 1
			} else {
				code = fail(deps, err)
			}
		}
	}

	written, err := rec.save(recordOpts{OutDir: *outDir, Case: *caseName, Session: session, Now: time.Now()}, deps.Stdout)
	if err != nil {
		return fail(deps, err)
	}
	if written == 0 {
		fmt.Fprintln(deps.Stdout, "recorded no exchanges (nothing on the wire)")
		return code
	}
	fmt.Fprintf(deps.Stdout, "recorded %d exchange(s) into %s\n", written, *outDir)
	return code
}

// recorder is the wire log of one recorded session: every report that crossed
// the Transport seam, in order (docs/capture.md Method A).
type recorder struct {
	mu   sync.Mutex
	log  []protocol.WireReport
	info hid.Info // the Device the session opened (metadata), zero if none
}

func newRecorder() *recorder { return &recorder{} }

// device notes which Device the session opened — the identity half of the
// fixture metadata.
func (r *recorder) device(info hid.Info) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.info = info
}

// record appends one report to the wire log.
func (r *recorder) record(sent bool, report []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.log = append(r.log, protocol.WireReport{Sent: sent, Report: bytes.Clone(report)})
}

// recordOpts is the recording's naming and provenance — what meta.json needs
// beyond the wire bytes themselves.
type recordOpts struct {
	OutDir  string    // corpus directory to record into
	Case    string    // fixture case name; "" = the Model name in lower case
	Session string    // what the recorded session did, for the meta source
	Now     time.Time // recording date, for the meta source
}

// save splits the wire log into per-transfer exchanges and writes each one as
// a fixture with its provenance metadata (firmware version, connection type,
// capture method). It returns the number of fixtures written.
func (r *recorder) save(opts recordOpts, out io.Writer) (int, error) {
	r.mu.Lock()
	log, info := r.log, r.info
	r.log = nil
	r.mu.Unlock()

	transfers, err := protocol.SplitTransfers(log)
	if err != nil {
		return 0, fmt.Errorf("record session: %w", err)
	}
	if len(transfers) == 0 {
		return 0, nil
	}
	model, err := device.Identify(device.IdentityFromUSB(info))
	if err != nil {
		// A fixture whose Device is unknown is not evidence. Refuse it
		// loudly instead of committing a meta.json that guesses.
		return 0, fmt.Errorf("recorded Device %q is not a known Model: %w", info.ProductName, err)
	}
	di, probed, err := capturedProbe(transfers)
	if err != nil {
		return 0, fmt.Errorf("recorded GET_DEVICE_INFO does not decode: %w", err)
	}
	firmware := ""
	if probed {
		firmware = di.Version
	}
	firmwareShown := firmwareLabel(firmware)
	reportLen := info.ReportLength
	if reportLen == 0 {
		reportLen = 32
	}
	caseName := opts.Case
	if caseName == "" {
		caseName = strings.ToLower(model.Name)
	}

	source := fmt.Sprintf(
		"recorded %s from the Device at %s (USB %04x:%04x, usage page %#x, firmware %s) via `nutctl fixtures record` over the hidraw transport — docs/capture.md Method A; %d-byte reports; session: %s",
		opts.Now.Format("2006-01-02"), info.Path, info.VendorID, info.ProductID, info.UsagePage,
		firmwareShown, reportLen, opts.Session)

	return writeFixtures(transfers, writeOpts{
		OutDir: opts.OutDir,
		Case:   caseName,
		Meta: fixture.Meta{
			Model:         model.Name,
			Connection:    string(model.Connection),
			Firmware:      firmware,
			CaptureMethod: "active-probing",
			Source:        source,
		},
	}, out)
}

// writeOpts is the naming and provenance of one set of fixtures — what
// meta.json holds beyond the wire bytes themselves (docs/capture.md).
type writeOpts struct {
	OutDir string       // corpus directory to write into
	Case   string       // fixture case name (the same name numbers repeats <case>-2, …)
	Meta   fixture.Meta // model, connection, firmware, capture method, source
}

// writeFixtures writes each exchange as one corpus fixture
// (internal/fixture), named by the wire (protocol.TransferName). A command
// seen more than once in one capture gets <case>-2, <case>-3, … in stream
// order. It returns the number of fixtures written.
func writeFixtures(transfers []protocol.WireTransfer, opts writeOpts, out io.Writer) (int, error) {
	seen := map[string]int{}
	for _, tr := range transfers {
		name := protocol.TransferName(tr)
		seen[name]++
		caseName := opts.Case
		if seen[name] > 1 {
			caseName = fmt.Sprintf("%s-%d", opts.Case, seen[name])
		}
		x := fixture.Exchange{
			Case:      caseName,
			Cmd:       name,
			Requests:  tr.Requests,
			Responses: tr.Responses,
			Meta:      opts.Meta,
		}
		if err := fixture.Save(filepath.Join(opts.OutDir, fixture.DirFor(name)), x); err != nil {
			return 0, err
		}
		fmt.Fprintf(out, "  %s/%s: %d request report(s), %d response report(s)\n",
			fixture.DirFor(name), caseName, len(tr.Requests), len(tr.Responses))
	}
	return len(transfers), nil
}

// firmwareLabel renders the firmware version for provenance text: "unknown"
// when a capture carries no GET_DEVICE_INFO to claim one from.
func firmwareLabel(version string) string {
	if version == "" {
		return "unknown"
	}
	return version
}

// capturedProbe is the GET_DEVICE_INFO exchange a capture carried — the
// firmware version and USB ids the metadata records. It reports ok=false
// when the capture never probed (nothing to claim) and an error when the
// probe exists but does not decode (captured bytes that cannot be evidence).
func capturedProbe(transfers []protocol.WireTransfer) (protocol.DeviceInfo, bool, error) {
	for _, tr := range transfers {
		if tr.Cmd != protocol.CmdGetDeviceInfo || len(tr.Responses) == 0 {
			continue
		}
		di, err := protocol.DecodeDeviceInfo(protocol.Reassemble(tr.Responses, protocol.DeviceInfoSize))
		if err != nil {
			return protocol.DeviceInfo{}, false, err
		}
		return di, true, nil
	}
	return protocol.DeviceInfo{}, false, nil
}

// recordEnumerator wraps the Transport seam so every report the recorded
// session sends or receives lands in the recorder's wire log. It is a
// decorator on hid.Enumerator: the session under it runs unchanged.
type recordEnumerator struct {
	inner hid.Enumerator
	rec   *recorder
}

func (e recordEnumerator) Enumerate() ([]hid.Info, error) { return e.inner.Enumerate() }

func (e recordEnumerator) Open(info hid.Info) (hid.Transport, error) {
	tr, err := e.inner.Open(info)
	if err != nil {
		return nil, err
	}
	e.rec.device(info)
	return newRecordTransport(tr, e.rec), nil
}

// recordTransport is the recording half of the decorator.
type recordTransport struct {
	inner hid.Transport
	rec   *recorder
	out   chan []byte
}

func newRecordTransport(inner hid.Transport, rec *recorder) *recordTransport {
	t := &recordTransport{inner: inner, rec: rec, out: make(chan []byte, notifyBufferSize)}
	go t.pump()
	return t
}

func (t *recordTransport) pump() {
	for raw := range t.inner.Reports() {
		t.rec.record(false, raw)
		t.out <- raw
	}
	close(t.out)
}

// notifyBufferSize bounds the recorded session's input queue; the reader is
// the protocol layer, which drains continuously.
const notifyBufferSize = 256

func (t *recordTransport) ReportLength() int { return t.inner.ReportLength() }

// SendReport records the report BEFORE handing it down: fast Devices answer
// from inside the send, and the wire log must read request-then-response.
func (t *recordTransport) SendReport(id uint8, report []byte) error {
	t.rec.record(true, report)
	return t.inner.SendReport(id, report)
}

func (t *recordTransport) Reports() <-chan []byte { return t.out }

func (t *recordTransport) Close() error { return t.inner.Close() }
