package usbmon

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"
	"time"
)

// Kernel USB captures (docs/capture.md Method B) arrive as pcap or pcapng
// files of Linux usbmon traffic — what `tshark -i usbmonN -w` writes while
// the vendor app runs in a VM. These tests build capture bytes literally (the
// format facts of pcap, pcapng and the usbmon_packet header) and require the
// reader to decode them back; a reader that guesses wrong here misreads every
// imported fixture.

// --- capture builders (literal format knowledge, independent of the reader) ---

// usbmonPacket builds one usbmon packet: the usbmon_packet header (48 bytes
// on LINKTYPE_USB_LINUX, 64 on ..._MMAPPED — the mmap'd variant adds
// interval/start_frame/xfer_flags/ndesc) followed by the data stage.
func usbmonPacket(hdrLen int, u testURB) []byte {
	b := make([]byte, hdrLen+len(u.data))
	binary.LittleEndian.PutUint64(b[0:], u.id)
	b[8] = u.event
	b[9] = u.xfer
	b[10] = u.ep
	b[11] = u.dev
	binary.LittleEndian.PutUint16(b[12:], u.bus)
	b[14] = 0xFF // flag_setup: no setup packet for non-control
	b[15] = 0x00 // flag_data: data present
	if u.setup != [8]byte{} {
		copy(b[40:48], u.setup[:])
		b[14] = 0x00 // flag_setup: setup packet present
	}
	binary.LittleEndian.PutUint64(b[16:], uint64(u.ts.Unix()))
	binary.LittleEndian.PutUint32(b[24:], uint32(u.ts.Nanosecond()/1000))
	binary.LittleEndian.PutUint32(b[28:], uint32(u.status))
	binary.LittleEndian.PutUint32(b[32:], uint32(len(u.data)))
	binary.LittleEndian.PutUint32(b[36:], uint32(len(u.data)))
	copy(b[hdrLen:], u.data)
	return b
}

type testURB struct {
	id     uint64
	event  byte
	xfer   byte
	ep     byte
	dev    byte
	bus    uint16
	status int32
	setup  [8]byte
	ts     time.Time
	data   []byte
}

// pcapFile wraps packets in a little-endian pcap file with the given link
// type (the classic libpcap container).
func pcapFile(linktype uint32, ts []time.Time, packets [][]byte) []byte {
	var b bytes.Buffer
	b.Write([]byte{0xd4, 0xc3, 0xb2, 0xa1}) // magic: LE, microseconds
	binary.Write(&b, binary.LittleEndian, uint16(2))
	binary.Write(&b, binary.LittleEndian, uint16(4))
	binary.Write(&b, binary.LittleEndian, uint32(0))
	binary.Write(&b, binary.LittleEndian, uint32(0))
	binary.Write(&b, binary.LittleEndian, uint32(65535))
	binary.Write(&b, binary.LittleEndian, linktype)
	for i, p := range packets {
		binary.Write(&b, binary.LittleEndian, uint32(ts[i].Unix()))
		binary.Write(&b, binary.LittleEndian, uint32(ts[i].Nanosecond()/1000))
		binary.Write(&b, binary.LittleEndian, uint32(len(p)))
		binary.Write(&b, binary.LittleEndian, uint32(len(p)))
		b.Write(p)
	}
	return b.Bytes()
}

// pcapngFile wraps packets in a little-endian pcapng file: one section, one
// interface (the tshark/wireshark container).
func pcapngFile(linktype uint16, tsresol byte, ts []time.Time, packets [][]byte) []byte {
	var blocks bytes.Buffer
	block := func(typ uint32, body []byte) {
		binary.Write(&blocks, binary.LittleEndian, typ)
		total := uint32(12 + len(body))
		binary.Write(&blocks, binary.LittleEndian, total)
		blocks.Write(body)
		binary.Write(&blocks, binary.LittleEndian, total)
	}
	var shb bytes.Buffer
	binary.Write(&shb, binary.LittleEndian, uint32(0x1A2B3C4D)) // byte-order magic
	binary.Write(&shb, binary.LittleEndian, uint16(1))
	binary.Write(&shb, binary.LittleEndian, uint16(0))
	binary.Write(&shb, binary.LittleEndian, uint64(0xFFFFFFFFFFFFFFFF))
	block(0x0A0D0D0A, shb.Bytes())

	var idb bytes.Buffer
	binary.Write(&idb, binary.LittleEndian, linktype)
	binary.Write(&idb, binary.LittleEndian, uint16(0))
	binary.Write(&idb, binary.LittleEndian, uint32(65535))
	// if_tsresol (option 9): timestamp resolution as 10^-n.
	idb.Write([]byte{9, 0, 1, tsresol})
	block(1, idb.Bytes())

	for i, p := range packets {
		var epb bytes.Buffer
		binary.Write(&epb, binary.LittleEndian, uint32(0)) // interface id
		// Timestamps are in units of 10^-tsresol seconds.
		units := ts[i].UnixNano() / int64(pow10(tsresol))
		binary.Write(&epb, binary.LittleEndian, uint32(units>>32))
		binary.Write(&epb, binary.LittleEndian, uint32(units))
		binary.Write(&epb, binary.LittleEndian, uint32(len(p)))
		binary.Write(&epb, binary.LittleEndian, uint32(len(p)))
		epb.Write(p)
		for epb.Len()%4 != 0 {
			epb.WriteByte(0)
		}
		block(6, epb.Bytes())
	}
	return blocks.Bytes()
}

func pow10(n byte) time.Duration {
	d := time.Second
	for i := byte(0); i < n; i++ {
		d /= 10
	}
	return d
}

// --- reader ---

func TestReadDecodesPcapLinuxMmapd(t *testing.T) {
	ts := time.Unix(1_760_000_000, 123_456_000)
	data := bytes.Repeat([]byte{0xAA}, 64)
	pkt := usbmonPacket(64, testURB{
		id: 0x1122334455667788, event: 'C', xfer: TransferInterrupt,
		ep: 0x81, dev: 14, bus: 2, ts: ts, data: data,
	})
	urbs, err := Read(bytes.NewReader(pcapFile(linkTypeUSBLinuxMmaped, []time.Time{ts}, [][]byte{pkt})))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(urbs) != 1 {
		t.Fatalf("Read: %d URBs, want 1", len(urbs))
	}
	u := urbs[0]
	if u.Event != 'C' || u.Xfer != TransferInterrupt || u.EP != 0x81 || u.Dev != 14 || u.Bus != 2 {
		t.Errorf("URB header = %+v, want the captured usbmon_packet fields", u)
	}
	if !u.Time.Equal(ts) {
		t.Errorf("URB time = %v, want %v", u.Time, ts)
	}
	if !bytes.Equal(u.Data, data) {
		t.Errorf("URB data = % X, want the captured data stage", u.Data)
	}
}

func TestReadDecodesPcapngLinux(t *testing.T) {
	ts := time.Unix(1_760_000_001, 250_000_000)
	setup := [8]byte{0x21, 0x09, 0x02, 0x00, 0x02, 0x00, 0x40, 0x00}
	data := bytes.Repeat([]byte{0x55}, 64)
	pkt := usbmonPacket(48, testURB{
		id: 7, event: 'S', xfer: TransferControl,
		dev: 3, bus: 1, setup: setup, ts: ts, data: data,
	})
	urbs, err := Read(bytes.NewReader(pcapngFile(linkTypeUSBLinux, 6, []time.Time{ts}, [][]byte{pkt})))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(urbs) != 1 {
		t.Fatalf("Read: %d URBs, want 1", len(urbs))
	}
	u := urbs[0]
	if u.Event != 'S' || u.Xfer != TransferControl || u.Dev != 3 || u.Bus != 1 {
		t.Errorf("URB header = %+v, want the captured usbmon_packet fields", u)
	}
	if u.Setup != setup {
		t.Errorf("URB setup = % X, want % X", u.Setup, setup)
	}
	if !u.Time.Equal(ts) {
		t.Errorf("URB time = %v, want %v (if_tsresol %d)", u.Time, ts, 6)
	}
	if !bytes.Equal(u.Data, data) {
		t.Errorf("URB data = % X, want the captured data stage", u.Data)
	}
}

func TestReadRefusesWhatItCannotDecode(t *testing.T) {
	ts := time.Unix(1_760_000_002, 0)
	good := usbmonPacket(64, testURB{id: 1, event: 'S', xfer: TransferInterrupt, ts: ts, data: []byte{0xAA}})
	for _, tc := range []struct {
		name string
		file []byte
		want string
	}{
		{"not a capture", []byte("GET / HTTP/1.1"), "not a capture"},
		{"foreign link type", pcapFile(1, []time.Time{ts}, [][]byte{good}), "link type"},
		{"truncated frame", pcapFile(linkTypeUSBLinuxMmaped, []time.Time{ts}, [][]byte{good[:20]}), "packet 1"},
		{"not a usbmon header", pcapFile(linkTypeUSBLinuxMmaped, []time.Time{ts}, [][]byte{bytes.Repeat([]byte{0xFF}, 64)}), "packet 1"},
	} {
		_, err := Read(bytes.NewReader(tc.file))
		if err == nil {
			t.Errorf("%s: Read accepted it, want a refusal", tc.name)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: Read error %q does not mention %q", tc.name, err, tc.want)
		}
	}
}

// A URB carries at most one HID report: the data stage of an OUT submit or of
// an IN complete. Everything else (echoed OUT completions, IN submits,
// failed URBs, bulk/iso, control transfers that are not SET_REPORT/
// GET_REPORT) carries no report at all — the importer accounts for those as
// traffic that is not report traffic, never as reports.
func TestURBReport(t *testing.T) {
	ts := time.Unix(1_760_000_003, 0)
	setReport := [8]byte{0x21, 0x09, 0x02, 0x00, 0x02, 0x00, 0x40, 0x00}
	getReport := [8]byte{0xA1, 0x01, 0x02, 0x00, 0x02, 0x00, 0x40, 0x00}
	getDescriptor := [8]byte{0x80, 0x06, 0x01, 0x00, 0x00, 0x00, 0x40, 0x00}
	data := []byte{0xAA, 0x12}

	for _, tc := range []struct {
		name string
		urb  URB
		ok   bool
		sent bool
		strm Stream
	}{
		{"INT OUT submit", URB{Event: 'S', Xfer: TransferInterrupt, EP: 0x02, Data: data}, true, true, Stream{Index: 0x02}},
		{"INT IN complete", URB{Event: 'C', Xfer: TransferInterrupt, EP: 0x82, Data: data}, true, false, Stream{Index: 0x82}},
		{"INT OUT complete echoes no report", URB{Event: 'C', Xfer: TransferInterrupt, EP: 0x02, Data: data}, false, false, Stream{}},
		{"INT IN submit carries no data", URB{Event: 'S', Xfer: TransferInterrupt, EP: 0x82, Data: data}, false, false, Stream{}},
		{"empty data stage", URB{Event: 'S', Xfer: TransferInterrupt, EP: 0x02}, false, false, Stream{}},
		{"failed URB", URB{Event: 'C', Xfer: TransferInterrupt, EP: 0x82, Status: -32, Data: data}, false, false, Stream{}},
		{"bulk is not HID", URB{Event: 'S', Xfer: TransferBulk, EP: 0x02, Data: data}, false, false, Stream{}},
		{"isochronous is not HID", URB{Event: 'C', Xfer: TransferISO, EP: 0x82, Data: data}, false, false, Stream{}},
		{"SET_REPORT control", URB{Event: 'S', Xfer: TransferControl, Setup: setReport, Data: data}, true, true, Stream{Control: true, Index: 2}},
		{"GET_REPORT control", URB{Event: 'C', Xfer: TransferControl, Setup: getReport, Data: data}, true, false, Stream{Control: true, Index: 2}},
		{"GET_DESCRIPTOR control is not report traffic", URB{Event: 'C', Xfer: TransferControl, Setup: getDescriptor, Data: data}, false, false, Stream{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.urb.Time = ts
			tc.urb.Data = cloneBytes(tc.urb.Data)
			rep, ok := tc.urb.Report()
			if ok != tc.ok {
				t.Fatalf("Report() ok = %v, want %v", ok, tc.ok)
			}
			if !ok {
				return
			}
			if rep.Sent != tc.sent {
				t.Errorf("Report().Sent = %v, want %v", rep.Sent, tc.sent)
			}
			if rep.Stream != tc.strm {
				t.Errorf("Report().Stream = %+v, want %+v", rep.Stream, tc.strm)
			}
			if !bytes.Equal(rep.Data, tc.urb.Data) {
				t.Errorf("Report().Data = % X, want % X", rep.Data, tc.urb.Data)
			}
			if !rep.Time.Equal(ts) || rep.Bus != tc.urb.Bus || rep.Dev != tc.urb.Dev {
				t.Errorf("Report() = %+v, want the URB's provenance", rep)
			}
		})
	}
}

func cloneBytes(b []byte) []byte {
	if b == nil {
		return nil
	}
	out := make([]byte, len(b))
	copy(out, b)
	return out
}
