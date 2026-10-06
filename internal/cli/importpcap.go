package cli

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ht4w5/nutctl/internal/device"
	"github.com/ht4w5/nutctl/internal/fixture"
	"github.com/ht4w5/nutctl/internal/protocol"
	"github.com/ht4w5/nutctl/internal/usbmon"
)

// --- `nutctl fixtures import-pcap` (ticket 10, docs/capture.md Method B) ---
//
// Vendor-app traffic captured at the kernel level (usbmon while the official
// app runs in a Windows VM) becomes fixtures too, so our implementation can
// be cross-checked against the official one. The importer reads the capture's
// URBs, keeps the HID reports that carry the protocol (the same §2 framing
// `internal/protocol` speaks) and groups them into the same request/response
// exchanges `nutctl fixtures record` stores — one fixture per transfer, in
// the corpus format.
//
// Two rules keep the import honest (the ticket's):
//
//   - Imported fixtures validate against the protocol framing
//     (protocol.ValidateTransfer — the corpus test's rule too).
//   - Traffic that does not become a fixture is reported loudly instead of
//     silently dropped: unparseable reports on the Device's protocol streams
//     are a parse failure, i.e. bug-report material, and everything else
//     (other Devices, other endpoints, non-report URBs) is counted and
//     sampled where the operator can see it.

// runFixturesImportPcap imports one capture file.
func runFixturesImportPcap(args []string, deps Deps) int {
	file, flagArgs, err := splitCaptureArgs(args)
	if err != nil {
		return usageError(deps, err)
	}
	fs := flag.NewFlagSet("fixtures import-pcap", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	outDir := fs.String("out", "testdata/captures", "corpus directory to import into")
	caseName := fs.String("case", "", "fixture case name (default: the capture file's base name)")
	modelName := fs.String("model", "", "Model the captured Device is (e.g. NUT87)")
	usbSel := fs.String("usb", "", "which captured Device to import: <bus>.<dev> (default: the one carrying protocol traffic)")
	if err := fs.Parse(flagArgs); err != nil {
		return usageError(deps, err)
	}
	if *modelName == "" {
		return usageError(deps, fmt.Errorf("import-pcap needs --model: a kernel capture carries no USB product strings to identify the Device by"))
	}
	model, err := device.ModelByName(*modelName)
	if err != nil {
		return usageError(deps, err)
	}
	sel, err := parseUSBSelection(*usbSel)
	if err != nil {
		return usageError(deps, err)
	}
	urbs, err := usbmon.ReadFile(file)
	if err != nil {
		return fail(deps, fmt.Errorf("import-pcap: %w", err))
	}
	imp := pcapImport{
		model:    model,
		outDir:   *outDir,
		caseName: *caseName,
		path:     file,
		now:      time.Now(),
	}
	if imp.caseName == "" {
		imp.caseName = strings.TrimSuffix(filepath.Base(file), filepath.Ext(file))
	}
	return imp.run(urbs, sel, deps)
}

// splitCaptureArgs separates the capture file from the flags: the file comes
// first (`nutctl fixtures import-pcap nut87.pcapng --model NUT87`,
// docs/capture.md) and Go's flag parsing stops at the first positional
// argument. Every flag of this command takes a value.
func splitCaptureArgs(args []string) (file string, flags []string, err error) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "-") {
			if file != "" {
				return "", nil, fmt.Errorf("import-pcap takes exactly one capture file, got %q and %q", file, a)
			}
			file = a
			continue
		}
		flags = append(flags, a)
		if !strings.Contains(a, "=") && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
			i++
			flags = append(flags, args[i])
		}
	}
	if file == "" {
		return "", nil, fmt.Errorf("import-pcap needs exactly one capture file")
	}
	return file, flags, nil
}

// devKey is one Device in the capture: usbmon identifies Devices by bus and
// device number (a capture carries no product strings).
type devKey struct {
	bus uint16
	dev uint8
}

func (d devKey) String() string { return fmt.Sprintf("USB %d.%d", d.bus, d.dev) }

// parseUSBSelection parses a --usb <bus>.<dev> selector (nil: none given).
func parseUSBSelection(s string) (*devKey, error) {
	if s == "" {
		return nil, nil
	}
	var bus uint16
	var dev uint8
	if _, err := fmt.Sscanf(s, "%d.%d", &bus, &dev); err != nil {
		return nil, fmt.Errorf("--usb wants <bus>.<dev> as lsusb names it (e.g. 2.14), got %q", s)
	}
	d := devKey{bus, dev}
	return &d, nil
}

// capturedReport is one HID report of the capture, classified by the framing
// it carries (protocol.ParseReport).
type capturedReport struct {
	rep  usbmon.Report
	kind protocol.ReportKind
	data []byte // framing payload (report-id prefix stripped)
}

// where names the stream the report rode, for the traffic report.
func (r capturedReport) where() string {
	return fmt.Sprintf("%s %s", devKey{r.rep.Bus, r.rep.Dev}, r.rep.Stream)
}

// framed reports whether the report carries the request/response framing —
// the traffic that becomes fixtures.
func (r capturedReport) framed() bool {
	return r.kind == protocol.ReportRequest || r.kind == protocol.ReportResponse
}

// pcapImport is one capture's import: Device selection, transfer grouping
// and the loud accounting of everything that does not become a fixture.
type pcapImport struct {
	model    device.Model
	outDir   string
	caseName string
	path     string
	now      time.Time
}

func (p pcapImport) run(urbs []usbmon.URB, sel *devKey, deps Deps) int {
	skip := newTrafficSkipped()
	var reports []capturedReport
	for _, u := range urbs {
		rep, ok := u.Report()
		if !ok {
			skip.noReport(devKey{u.Bus, u.Dev})
			continue
		}
		kind, data := protocol.ParseReport(rep.Data)
		reports = append(reports, capturedReport{rep: rep, kind: kind, data: data})
	}

	// Select the Device: the one whose traffic carries protocol framing, or
	// the one --usb names. Several Devices carrying framing is ambiguous —
	// never a best guess.
	var candidates []devKey
	seen := map[devKey]bool{}
	for _, r := range reports {
		if r.framed() {
			dev := devKey{r.rep.Bus, r.rep.Dev}
			if !seen[dev] {
				seen[dev] = true
				candidates = append(candidates, dev)
			}
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].bus != candidates[j].bus {
			return candidates[i].bus < candidates[j].bus
		}
		return candidates[i].dev < candidates[j].dev
	})
	chosen, err := selectDevice(candidates, sel)
	if err != nil {
		skip.unassigned(reports)
		fmt.Fprint(deps.Stderr, skip.render(p.path))
		return fail(deps, fmt.Errorf("import-pcap: %w", err))
	}

	// The Device's protocol streams: the endpoints/interfaces that carry
	// framing. Report-shaped traffic elsewhere (key reports on a keyboard's
	// own endpoint) is traffic, not protocol — it must not be mistaken for a
	// parse failure.
	streams := map[usbmon.Stream]bool{}
	for _, r := range reports {
		dev := devKey{r.rep.Bus, r.rep.Dev}
		if dev == chosen && r.framed() {
			streams[r.rep.Stream] = true
		}
	}

	// Keep the protocol's reports, in capture order, and account for every
	// other event of the capture.
	var (
		log      []protocol.WireReport
		imported []capturedReport
		prefixed int // reports that carried the HID report-id prefix
	)
	for _, r := range reports {
		dev := devKey{r.rep.Bus, r.rep.Dev}
		if dev != chosen {
			skip.addOther(skipGroup{Where: dev.String(), Note: "reports of another Device"}, r.data)
			continue
		}
		switch r.kind {
		case protocol.ReportRequest, protocol.ReportResponse:
			if len(r.data) != len(r.rep.Data) {
				prefixed++
			}
			log = append(log, protocol.WireReport{Sent: r.kind == protocol.ReportRequest, Report: r.data})
			imported = append(imported, r)
		case protocol.ReportWake:
			skip.wake++
		case protocol.ReportUnparseable:
			if streams[r.rep.Stream] {
				skip.addUnparseable(r)
			} else {
				skip.addOther(skipGroup{Where: r.where(), Note: "protocol-sized reports without framing, outside the protocol streams — if the protocol rides this stream, that is a parse failure (keep the bytes)"}, r.data)
			}
		case protocol.ReportForeign:
			skip.addOther(skipGroup{Where: r.where(), Note: "reports of another size than the 32/64-byte reports this protocol speaks (docs/protocol.md §1/§6.5)"}, r.data)
		}
	}

	// Group the wire log into the per-transfer exchanges the corpus stores —
	// the same grouping `nutctl fixtures record` does (protocol.SplitTransfers).
	transfers, err := protocol.SplitTransfers(log)
	if err != nil {
		return fail(deps, fmt.Errorf("import-pcap: %w", err))
	}
	var valid []protocol.WireTransfer
	for _, tr := range transfers {
		if err := protocol.ValidateTransfer(tr); err != nil {
			skip.addInvalid(tr, err)
			continue
		}
		valid = append(valid, tr)
	}
	if len(valid) == 0 {
		fmt.Fprint(deps.Stderr, skip.render(p.path))
		return fail(deps, fmt.Errorf("import-pcap: %s carries no request/response exchanges that validate as %s traffic", p.path, p.model.Name))
	}

	// Provenance: the firmware version and USB ids the capture's own
	// GET_DEVICE_INFO carries (docs/capture.md). A capture of another Device
	// than the Model named is refused — a fixture whose Device is wrong is
	// not evidence.
	di, probed, err := capturedProbe(valid)
	if err != nil {
		return fail(deps, fmt.Errorf("import-pcap: the capture's GET_DEVICE_INFO does not decode: %w", err))
	}
	if probed && (di.VID != p.model.VendorID || di.PID != p.model.ProductID) {
		return fail(deps, fmt.Errorf("import-pcap: the capture's GET_DEVICE_INFO reports USB %04x:%04x, but --model %s is USB %04x:%04x — refusing to import a capture of another Device",
			di.VID, di.PID, p.model.Name, p.model.VendorID, p.model.ProductID))
	}
	firmware := ""
	if probed {
		firmware = di.Version
	}

	written, err := writeFixtures(valid, writeOpts{
		OutDir: p.outDir,
		Case:   p.caseName,
		Meta: fixture.Meta{
			Model:         p.model.Name,
			Connection:    string(p.model.Connection),
			Firmware:      firmware,
			CaptureMethod: "usbmon-pcap",
			Source:        p.source(chosen, imported, firmwareLabel(firmware)),
		},
	}, deps.Stdout)
	if err != nil {
		return fail(deps, err)
	}
	fmt.Fprintf(deps.Stdout, "imported %d exchange(s) into %s\n", written, p.outDir)
	if prefixed > 0 {
		fmt.Fprintf(deps.Stdout, "stripped the HID report-id prefix of %s (output report id 0)\n", counted(prefixed, "report(s)"))
	}
	fmt.Fprint(deps.Stderr, skip.render(p.path))
	if skip.loud() {
		return fail(deps, fmt.Errorf("import-pcap: %s and %s left behind unimported — %d exchange(s) imported anyway; see the report above",
			counted(len(skip.unparseable), "report(s) unparseable"), counted(len(skip.invalid), "exchange(s) failing the framing"), written))
	}
	return 0
}

// source composes the capture provenance meta.json records (the ticket's
// "pcap source, device, firmware where known"): which capture, which Device
// on it, which streams and report length, when it was captured.
func (p pcapImport) source(chosen devKey, imported []capturedReport, firmware string) string {
	streamSet := map[string]bool{}
	lengthSet := map[int]bool{}
	var captured time.Time
	for _, r := range imported {
		streamSet[r.rep.Stream.String()] = true
		lengthSet[len(r.data)] = true
		if captured.IsZero() || r.rep.Time.Before(captured) {
			captured = r.rep.Time
		}
	}
	streams := make([]string, 0, len(streamSet))
	for s := range streamSet {
		streams = append(streams, s)
	}
	sort.Strings(streams)
	lengths := make([]string, 0, len(lengthSet))
	for l := range lengthSet {
		lengths = append(lengths, fmt.Sprintf("%d", l))
	}
	sort.Strings(lengths)
	capturedShown := "unknown"
	if !captured.IsZero() {
		capturedShown = captured.Format("2006-01-02")
	}
	return fmt.Sprintf(
		"imported %s from %s (usbmon capture — docs/capture.md Method B): %s USB %04x:%04x at bus %d device %d, reports via %s, %s-byte reports, captured %s, firmware %s",
		p.now.Format("2006-01-02"), p.path, p.model.Name,
		p.model.VendorID, p.model.ProductID, chosen.bus, chosen.dev,
		strings.Join(streams, ", "), strings.Join(lengths, "/"),
		capturedShown, firmware)
}

// selectDevice picks the captured Device to import: --usb's choice, or the
// one carrying protocol traffic when exactly one does.
func selectDevice(candidates []devKey, sel *devKey) (devKey, error) {
	if sel != nil {
		for _, c := range candidates {
			if c == *sel {
				return c, nil
			}
		}
		return devKey{}, fmt.Errorf("%s carries no protocol traffic in this capture (Devices that do: %s)", sel, listDevices(candidates))
	}
	switch len(candidates) {
	case 0:
		return devKey{}, fmt.Errorf("no protocol traffic found in this capture")
	case 1:
		return candidates[0], nil
	default:
		return devKey{}, fmt.Errorf("the capture carries protocol traffic for %d Devices (%s) — pick one with --usb",
			len(candidates), listDevices(candidates))
	}
}

func listDevices(devs []devKey) string {
	if len(devs) == 0 {
		return "none"
	}
	names := make([]string, len(devs))
	for i, d := range devs {
		names[i] = d.String()
	}
	return strings.Join(names, ", ")
}

// counted renders a count with its noun ("3 report(s) unparseable").
func counted(n int, noun string) string { return fmt.Sprintf("%d %s", n, noun) }

// maxListed caps the per-event listings of the traffic report; the counts
// stay exact.
const maxListed = 10

// skipEvent is one event the importer did not turn into a fixture, named
// with where it was and what it carried.
type skipEvent struct {
	At    time.Time
	Where string
	Data  []byte
	Note  string
}

// skipGroup is a bucket of same-shaped events (other Devices, non-report
// URBs, foreign endpoints), counted and sampled.
type skipGroup struct {
	Where  string
	Note   string
	Count  int
	Size   int
	Sample []byte
}

// trafficSkipped is everything the importer saw and did not turn into
// fixtures. The ticket's rule: unparseable traffic is reported loudly
// instead of silently dropped — every capture event ends up either in a
// fixture or in this report.
type trafficSkipped struct {
	unparseable []skipEvent
	invalid     []skipEvent
	wake        int
	order       []string
	other       map[string]*skipGroup
}

func newTrafficSkipped() *trafficSkipped { return &trafficSkipped{other: map[string]*skipGroup{}} }

// unparseable records a protocol-shaped report on a protocol stream that
// does not parse: a parse failure is a bug report (docs/capture.md Method B).
func (s *trafficSkipped) addUnparseable(r capturedReport) {
	s.unparseable = append(s.unparseable, skipEvent{
		At: r.rep.Time, Where: r.where(), Data: r.data,
		Note: "no framing this protocol knows",
	})
}

// invalid records an exchange that fails the framing validation — it is not
// imported (imported fixtures validate), and its failure is reported.
func (s *trafficSkipped) addInvalid(tr protocol.WireTransfer, err error) {
	s.invalid = append(s.invalid, skipEvent{
		Where: fmt.Sprintf("%s (%d request / %d response reports)",
			protocol.TransferName(tr), len(tr.Requests), len(tr.Responses)),
		Note: strings.ReplaceAll(err.Error(), "\n", "; "),
	})
}

// noReport records a URB that carries no HID report (handshake, descriptor,
// bulk/isochronous traffic).
func (s *trafficSkipped) noReport(dev devKey) {
	s.group(skipGroup{Where: dev.String(), Note: "URBs carrying no HID report"}, 0, nil)
}

// other records a report that is not protocol traffic, in a bucket counted
// and sampled by where it appeared.
func (s *trafficSkipped) addOther(g skipGroup, data []byte) {
	s.group(g, len(data), data)
}

func (s *trafficSkipped) group(g skipGroup, size int, sample []byte) {
	key := g.Where + "\x00" + g.Note
	e, ok := s.other[key]
	if !ok {
		e = &skipGroup{Where: g.Where, Note: g.Note, Size: size, Sample: bytes.Clone(sample)}
		s.other[key] = e
		s.order = append(s.order, key)
	}
	e.Count++
}

// unassigned records reports of a capture where no Device could be selected:
// none of them was imported, and the report must say what the capture holds.
func (s *trafficSkipped) unassigned(reports []capturedReport) {
	for _, r := range reports {
		s.addOther(skipGroup{Where: devKey{r.rep.Bus, r.rep.Dev}.String(), Note: "reports in the capture (no Device selected)"}, r.data)
	}
}

// loud reports whether the traffic report is a bug report: unparseable
// traffic or exchanges that fail the framing.
func (s *trafficSkipped) loud() bool { return len(s.unparseable) > 0 || len(s.invalid) > 0 }

// render writes the traffic report: what was not imported, loud and with its
// bytes. Nothing in the capture is dropped silently (the ticket's rule).
func (s *trafficSkipped) render(path string) string {
	if !s.loud() && s.wake == 0 && len(s.order) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "import-pcap: traffic NOT imported from %s (nothing is dropped silently):\n", path)
	if n := len(s.unparseable); n > 0 {
		fmt.Fprintf(&b, "  %s on the Device's protocol streams — a parse failure is a bug report; keep these bytes!\n",
			counted(n, "unparseable report(s)"))
		for i, e := range s.unparseable {
			if i == maxListed {
				fmt.Fprintf(&b, "    … and %d more\n", n-maxListed)
				break
			}
			fmt.Fprintf(&b, "    %s %s (%d bytes): % X\n",
				e.At.Format("2006-01-02 15:04:05.000000"), e.Where, len(e.Data), e.Data)
		}
	}
	if n := len(s.invalid); n > 0 {
		fmt.Fprintf(&b, "  %s that fail the protocol framing (docs/protocol.md §2) — not imported:\n",
			counted(n, "exchange(s)"))
		for i, e := range s.invalid {
			if i == maxListed {
				fmt.Fprintf(&b, "    … and %d more\n", n-maxListed)
				break
			}
			fmt.Fprintf(&b, "    %s: %s\n", e.Where, e.Note)
		}
	}
	if s.wake > 0 {
		fmt.Fprintf(&b, "  %s (documented 2.4G traffic, out of scope for USB imports)\n",
			counted(s.wake, "2.4G wake report(s)"))
	}
	if len(s.order) > 0 {
		b.WriteString("  other traffic (not protocol reports):\n")
		for _, key := range s.order {
			g := s.other[key]
			if g.Sample != nil {
				fmt.Fprintf(&b, "    %s — %s: %s of %d bytes, e.g. % X\n",
					g.Where, g.Note, counted(g.Count, "report(s)"), g.Size, g.Sample)
			} else {
				fmt.Fprintf(&b, "    %s — %s: %s\n", g.Where, g.Note, counted(g.Count, "event(s)"))
			}
		}
	}
	return b.String()
}
