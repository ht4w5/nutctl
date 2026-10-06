package protocol

import (
	"bytes"
	"reflect"
	"testing"
)

// SplitTransfers groups a wire log into the per-transfer exchanges the
// fixture corpus stores (ticket 09: `nutctl fixtures record` turns a live
// session into fixtures; ticket 10's pcap importer will group captured URBs
// the same way). It is the transfer layer's matching, replayed backwards over
// a recording: requests chunk a transfer (docs/protocol.md §2), responses
// answer by cmd, and anything unsolicited stands alone. These tests pin that
// grouping — a misparse here corrupts every recorded fixture.

func TestParseRequestIsTheInverseOfMarshal(t *testing.T) {
	for _, r := range []Request{
		{Cmd: CmdGetKey, Length: 8, Addr: 504, Payload: bytes.Repeat([]byte{0x11}, 8), LastPacket: true},
		{Cmd: CmdSetGameMode, Length: 56, Payload: bytes.Repeat([]byte{0xAB}, 56)},
		{Cmd: CmdGetDeviceInfo, Length: 48, Payload: make([]byte, 48), LastPacket: true},
	} {
		report, err := r.Marshal(64)
		if err != nil {
			t.Fatalf("Marshal(%+v): %v", r, err)
		}
		got, err := ParseRequest(report)
		if err != nil {
			t.Fatalf("ParseRequest(% X): %v", report, err)
		}
		if got.Cmd != r.Cmd || got.Length != r.Length || got.Addr != r.Addr || got.LastPacket != r.LastPacket {
			t.Errorf("ParseRequest(Marshal(%+v)) = %+v", r, got)
		}
		if !bytes.Equal(got.Payload, r.Payload) {
			t.Errorf("ParseRequest payload = % X, want % X", got.Payload, r.Payload)
		}
	}
}

func TestParseRequestRefusesNonRequests(t *testing.T) {
	for _, report := range [][]byte{
		{0x55, 0x12, 0x38, 0x00, 0x00, 0x00, 0x00, 0x00}, // a response
		{0xAA, 0x12}, // short
		{},           // empty
	} {
		if _, err := ParseRequest(report); err == nil {
			t.Errorf("ParseRequest(% X) = nil error, want a refusal", report)
		}
	}
}

func TestSplitTransfersSingleChunkRead(t *testing.T) {
	req := reqReport(t, CmdGetDeviceInfo, 48, 0, false)
	res := resReport(CmdGetDeviceInfo, 48, 0)
	got, err := SplitTransfers([]WireReport{{Sent: true, Report: req}, {Report: res}})
	if err != nil {
		t.Fatalf("SplitTransfers: %v", err)
	}
	want := []WireTransfer{{Cmd: CmdGetDeviceInfo, Requests: [][]byte{req}, Responses: [][]byte{res}}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("SplitTransfers = %+v, want %+v", got, want)
	}
}

func TestSplitTransfersMultiChunkRead(t *testing.T) {
	var log []WireReport
	for i := 0; i < 10; i++ {
		log = append(log, WireReport{Sent: true, Report: reqReport(t, CmdGetKey, 56, uint16(i*56), i == 9)})
		log = append(log, WireReport{Report: resReport(CmdGetKey, 56, uint16(i*56))})
	}
	got, err := SplitTransfers(log)
	if err != nil {
		t.Fatalf("SplitTransfers: %v", err)
	}
	if len(got) != 1 || got[0].Cmd != CmdGetKey || len(got[0].Requests) != 10 || len(got[0].Responses) != 10 {
		t.Fatalf("SplitTransfers: %d transfers, want one 10-chunk GET_KEY transfer: %+v", len(got), got)
	}
}

// Two transfers of the same command back to back (a read and its read-back,
// as `load` records them) are two exchanges: the last-packet flag closes the
// first one.
func TestSplitTransfersSplitsSameCommandTransfers(t *testing.T) {
	var log []WireReport
	for _, last := range []bool{false, true, false, true} {
		log = append(log,
			WireReport{Sent: true, Report: reqReport(t, CmdGetLEDEffect, 16, 0, last)},
			WireReport{Report: resReport(CmdGetLEDEffect, 16, 0)})
	}
	got, err := SplitTransfers(log)
	if err != nil {
		t.Fatalf("SplitTransfers: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("SplitTransfers: %d transfers, want 2: %+v", len(got), got)
	}
}

// A transfer that ends without the last-packet flag (SET_FACTORY_RESET is one
// fire-and-forget report) still stands alone when another command follows.
func TestSplitTransfersFireAndForget(t *testing.T) {
	reset := reqReport(t, CmdSetFactoryReset, 1, 0, false)
	req := reqReport(t, CmdGetGameMode, 56, 0, true)
	res := resReport(CmdGetGameMode, 56, 0)
	got, err := SplitTransfers([]WireReport{
		{Sent: true, Report: reset},
		{Sent: true, Report: req},
		{Report: res},
	})
	if err != nil {
		t.Fatalf("SplitTransfers: %v", err)
	}
	want := []WireTransfer{
		{Cmd: CmdSetFactoryReset, Requests: [][]byte{reset}},
		{Cmd: CmdGetGameMode, Requests: [][]byte{req}, Responses: [][]byte{res}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("SplitTransfers = %+v, want %+v", got, want)
	}
}

// Unsolicited input reports (device notify traffic — `nutctl watch`) stand
// alone as exchanges of their own; a run of consecutive ones is one exchange.
func TestSplitTransfersUnsolicitedRun(t *testing.T) {
	notify1 := resReport(CmdGet24GDisconnectNotify, 4, 0)
	notify2 := resReport(CmdGetDeviceNotify, 2, 0)
	got, err := SplitTransfers([]WireReport{{Report: notify1}, {Report: notify2}})
	if err != nil {
		t.Fatalf("SplitTransfers: %v", err)
	}
	want := []WireTransfer{{Cmd: CmdGet24GDisconnectNotify, Responses: [][]byte{notify1, notify2}}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("SplitTransfers = %+v, want %+v", got, want)
	}
}

// Notify traffic arriving in the middle of a transfer does not pollute the
// transfer's responses: pairing is by cmd (docs/protocol.md §2), never by
// position.
func TestSplitTransfersNotifyDoesNotPolluteResponses(t *testing.T) {
	notify := resReport(CmdGetDeviceNotify, 2, 0)
	log := []WireReport{{Sent: true, Report: reqReport(t, CmdGetLEDEffect, 16, 0, true)}}
	log = append(log, WireReport{Report: notify})
	log = append(log, WireReport{Report: resReport(CmdGetLEDEffect, 16, 0)})
	got, err := SplitTransfers(log)
	if err != nil {
		t.Fatalf("SplitTransfers: %v", err)
	}
	want := []WireTransfer{
		{Cmd: CmdGetLEDEffect, Requests: [][]byte{log[0].Report}, Responses: [][]byte{log[2].Report}},
		{Cmd: CmdGetDeviceNotify, Responses: [][]byte{notify}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("SplitTransfers = %+v, want %+v", got, want)
	}
}

// A late response to an earlier transfer is matched back to it by cmd, not
// bolted onto whatever is open.
func TestSplitTransfersLateResponseMatchesItsTransfer(t *testing.T) {
	late := resReport(CmdGetKey, 56, 0)
	log := []WireReport{
		{Sent: true, Report: reqReport(t, CmdGetKey, 56, 0, true)},
		{Sent: true, Report: reqReport(t, CmdGetGameMode, 56, 0, true)},
		{Report: resReport(CmdGetGameMode, 56, 0)},
		{Report: late},
	}
	got, err := SplitTransfers(log)
	if err != nil {
		t.Fatalf("SplitTransfers: %v", err)
	}
	if len(got) != 2 || !reflect.DeepEqual(got[0].Responses, [][]byte{late}) || len(got[1].Responses) != 1 {
		t.Fatalf("SplitTransfers = %+v, want the late GET_KEY response back with GET_KEY", got)
	}
}

// An input report that is not a framed response at all (the 2.4G wake report
// `A6 FF 01`, or garbage) is recorded as observed, never silently dropped —
// with no command name of its own (Cmd 0).
func TestSplitTransfersUnparseableInputStandsAlone(t *testing.T) {
	wake := []byte{0xA6, 0xFF, 0x01}
	got, err := SplitTransfers([]WireReport{{Report: wake}})
	if err != nil {
		t.Fatalf("SplitTransfers: %v", err)
	}
	want := []WireTransfer{{Cmd: 0, Responses: [][]byte{wake}}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("SplitTransfers = %+v, want %+v", got, want)
	}
}

// A request report that does not parse is a bug report, not a fixture: the
// recorder must fail loudly rather than commit a corpus entry that does not
// follow the framing.
func TestSplitTransfersRefusesUnparseableRequests(t *testing.T) {
	if _, err := SplitTransfers([]WireReport{{Sent: true, Report: []byte{0x00, 0x01}}}); err == nil {
		t.Fatal("SplitTransfers accepted an unparseable request report, want an error")
	}
}

// CommandName names every command the v0 read and write paths put on the
// wire — the names meta.json records and the corpus directories derive from.
func TestCommandNamesCoverTheV0Surface(t *testing.T) {
	for _, tc := range []struct {
		cmd  byte
		name string
	}{
		{CmdGetDeviceInfo, "GET_DEVICE_INFO"},
		{CmdGetGameMode, "GET_GAME_MODE"},
		{CmdGetKey, "GET_KEY"},
		{CmdGetFnKey, "GET_FN_KEY"},
		{CmdGetLEDEffect, "GET_LED_EFFECT"},
		{CmdGetCustomLEDData, "GET_CUSTOM_LED_DATA"},
		{CmdSetKey, "SET_KEY"},
		{CmdSetFnKey, "SET_FN_KEY"},
		{CmdSetLEDEffect, "SET_LED_EFFECT"},
		{CmdSetCustomLEDData, "SET_CUSTOM_LED_DATA"},
		{CmdSetGameMode, "SET_GAME_MODE"},
		{CmdSetFactoryReset, "SET_FACTORY_RESET"},
		{CmdGetDeviceNotify, "GET_DEVICE_NOTIFY"},
		{CmdGet24GDisconnectNotify, "GET_24G_DISCONNECT_NOTIFY"},
	} {
		if got := CommandName(tc.cmd); got != tc.name {
			t.Errorf("CommandName(%d) = %q, want %q", tc.cmd, got, tc.name)
		}
	}
	if got := CommandName(0); got != "UNKNOWN_0" {
		t.Errorf("CommandName(0) = %q, want an explicit unknown", got)
	}
}

// reqReport builds one request chunk report the way the transfer engine does
// (Request.Marshal, 64-byte reports).
func reqReport(t *testing.T, cmd byte, length int, addr uint16, last bool) []byte {
	t.Helper()
	r, err := (Request{Cmd: cmd, Length: uint8(length), Addr: addr, LastPacket: last}).Marshal(64)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	return r
}

// resReport builds one response chunk report with the framing the Device
// uses (docs/protocol.md §2: cmd, lenOrType, addr mirroring the request).
func resReport(cmd byte, length int, addr uint16) []byte {
	r := make([]byte, 64)
	r[0] = ResponseMagic
	r[1] = cmd
	r[2] = byte(length)
	r[3] = byte(addr)
	r[4] = byte(addr >> 8)
	return r
}
