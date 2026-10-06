package cli

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ht4w5/nutctl/internal/hidfake"
)

// --- `nutctl fixtures import-pcap` (ticket 10, docs/capture.md Method B) ---
//
// Vendor-app traffic captured at the kernel level (usbmon while the official
// app runs in a Windows VM) becomes fixtures too: the importer reassembles
// the captured URBs into the same framed request/response exchanges the
// recorder stores, so our implementation can be cross-checked against the
// official one. Imported fixtures validate against the protocol framing;
// traffic that cannot become a fixture is reported loudly instead of
// silently dropped.

// captureBuilder assembles a kernel capture: a pcapng file of usbmon
// packets, the bytes `tshark -i usbmonN -w` writes. Every field is built
// literally (the usbmon_packet header of Documentation/usb/usbmon.rst) — the
// importer must decode what the kernel produces, not what the test would
// like it to produce.
type captureBuilder struct {
	frames [][]byte
	seq    uint64
}

// usbmonFrame builds one mmap'd usbmon packet (64-byte header + data stage).
func (b *captureBuilder) usbmonFrame(event, xfer, ep, dev byte, bus uint16, setup [8]byte, data []byte) []byte {
	b.seq++
	f := make([]byte, 64+len(data))
	binary.LittleEndian.PutUint64(f[0:], b.seq)
	f[8] = event
	f[9] = xfer
	f[10] = ep
	f[11] = dev
	binary.LittleEndian.PutUint16(f[12:], bus)
	f[14] = 0xFF
	f[15] = 0x00
	if setup != [8]byte{} {
		copy(f[40:48], setup[:])
		f[14] = 0x00
	}
	ts := b.timestamp()
	binary.LittleEndian.PutUint64(f[16:], uint64(ts.Unix()))
	binary.LittleEndian.PutUint32(f[24:], uint32(ts.Nanosecond()/1000))
	binary.LittleEndian.PutUint32(f[32:], uint32(len(data)))
	binary.LittleEndian.PutUint32(f[36:], uint32(len(data)))
	copy(f[64:], data)
	b.frames = append(b.frames, f)
	return f
}

func (b *captureBuilder) timestamp() time.Time {
	return time.Unix(1_760_000_000, 0).Add(time.Duration(b.seq) * time.Millisecond)
}

// interruptOut appends one report as an interrupt OUT submit URB (host →
// device), interruptIn one as an interrupt IN complete URB (device → host).
func (b *captureBuilder) interruptOut(report []byte) {
	b.usbmonFrame('S', 1, 0x02, 14, 2, [8]byte{}, report)
}

func (b *captureBuilder) interruptIn(report []byte) {
	b.usbmonFrame('C', 1, 0x81, 14, 2, [8]byte{}, report)
}

// setReport appends one report as a SET_REPORT control transfer (0x21 0x09)
// — how the vendor app's writes may arrive too (docs/capture.md Method B).
func (b *captureBuilder) setReport(report []byte) {
	b.usbmonFrame('S', 2, 0x00, 14, 2, [8]byte{0x21, 0x09, 0x02, 0x00, 0x02, 0x00, byte(len(report)), 0x00}, report)
}

// getReport appends one report as a GET_REPORT control transfer (0xA1 0x01).
func (b *captureBuilder) getReport(report []byte) {
	b.usbmonFrame('C', 2, 0x00, 14, 2, [8]byte{0xA1, 0x01, 0x02, 0x00, 0x02, 0x00, byte(len(report)), 0x00}, report)
}

// pcapng renders the capture the way tshark -w writes it: one little-endian
// section, one USB Linux mmap'd interface, one enhanced packet block per
// URB (timestamps in microseconds).
func (b *captureBuilder) pcapng() []byte {
	var out bytes.Buffer
	block := func(typ uint32, body []byte) {
		binary.Write(&out, binary.LittleEndian, typ)
		total := uint32(12 + len(body))
		binary.Write(&out, binary.LittleEndian, total)
		out.Write(body)
		binary.Write(&out, binary.LittleEndian, total)
	}
	var shb bytes.Buffer
	binary.Write(&shb, binary.LittleEndian, uint32(0x1A2B3C4D))
	binary.Write(&shb, binary.LittleEndian, uint16(1))
	binary.Write(&shb, binary.LittleEndian, uint16(0))
	binary.Write(&shb, binary.LittleEndian, uint64(0xFFFFFFFFFFFFFFFF))
	block(0x0A0D0D0A, shb.Bytes())

	var idb bytes.Buffer
	binary.Write(&idb, binary.LittleEndian, uint16(220)) // LINKTYPE_USB_LINUX_MMAPPED
	binary.Write(&idb, binary.LittleEndian, uint16(0))
	binary.Write(&idb, binary.LittleEndian, uint32(65535))
	block(1, idb.Bytes())

	for i, f := range b.frames {
		var epb bytes.Buffer
		binary.Write(&epb, binary.LittleEndian, uint32(0))
		ts := time.Unix(1_760_000_000, 0).Add(time.Duration(i+1) * time.Millisecond).UnixMicro()
		binary.Write(&epb, binary.LittleEndian, uint32(ts>>32))
		binary.Write(&epb, binary.LittleEndian, uint32(ts))
		binary.Write(&epb, binary.LittleEndian, uint32(len(f)))
		binary.Write(&epb, binary.LittleEndian, uint32(len(f)))
		epb.Write(f)
		for epb.Len()%4 != 0 {
			epb.WriteByte(0)
		}
		block(6, epb.Bytes())
	}
	return out.Bytes()
}

// writeCapture stores one capture file and returns its name.
func writeCapture(t *testing.T, name string, b *captureBuilder) string {
	t.Helper()
	if err := os.WriteFile(name, b.pcapng(), 0o644); err != nil {
		t.Fatal(err)
	}
	return name
}

// The happy path: a capture of the vendor app reading the Device — the
// committed read-path fixtures' own reports, as usbmon saw them — imports
// back into the corpus format, byte for byte, with the capture provenance in
// the metadata.
func TestFixturesImportPcapImportsTheVendorCapture(t *testing.T) {
	t.Chdir(t.TempDir())
	cap := &captureBuilder{}
	for _, x := range readPathFixtures(t) {
		for i := range x.Requests {
			cap.interruptOut(x.Requests[i])
			if i < len(x.Responses) {
				cap.interruptIn(x.Responses[i])
			}
		}
	}
	writeCapture(t, "cap.pcapng", cap)

	d := newNut87Fake(t, "/dev/hidraw9")
	code, out, errOut := run(t, &hidfake.Enumerator{Devices: []*hidfake.Device{d}},
		"fixtures", "import-pcap", "cap.pcapng", "--model", "NUT87", "--out", "corpus")
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr: %s)", code, errOut)
	}
	if !strings.Contains(out, "imported 6 exchange(s)") {
		t.Errorf("output does not report the imported exchanges:\n%s", out)
	}
	if d.Opened() {
		t.Error("import-pcap touched a Device — importing a capture is offline")
	}

	for _, dir := range []string{
		"get_device_info", "get_key", "get_fn_key",
		"get_led_effect", "get_custom_led_data", "get_game_mode",
	} {
		got := recordedFixture(t, dir, "cap") // default case name: the capture file's base name
		want := loadFixture(t, dir, "nut87")
		assertSameWire(t, got, want)
		if got.Cmd != want.Cmd {
			t.Errorf("%s: meta cmd = %q, want %q", dir, got.Cmd, want.Cmd)
		}
		for _, f := range []struct{ key, got, want string }{
			{"model", got.Meta.Model, "NUT87"},
			{"connection", got.Meta.Connection, "USB"},
			{"firmware", got.Meta.Firmware, "1.20"},
			{"captureMethod", got.Meta.CaptureMethod, "usbmon-pcap"},
		} {
			if f.got != f.want {
				t.Errorf("%s: meta %s = %q, want %q", dir, f.key, f.got, f.want)
			}
		}
		for _, prov := range []string{
			"cap.pcapng", "docs/capture.md Method B",
			"bus 2 device 14", "64-byte reports", "firmware 1.20",
		} {
			if !strings.Contains(got.Meta.Source, prov) {
				t.Errorf("%s: meta source does not carry %q (capture provenance):\n%s", dir, prov, got.Meta.Source)
			}
		}
	}
}

// The write path arrives as SET_REPORT control transfers too (Method B:
// "Interrupt OUT / SET_REPORT control transfers carrying our 0xAA request
// frames") — the importer reassembles those into the same fixtures.
func TestFixturesImportPcapReadsControlTransfers(t *testing.T) {
	t.Chdir(t.TempDir())
	cap := &captureBuilder{}
	x := loadFixture(t, "set_led_effect", "nut87")
	for i := range x.Requests {
		cap.setReport(x.Requests[i])
		if i < len(x.Responses) {
			cap.getReport(x.Responses[i])
		}
	}
	writeCapture(t, "writes.pcapng", cap)

	code, out, errOut := run(t, nil,
		"fixtures", "import-pcap", "writes.pcapng", "--model", "NUT87", "--out", "corpus")
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr: %s)", code, errOut)
	}
	if !strings.Contains(out, "set_led_effect/writes") {
		t.Errorf("output does not name the imported exchange:\n%s", out)
	}
	got := recordedFixture(t, "set_led_effect", "writes")
	assertSameWire(t, got, x)
	if !strings.Contains(got.Meta.Source, "control interface 2") {
		t.Errorf("meta source does not name the stream the reports rode:\n%s", got.Meta.Source)
	}
}

// Reports may carry the HID report-id prefix the kernel adds on hidraw
// writes (output report id 0): the importer strips it and says so, rather
// than refusing the whole capture.
func TestFixturesImportPcapStripsReportIDPrefixes(t *testing.T) {
	t.Chdir(t.TempDir())
	cap := &captureBuilder{}
	x := loadFixture(t, "get_led_effect", "nut87")
	prefixed := func(report []byte) []byte {
		return append([]byte{0x00}, report...) // report id 0
	}
	cap.interruptOut(prefixed(x.Requests[0]))
	cap.interruptIn(prefixed(x.Responses[0]))
	writeCapture(t, "prefixed.pcapng", cap)

	code, out, errOut := run(t, nil,
		"fixtures", "import-pcap", "prefixed.pcapng", "--model", "NUT87", "--out", "corpus")
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr: %s)", code, errOut)
	}
	got := recordedFixture(t, "get_led_effect", "prefixed")
	assertSameWire(t, got, x) // fixtures carry the framing bytes, not the prefix
	if !strings.Contains(out, "report-id prefix") {
		t.Errorf("output does not report the stripped prefixes:\n%s", out)
	}
}

// The ticket's loudness rule: traffic that does not become a fixture is
// reported with its bytes, never dropped silently. Unparseable traffic on
// the Device's protocol streams is a parse failure — a bug report — and
// fails the import loudly even though the exchanges that did parse are
// imported.
func TestFixturesImportPcapReportsUnparseableTrafficLoudly(t *testing.T) {
	t.Chdir(t.TempDir())
	cap := &captureBuilder{}
	for _, x := range readPathFixtures(t) {
		for i := range x.Requests {
			cap.interruptOut(x.Requests[i])
			if i < len(x.Responses) {
				cap.interruptIn(x.Responses[i])
			}
		}
	}
	// Device notify traffic (docs/protocol.md §4): imports as its own
	// response-only fixture.
	notify := make([]byte, 64)
	notify[0], notify[1], notify[2] = 0x55, 0xFA, 0x06
	notify[3] = 0x01
	cap.interruptIn(notify)
	// Garbage on the protocol stream: a parse failure, and a bug report.
	garbage := bytes.Repeat([]byte{0x7E}, 64)
	cap.interruptIn(garbage)
	// Traffic that is not protocol at all: key reports on the keyboard's own
	// endpoint, another Device on the bus, a descriptor read.
	cap.usbmonFrame('C', 1, 0x83, 14, 2, [8]byte{}, []byte{0x04, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00})
	cap.interruptOut([]byte{0x01}) // keyboard LED report (control/interrupt LED write)
	cap.usbmonFrame('C', 1, 0x81, 3, 1, [8]byte{}, bytes.Repeat([]byte{0x22}, 64))
	writeCapture(t, "mixed.pcapng", cap)

	code, out, errOut := run(t, nil,
		"fixtures", "import-pcap", "mixed.pcapng", "--model", "NUT87", "--out", "corpus")
	if code != 1 {
		t.Fatalf("exit = %d, want 1 (unparseable traffic is a bug report); stderr:\n%s", code, errOut)
	}
	if !strings.Contains(out, "imported 7 exchange(s)") {
		t.Errorf("the exchanges that do parse must still be imported:\n%s", out)
	}
	// The notify traffic becomes a response-only fixture (ticket 08's
	// follow-up: notify fixtures from captures).
	if got := recordedFixture(t, "get_device_notify", "mixed"); len(got.Requests) != 0 || len(got.Responses) != 1 {
		t.Errorf("notify fixture = %d request reports, %d responses; want 0 and 1",
			len(got.Requests), len(got.Responses))
	}
	// Loud: the unparseable report, with its bytes.
	for _, want := range []string{
		"unparseable", "bug report", "7E 7E", // the garbage bytes
	} {
		if !strings.Contains(errOut, want) {
			t.Errorf("stderr does not report %q loudly:\n%s", want, errOut)
		}
	}
	// Loud: the traffic that is not protocol, counted and sampled.
	for _, want := range []string{
		"endpoint 0x83", "04 00 00 00", // the key reports
		"USB 1.3", // the other Device
	} {
		if !strings.Contains(errOut, want) {
			t.Errorf("stderr does not account for %q (nothing is dropped silently):\n%s", want, errOut)
		}
	}
}

// Traffic that is not protocol and not a parse failure (key reports, other
// Devices, 2.4G wake reports) is reported, but is no bug report: the import
// succeeds.
func TestFixturesImportPcapAccountsForForeignTrafficWithoutFailing(t *testing.T) {
	t.Chdir(t.TempDir())
	cap := &captureBuilder{}
	x := loadFixture(t, "get_led_effect", "nut87")
	cap.interruptOut(x.Requests[0])
	cap.interruptIn(x.Responses[0])
	// The documented 2.4G wake report (§4, out of scope for USB imports).
	wake := append([]byte{0xA6, 0xFF, 0x01}, make([]byte, 61)...)
	cap.interruptIn(wake)
	cap.usbmonFrame('C', 1, 0x83, 14, 2, [8]byte{}, []byte{0x04, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00})
	writeCapture(t, "foreign.pcapng", cap)

	code, out, errOut := run(t, nil,
		"fixtures", "import-pcap", "foreign.pcapng", "--model", "NUT87", "--out", "corpus")
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (foreign traffic is expected noise); stderr:\n%s", code, errOut)
	}
	if !strings.Contains(out, "imported 1 exchange(s)") {
		t.Errorf("output does not report the imported exchange:\n%s", out)
	}
	for _, want := range []string{"2.4G wake", "endpoint 0x83"} {
		if !strings.Contains(errOut, want) {
			t.Errorf("stderr does not account for %q:\n%s", want, errOut)
		}
	}
}

// An exchange that fails the framing validation is not imported (imported
// fixtures validate against the protocol framing) — its failure is reported
// loudly instead.
func TestFixturesImportPcapRefusesExchangesThatFailTheFraming(t *testing.T) {
	t.Chdir(t.TempDir())
	cap := &captureBuilder{}
	good := loadFixture(t, "get_led_effect", "nut87")
	cap.interruptOut(good.Requests[0])
	cap.interruptIn(good.Responses[0])
	// A response that does not mirror its request chunk (§2).
	bad := loadFixture(t, "get_led_effect", "nut87")
	response := bytes.Clone(bad.Responses[0])
	response[3], response[4] = 0x34, 0x12 // addr 0x1234, the request says 0
	cap.interruptOut(bad.Requests[0])
	cap.interruptIn(response)
	writeCapture(t, "broken.pcapng", cap)

	code, out, errOut := run(t, nil,
		"fixtures", "import-pcap", "broken.pcapng", "--model", "NUT87", "--out", "corpus")
	if code != 1 {
		t.Fatalf("exit = %d, want 1 (a framing failure is a bug report); stderr:\n%s", code, errOut)
	}
	if !strings.Contains(out, "imported 1 exchange(s)") {
		t.Errorf("the valid exchange must still be imported:\n%s", out)
	}
	for _, want := range []string{"GET_LED_EFFECT", "mirrors the request offset", "not imported"} {
		if !strings.Contains(errOut, want) {
			t.Errorf("stderr does not report the framing failure %q:\n%s", want, errOut)
		}
	}
	if _, err := os.Stat(filepath.Join("corpus", "get_led_effect", "broken-2.meta.json")); err == nil {
		t.Error("the invalid exchange was imported; imported fixtures must validate")
	}
}

// A bus capture may hold several Devices speaking the protocol — never a
// best guess: --usb picks one.
func TestFixturesImportPcapSelectsTheDevice(t *testing.T) {
	t.Chdir(t.TempDir())
	build := func(dev byte, bus uint16) *captureBuilder {
		cap := &captureBuilder{}
		x := loadFixture(t, "get_led_effect", "nut87")
		cap.usbmonFrame('S', 1, 0x02, dev, bus, [8]byte{}, x.Requests[0])
		cap.usbmonFrame('C', 1, 0x81, dev, bus, [8]byte{}, x.Responses[0])
		return cap
	}
	mixed := build(14, 2)
	other := build(5, 3)
	mixed.frames = append(mixed.frames, other.frames...)
	writeCapture(t, "two.pcapng", mixed)

	code, _, errOut := run(t, nil,
		"fixtures", "import-pcap", "two.pcapng", "--model", "NUT87", "--out", "corpus")
	if code != 1 {
		t.Fatalf("exit = %d, want 1 (ambiguous Devices); stderr:\n%s", code, errOut)
	}
	for _, want := range []string{"--usb", "USB 2.14", "USB 3.5"} {
		if !strings.Contains(errOut, want) {
			t.Errorf("stderr does not name the choice %q:\n%s", want, errOut)
		}
	}

	code, out, errOut := run(t, nil,
		"fixtures", "import-pcap", "two.pcapng", "--model", "NUT87", "--out", "corpus2", "--usb", "3.5")
	if code != 0 {
		t.Fatalf("--usb exit = %d, want 0; stderr:\n%s", code, errOut)
	}
	if !strings.Contains(out, "imported 1 exchange(s) into corpus2") {
		t.Errorf("output does not report the import:\n%s", out)
	}
	got := recordedFixtureIn(t, "corpus2", "get_led_effect", "two")
	assertSameWire(t, got, loadFixture(t, "get_led_effect", "nut87"))
	if !strings.Contains(got.Meta.Source, "bus 3 device 5") {
		t.Errorf("meta source does not name the selected Device:\n%s", got.Meta.Source)
	}
}

// A capture of another Device than the Model named is refused: a fixture
// whose Device is wrong is not evidence (the recorder's refusal rule).
func TestFixturesImportPcapRefusesACaptureOfAnotherDevice(t *testing.T) {
	t.Chdir(t.TempDir())
	x := loadFixture(t, "get_device_info", "nut87")
	response := bytes.Clone(x.Responses[0])
	response[12], response[13] = 0x11, 0x11 // payload vid 0x1111
	response[14], response[15] = 0x22, 0x22 // payload pid 0x2222
	cap := &captureBuilder{}
	cap.interruptOut(x.Requests[0])
	cap.interruptIn(response)
	writeCapture(t, "other-device.pcapng", cap)

	code, _, errOut := run(t, nil,
		"fixtures", "import-pcap", "other-device.pcapng", "--model", "NUT87", "--out", "corpus")
	if code != 1 {
		t.Fatalf("exit = %d, want 1 (refused); stderr:\n%s", code, errOut)
	}
	for _, want := range []string{"GET_DEVICE_INFO reports USB 1111:2222", "refusing to import a capture of another Device"} {
		if !strings.Contains(errOut, want) {
			t.Errorf("stderr does not refuse with %q:\n%s", want, errOut)
		}
	}
	if _, err := os.Stat(filepath.Join("corpus", "get_device_info")); err == nil {
		t.Error("a refused capture must not write fixtures")
	}
}

// Bad importer input is a usage error (exit 2); unreadable captures are
// runtime failures (exit 1). Neither touches anything.
func TestFixturesImportPcapUsageErrors(t *testing.T) {
	t.Chdir(t.TempDir())
	writeCapture(t, "ok.pcapng", &captureBuilder{})
	for _, tc := range []struct {
		args []string
		code int
	}{
		{[]string{"fixtures", "import-pcap"}, 2},                                  // no capture file
		{[]string{"fixtures", "import-pcap", "ok.pcapng"}, 2},                     // no --model
		{[]string{"fixtures", "import-pcap", "ok.pcapng", "--model", "NUT60"}, 2}, // unknown Model
		{[]string{"fixtures", "import-pcap", "ok.pcapng", "--model", "NUT87", "--usb", "nonsense"}, 2},
		{[]string{"fixtures", "import-pcap", "a.pcapng", "b.pcapng", "--model", "NUT87"}, 2},
		{[]string{"fixtures", "import-pcap", "missing.pcapng", "--model", "NUT87"}, 1},
	} {
		code, _, errOut := run(t, nil, tc.args...)
		if code != tc.code {
			t.Errorf("nutctl %s: exit = %d, want %d (stderr: %s)",
				strings.Join(tc.args, " "), code, tc.code, errOut)
		}
		if !strings.Contains(errOut, "nutctl:") && !strings.Contains(errOut, "error:") {
			t.Errorf("nutctl %s: stderr does not read like an error:\n%s",
				strings.Join(tc.args, " "), errOut)
		}
	}
}
