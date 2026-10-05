// Package protocol implements the NUT87 wire protocol: framing, chunked
// transfers with request/response matching and retries, and the payload
// codecs. Everything ugly about the wire lives here (docs/protocol.md is the
// contract); callers use typed Device operations and never see bytes.
package protocol

import "fmt"

// Frame constants (docs/protocol.md §2).
const (
	RequestMagic  byte = 0xAA
	ResponseMagic byte = 0x55
	HeaderSize         = 8 // request header: magic, cmd, len, addr(2), other(3)
)

// Command ids used by the v0 surface (docs/protocol.md §3).
const (
	CmdGetDeviceInfo    byte = 16
	CmdGetGameMode      byte = 17
	CmdGetKey           byte = 18
	CmdGetLEDEffect     byte = 19
	CmdGetCustomLEDData byte = 20
	CmdGetFnKey         byte = 22

	// The write commands of the v0 surface (docs/protocol.md §3). Each SET
	// writes one complete block in one batched transfer (docs/protocol.md §4);
	// SET_FACTORY_RESET is the exception — one fire-and-forget report (reset.go).
	CmdSetFactoryReset  byte = 15
	CmdSetGameMode      byte = 33
	CmdSetKey           byte = 34
	CmdSetLEDEffect     byte = 35
	CmdSetCustomLEDData byte = 36
	CmdSetFnKey         byte = 38
)

// Payload sizes of the v0 commands (docs/protocol.md §4).
const (
	DeviceInfoSize = 48
	SettingsSize   = 56
	KeymapSize     = 512 // 128 Key Slots × 4 bytes
	LEDEffectSize  = 16
	PerKeyRGBSize  = 512 // 128 entries × 4 bytes
)

// Request is one request chunk: the contents of a single output report.
type Request struct {
	Cmd    uint8
	Length uint8  // payload bytes in THIS chunk (expected response data size)
	Addr   uint16 // byte offset of this chunk in the transfer

	Payload []byte // chunk payload, at offset 8 (or len(CustomHeader))

	// OtherHeader occupies request bytes 5..7. If it has fewer than 2 entries,
	// byte 6 carries the last-packet flag.
	OtherHeader []byte
	// CustomHeader is a pre-built header (e.g. single-Key-Slot reads); it
	// replaces the fields above and the payload starts right after it.
	CustomHeader []byte
	// LastPacket marks the final chunk of a transfer (byte 6 = 1).
	LastPacket bool
}

// Marshal encodes the request chunk into one output report of reportLen bytes.
func (r Request) Marshal(reportLen int) ([]byte, error) {
	if reportLen < HeaderSize {
		return nil, fmt.Errorf("report length %d is smaller than the %d-byte header", reportLen, HeaderSize)
	}
	buf := make([]byte, reportLen) // zero-filled, like the vendor encoder
	if len(r.CustomHeader) > 0 {
		if len(r.CustomHeader)+len(r.Payload) > reportLen {
			return nil, fmt.Errorf("custom header (%d) + payload (%d) exceeds report length %d",
				len(r.CustomHeader), len(r.Payload), reportLen)
		}
		copy(buf, r.CustomHeader)
		copy(buf[len(r.CustomHeader):], r.Payload)
		return buf, nil
	}
	buf[0] = RequestMagic
	buf[1] = r.Cmd
	buf[2] = r.Length
	buf[3] = byte(r.Addr)
	buf[4] = byte(r.Addr >> 8)
	for i := 0; i < 3 && i < len(r.OtherHeader); i++ {
		buf[5+i] = r.OtherHeader[i]
	}
	if len(r.OtherHeader) < 2 && r.LastPacket {
		buf[6] = 1
	}
	if len(r.Payload) > reportLen-HeaderSize {
		return nil, fmt.Errorf("payload (%d) exceeds report payload capacity %d", len(r.Payload), reportLen-HeaderSize)
	}
	copy(buf[HeaderSize:], r.Payload)
	return buf, nil
}

// Response is one response chunk: the contents of a single input report.
type Response struct {
	Cmd       uint8
	LenOrType uint8
	Addr      uint16
	Data      []byte // report bytes from offset 8 on
}

// ParseResponse decodes one input report. Reports with a bad magic byte
// (garbage on the wire) are rejected here; the transfer layer treats them as
// "no response" and retries.
func ParseResponse(report []byte) (Response, error) {
	if len(report) < HeaderSize {
		return Response{}, fmt.Errorf("short response report (%d bytes)", len(report))
	}
	if report[0] != ResponseMagic {
		return Response{}, fmt.Errorf("bad response magic %#x (want %#x)", report[0], ResponseMagic)
	}
	return Response{
		Cmd:       report[1],
		LenOrType: report[2],
		Addr:      uint16(report[3]) | uint16(report[4])<<8,
		Data:      report[HeaderSize:],
	}, nil
}
