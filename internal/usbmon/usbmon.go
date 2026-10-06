// Package usbmon decodes kernel USB captures (docs/capture.md Method B): the
// pcap and pcapng files of Linux usbmon traffic that tshark or Wireshark
// write while the vendor app runs. One URB is one USB transfer event; what
// the protocol cares about is the HID report payload a URB carries (Report),
// which is what `nutctl fixtures import-pcap` turns into fixtures.
//
// This package is pure format decoding: it knows pcap, pcapng and the
// usbmon_packet header, and nothing about the NUT87 wire protocol. Anything
// it cannot decode is an error naming the packet — a capture read must never
// guess (guiding constraint 1 of PLAN.md).
package usbmon

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"time"
)

// Link types of Linux usbmon captures (tcpdump.org linktypes).
const (
	linkTypeUSBLinux       = 189 // usbmon_packet, 48-byte header
	linkTypeUSBLinuxMmaped = 220 // usbmon_packet, 64-byte header
)

// usbmon_packet header lengths per link type: the mmap'd variant adds
// interval/start_frame/xfer_flags/ndesc (Documentation/usb/usbmon.rst).
const (
	hdrLenUSBLinux       = 48
	hdrLenUSBLinuxMmaped = 64
)

// URB transfer types (usbmon_packet.xfer_type).
const (
	TransferISO       = 0
	TransferInterrupt = 1
	TransferControl   = 2
	TransferBulk      = 3
)

// URB event types (usbmon_packet.type).
const (
	EventSubmit   = 'S' // host → device submission
	EventComplete = 'C' // completion
	EventError    = 'E' // submission failed in the HCD
)

// URB is one captured USB transfer event (usbmon_packet). Setup is the 8-byte
// setup packet of a control transfer (valid when Xfer is TransferControl);
// Data is the captured data stage (len_cap bytes).
type URB struct {
	Time   time.Time
	Bus    uint16
	Dev    uint8
	Event  byte  // EventSubmit, EventComplete, EventError
	Xfer   uint8 // TransferISO … TransferBulk
	EP     uint8 // endpoint address (number + direction bit 0x80)
	Status int32
	Setup  [8]byte
	Data   []byte
}

// Stream is where reports appear on a wire: one endpoint for interrupt
// transfers, one interface for control report transfers (SET_REPORT and
// GET_REPORT ride endpoint 0 and are addressed per interface).
type Stream struct {
	Control bool
	Index   uint16 // endpoint address, or interface number for control
}

// String renders a stream the way a capture report names it.
func (s Stream) String() string {
	if s.Control {
		return fmt.Sprintf("control interface %d", s.Index)
	}
	return fmt.Sprintf("endpoint %#x", s.Index)
}

// Report is one HID report as a URB carried it: the data stage of an OUT
// submit (Sent) or of an IN complete (received). A URB carries at most one
// report — the HID transport moves one report per transfer.
type Report struct {
	Time   time.Time
	Bus    uint16
	Dev    uint8
	Stream Stream
	Sent   bool // true: host → device
	Data   []byte
}

// Report extracts the HID report a URB carries. Interrupt transfers carry
// reports on their data stage; control transfers only when they are the HID
// report plumbing (SET_REPORT 0x21/0x09, GET_REPORT 0xA1/0x01) — a
// GET_DESCRIPTOR data stage is traffic, but not report traffic. Failed URBs,
// bulk/isochronous transfers and the echoed halves of a transfer's data
// stage carry no report.
func (u URB) Report() (Report, bool) {
	if len(u.Data) == 0 {
		return Report{}, false
	}
	if u.Event == EventComplete && u.Status != 0 {
		return Report{}, false
	}
	var sent bool
	var stream Stream
	switch u.Xfer {
	case TransferInterrupt:
		stream = Stream{Index: uint16(u.EP)}
		sent = u.EP&0x80 == 0
		// OUT data is submitted, IN data is completed; the other event of
		// the transfer carries no report of its own.
		if sent != (u.Event == EventSubmit) {
			return Report{}, false
		}
	case TransferControl:
		stream = Stream{Control: true, Index: binary.LittleEndian.Uint16(u.Setup[4:6])}
		switch {
		case u.Setup[0] == 0x21 && u.Setup[1] == 0x09 && u.Event == EventSubmit:
			sent = true
		case u.Setup[0] == 0xA1 && u.Setup[1] == 0x01 && u.Event == EventComplete:
			sent = false
		default:
			return Report{}, false
		}
	default:
		return Report{}, false
	}
	return Report{Time: u.Time, Bus: u.Bus, Dev: u.Dev, Stream: stream, Sent: sent, Data: u.Data}, true
}

// ReadFile reads every URB of one capture file (pcap or pcapng of Linux
// usbmon traffic), in capture order.
func ReadFile(path string) ([]URB, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	urbs, err := Read(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return urbs, nil
}

// Read reads every URB of a capture (pcap or pcapng of Linux usbmon
// traffic), in capture order. The container is detected by its magic; a
// capture it cannot decode is an error naming the packet, never a best
// effort.
func Read(r io.Reader) ([]URB, error) {
	raw, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	switch {
	case isPcapMagic(raw):
		return readPcap(raw)
	case bytes.HasPrefix(raw, []byte{0x0A, 0x0D, 0x0D, 0x0A}):
		return readPcapng(raw)
	default:
		return nil, fmt.Errorf("not a capture (pcap or pcapng): unknown file magic")
	}
}

func isPcapMagic(raw []byte) bool {
	for _, m := range [][]byte{
		{0xd4, 0xc3, 0xb2, 0xa1}, // little-endian, microseconds
		{0xa1, 0xb2, 0xc3, 0xd4}, // big-endian, microseconds
		{0x4d, 0x3c, 0xb2, 0xa1}, // little-endian, nanoseconds
		{0xa1, 0xb2, 0x3c, 0x4d}, // big-endian, nanoseconds
	} {
		if bytes.HasPrefix(raw, m) {
			return true
		}
	}
	return false
}

// readPcap decodes the classic libpcap container: one global header, then
// per-packet headers of timestamp + lengths.
func readPcap(raw []byte) ([]URB, error) {
	if len(raw) < 24 {
		return nil, fmt.Errorf("not a capture (pcap or pcapng): truncated pcap header")
	}
	le := raw[0] == 0xd4 || raw[0] == 0x4d
	subsec := time.Microsecond
	if raw[0] == 0x4d || raw[0] == 0xa1 && raw[3] == 0x4d {
		subsec = time.Nanosecond
	}
	var bo binary.ByteOrder = binary.BigEndian
	if le {
		bo = binary.LittleEndian
	}
	linktype := bo.Uint32(raw[20:24]) & 0xFFFF
	var urbs []URB
	for off, n := 24, 0; off < len(raw); n++ {
		if off+16 > len(raw) {
			return nil, fmt.Errorf("packet %d: truncated pcap packet header", n+1)
		}
		sec := bo.Uint32(raw[off:])
		frac := bo.Uint32(raw[off+4:])
		incl := bo.Uint32(raw[off+8:])
		off += 16
		if off+int(incl) > len(raw) {
			return nil, fmt.Errorf("packet %d: truncated pcap packet (%d bytes claimed)", n+1, incl)
		}
		ts := time.Unix(int64(sec), int64(frac)*int64(subsec/time.Nanosecond))
		u, err := decodeURB(uint16(linktype), raw[off:off+int(incl)], ts)
		if err != nil {
			return nil, fmt.Errorf("packet %d: %w", n+1, err)
		}
		urbs = append(urbs, u)
		off += int(incl)
	}
	return urbs, nil
}

// pcapng block types and option codes.
const (
	blockSHB = 0x0A0D0D0A
	blockIDB = 1
	blockPB  = 2 // obsolete packet block
	blockSPB = 3
	blockEPB = 6

	optEnd       = 0
	optIfTsresol = 9
)

// readPcapng decodes the pcapng container (what tshark -w writes): sections
// of interface descriptions plus packet blocks. Timestamps come in the
// interface's resolution (if_tsresol, default microseconds).
func readPcapng(raw []byte) ([]URB, error) {
	var (
		urbs   []URB
		le     = true
		ifaces []iface
	)
	for off, n := 0, 0; off+12 <= len(raw); n++ {
		typBytes := raw[off : off+4]
		isSHB := bytes.Equal(typBytes, []byte{0x0A, 0x0D, 0x0D, 0x0A})
		if n == 0 && !isSHB {
			return nil, fmt.Errorf("not a capture (pcap or pcapng): pcapng without a section header")
		}
		if isSHB {
			// The byte-order magic at offset 8 fixes the endianness of the
			// whole section (0x1A2B3C4D in the section's own order).
			switch {
			case bytes.Equal(raw[off+8:off+12], []byte{0x4D, 0x3C, 0x2B, 0x1A}):
				le = true
			case bytes.Equal(raw[off+8:off+12], []byte{0x1A, 0x2B, 0x3C, 0x4D}):
				le = false
			default:
				return nil, fmt.Errorf("packet %d: pcapng section header with unknown byte-order magic", n+1)
			}
			ifaces = nil // a new section starts its own interface numbering
		}
		var bo binary.ByteOrder = binary.BigEndian
		if le {
			bo = binary.LittleEndian
		}
		total := int(bo.Uint32(raw[off+4 : off+8]))
		if total < 12 || total%4 != 0 || off+total > len(raw) {
			return nil, fmt.Errorf("packet %d: bad pcapng block length %d", n+1, total)
		}
		if got := bo.Uint32(raw[off+total-4 : off+total]); got != uint32(total) {
			return nil, fmt.Errorf("packet %d: pcapng block length mismatch (%d … %d)", n+1, total, got)
		}
		body := raw[off+8 : off+total-4]
		switch {
		case isSHB:
			// section header: nothing else needed
		case bo.Uint32(typBytes) == blockIDB:
			f, err := decodeIDB(bo, body, n+1)
			if err != nil {
				return nil, err
			}
			ifaces = append(ifaces, f)
		case bo.Uint32(typBytes) == blockEPB || bo.Uint32(typBytes) == blockPB:
			if len(body) < 4 {
				return nil, fmt.Errorf("packet %d: truncated pcapng packet block", n+1)
			}
			// The obsolete packet block carries the interface id as u16
			// (with a u16 drop counter beside it); the fields after it are
			// the enhanced packet block's, byte for byte.
			id := int(bo.Uint32(body[:4]))
			if bo.Uint32(typBytes) == blockPB {
				id = int(bo.Uint16(body[:2]))
			}
			u, err := decodePacketBlock(bo, body, id, ifaces, n+1)
			if err != nil {
				return nil, err
			}
			urbs = append(urbs, u)
		case bo.Uint32(typBytes) == blockSPB:
			if len(ifaces) == 0 {
				return nil, fmt.Errorf("packet %d: pcapng packet block before any interface", n+1)
			}
			f := ifaces[0]
			orig := int(bo.Uint32(body[:4]))
			data := body[4:]
			if orig < len(data) {
				data = data[:orig]
			}
			u, err := decodeURB(f.linktype, data, time.Time{})
			if err != nil {
				return nil, fmt.Errorf("packet %d: %w", n+1, err)
			}
			urbs = append(urbs, u)
		}
		off += total
	}
	return urbs, nil
}

// iface is one pcapng Interface Description Block: its link type and
// timestamp resolution.
type iface struct {
	linktype    uint16
	unitsPerSec uint64
}

func decodeIDB(bo binary.ByteOrder, body []byte, n int) (iface, error) {
	if len(body) < 8 {
		return iface{}, fmt.Errorf("packet %d: truncated pcapng interface description", n)
	}
	f := iface{linktype: bo.Uint16(body[:2]), unitsPerSec: 1_000_000}
	for off := 8; off+4 <= len(body); {
		code := bo.Uint16(body[off:])
		length := int(bo.Uint16(body[off+2:]))
		if code == optEnd {
			break
		}
		if off+4+length > len(body) {
			break
		}
		val := body[off+4 : off+4+length]
		if code == optIfTsresol && length == 1 {
			if val[0]&0x80 != 0 {
				f.unitsPerSec = 1 << (val[0] & 0x7F)
			} else {
				var p uint64 = 1
				for i := byte(0); i < val[0]; i++ {
					p *= 10
				}
				f.unitsPerSec = p
			}
		}
		off += 4 + (length+3)&^3
	}
	return f, nil
}

// decodePacketBlock decodes one packet block body (interface id, timestamp,
// captured/original lengths, data), already reduced to its interface id.
func decodePacketBlock(bo binary.ByteOrder, body []byte, id int, ifaces []iface, n int) (URB, error) {
	if len(body) < 20 {
		return URB{}, fmt.Errorf("packet %d: truncated pcapng packet block", n)
	}
	if id >= len(ifaces) {
		return URB{}, fmt.Errorf("packet %d: pcapng packet on unknown interface %d", n, id)
	}
	units := uint64(bo.Uint32(body[4:8]))<<32 | uint64(bo.Uint32(body[8:12]))
	caplen := int(bo.Uint32(body[12:16]))
	if 20+caplen > len(body) {
		return URB{}, fmt.Errorf("packet %d: truncated pcapng packet (%d bytes claimed)", n, caplen)
	}
	f := ifaces[id]
	ts := time.Unix(int64(units/f.unitsPerSec), int64(float64(units%f.unitsPerSec)*1e9/float64(f.unitsPerSec)))
	return decodeURB(f.linktype, body[20:20+caplen], ts)
}

// decodeURB decodes one usbmon packet: the usbmon_packet header (48 or 64
// bytes per link type) followed by the data stage.
func decodeURB(linktype uint16, pkt []byte, ts time.Time) (URB, error) {
	var hdrLen int
	switch linktype {
	case linkTypeUSBLinux:
		hdrLen = hdrLenUSBLinux
	case linkTypeUSBLinuxMmaped:
		hdrLen = hdrLenUSBLinuxMmaped
	default:
		return URB{}, fmt.Errorf("link type %d is not Linux usbmon — capture usbmon traffic (docs/capture.md Method B): %d is USB Linux, %d is USB Linux mmap'd",
			linktype, linkTypeUSBLinux, linkTypeUSBLinuxMmaped)
	}
	if len(pkt) < hdrLen {
		return URB{}, fmt.Errorf("truncated usbmon header (%d bytes, want %d)", len(pkt), hdrLen)
	}
	event := pkt[8]
	xfer := pkt[9]
	if event != EventSubmit && event != EventComplete && event != EventError {
		return URB{}, fmt.Errorf("not a Linux usbmon frame: event byte %#x", event)
	}
	if xfer > TransferBulk {
		return URB{}, fmt.Errorf("not a Linux usbmon frame: transfer type %d", xfer)
	}
	lenCap := int(binary.LittleEndian.Uint32(pkt[36:40]))
	if len(pkt) < hdrLen+lenCap {
		return URB{}, fmt.Errorf("truncated usbmon data stage (%d bytes claimed, %d present)", lenCap, len(pkt)-hdrLen)
	}
	var setup [8]byte
	copy(setup[:], pkt[40:48])
	return URB{
		Time:   ts,
		Bus:    binary.LittleEndian.Uint16(pkt[12:14]),
		Dev:    pkt[11],
		Event:  event,
		Xfer:   xfer,
		EP:     pkt[10],
		Status: int32(binary.LittleEndian.Uint32(pkt[28:32])),
		Setup:  setup,
		Data:   pkt[hdrLen : hdrLen+lenCap],
	}, nil
}
