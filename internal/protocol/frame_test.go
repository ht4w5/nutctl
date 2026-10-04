package protocol

import (
	"bytes"
	"testing"

	"github.com/ht4w5/nutctl/internal/fixture"
)

const fixturesDir = "../../testdata/captures"

func TestBuildRequestMatchesFixture(t *testing.T) {
	// The seed request reports are the golden wire bytes for a 48-byte read
	// over 32-byte reports (GET_DEVICE_INFO): 2 chunks of 24 payload bytes,
	// last-packet flag on the final chunk.
	x, err := fixture.Load(fixturesDir+"/get_device_info", "seed-32byte")
	if err != nil {
		t.Fatalf("load fixture: %v", err)
	}

	var got [][]byte
	for i := 0; i < 2; i++ {
		r := Request{
			Cmd:        CmdGetDeviceInfo,
			Length:     24,
			Addr:       uint16(i * 24),
			LastPacket: i == 1,
		}
		b, err := r.Marshal(32)
		if err != nil {
			t.Fatalf("chunk %d: %v", i, err)
		}
		got = append(got, b)
	}

	if len(got) != len(x.Requests) {
		t.Fatalf("built %d chunks, fixture has %d", len(got), len(x.Requests))
	}
	for i := range got {
		if !bytes.Equal(got[i], x.Requests[i]) {
			t.Errorf("chunk %d:\n got  %X\n want %X", i, got[i], x.Requests[i])
		}
	}
}

func TestBuildRequestLastChunkShortLength(t *testing.T) {
	// GET_GAME_MODE is 56 bytes: over 32-byte reports, chunk sizes are 24, 24
	// and 8.
	x, err := fixture.Load(fixturesDir+"/get_game_mode", "seed-32byte")
	if err != nil {
		t.Fatalf("load fixture: %v", err)
	}
	lengths := []uint8{24, 24, 8}
	for i, wantLen := range lengths {
		r := Request{
			Cmd:        CmdGetGameMode,
			Length:     wantLen,
			Addr:       uint16(i * 24),
			LastPacket: i == len(lengths)-1,
		}
		b, err := r.Marshal(32)
		if err != nil {
			t.Fatalf("chunk %d: %v", i, err)
		}
		if !bytes.Equal(b, x.Requests[i]) {
			t.Errorf("chunk %d:\n got  %X\n want %X", i, b, x.Requests[i])
		}
	}
}

func TestBuildRequestCustomHeader(t *testing.T) {
	// Single-Key-Slot read: the whole 8-byte header is pre-built and the
	// payload follows it (docs/protocol.md §2).
	r := Request{
		CustomHeader: []byte{0xAA, 0x12 /* GET_KEY */, 0x04, 0x08, 0x00, 0x00, 0x00, 0x00},
		Payload:      []byte{0xDE, 0xAD},
	}
	b, err := r.Marshal(32)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	want := []byte{0xAA, 18, 0x04, 0x08, 0x00, 0x00, 0x00, 0x00, 0xDE, 0xAD}
	if !bytes.Equal(b[:10], want) {
		t.Errorf("got %X, want prefix %X", b[:10], want)
	}
	for i := 10; i < len(b); i++ {
		if b[i] != 0 {
			t.Errorf("byte %d = %#x, want 0 (zero-filled report)", i, b[i])
		}
	}
}

func TestBuildRequestOtherHeaderSuppressesLastFlag(t *testing.T) {
	// When otherHeader supplies byte 6, the last-packet flag must not
	// overwrite it.
	r := Request{Cmd: CmdGetGameMode, Length: 24, LastPacket: true, OtherHeader: []byte{0x01, 0x00}}
	b, err := r.Marshal(32)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if b[5] != 0x01 || b[6] != 0x00 {
		t.Errorf("bytes 5..6 = %#x %#x, want 0x01 0x00", b[5], b[6])
	}
}

func TestParseResponseParsesFixture(t *testing.T) {
	x, err := fixture.Load(fixturesDir+"/get_device_info", "seed-32byte")
	if err != nil {
		t.Fatalf("load fixture: %v", err)
	}
	for i, raw := range x.Responses {
		resp, err := ParseResponse(raw)
		if err != nil {
			t.Fatalf("chunk %d: %v", i, err)
		}
		if resp.Cmd != CmdGetDeviceInfo {
			t.Errorf("chunk %d: cmd = %#x, want %#x", i, resp.Cmd, CmdGetDeviceInfo)
		}
		if want := uint16(i * 24); resp.Addr != want {
			t.Errorf("chunk %d: addr = %d, want %d", i, resp.Addr, want)
		}
		if len(resp.Data) != 24 {
			t.Errorf("chunk %d: data length %d, want 24", i, len(resp.Data))
		}
	}
}

func TestParseResponseRejectsGarbage(t *testing.T) {
	if _, err := ParseResponse([]byte{0xAA, 1, 2, 3, 4, 5, 6, 7}); err == nil {
		t.Error("garbage magic accepted, want error")
	}
	if _, err := ParseResponse([]byte{0x55, 1, 2}); err == nil {
		t.Error("short report accepted, want error")
	}
}

// The nut87 fixtures are recorded from real hardware (64-byte reports, one
// chunk per transfer). They are the evidence that our framing matches the
// Device byte for byte.

func TestBuildRequestMatchesRealCapture(t *testing.T) {
	x, err := fixture.Load(fixturesDir+"/get_device_info", "nut87")
	if err != nil {
		t.Fatalf("load fixture: %v", err)
	}
	if len(x.Requests) != 1 {
		t.Fatalf("fixture has %d request reports, want 1 (64-byte reports fit the 48-byte payload in one chunk)", len(x.Requests))
	}
	r := Request{Cmd: CmdGetDeviceInfo, Length: 48, Addr: 0, LastPacket: true}
	b, err := r.Marshal(64)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !bytes.Equal(b, x.Requests[0]) {
		t.Errorf("request:\n got  %X\n want %X", b, x.Requests[0])
	}
}

func TestParseResponseParsesRealCapture(t *testing.T) {
	x, err := fixture.Load(fixturesDir+"/get_device_info", "nut87")
	if err != nil {
		t.Fatalf("load fixture: %v", err)
	}
	resp, err := ParseResponse(x.Responses[0])
	if err != nil {
		t.Fatalf("ParseResponse: %v", err)
	}
	if resp.Cmd != CmdGetDeviceInfo {
		t.Errorf("cmd = %#x, want %#x", resp.Cmd, CmdGetDeviceInfo)
	}
	// Observed on hardware: lenOrType is the payload length and addr is the
	// transfer offset (docs/protocol.md §2).
	if resp.LenOrType != 48 {
		t.Errorf("lenOrType = %d, want 48", resp.LenOrType)
	}
	if resp.Addr != 0 {
		t.Errorf("addr = %d, want 0", resp.Addr)
	}
	if len(resp.Data) != 56 {
		t.Errorf("data length = %d, want 56 (64-byte report minus header)", len(resp.Data))
	}
}
