package protocol

import (
	"bytes"
	"strings"
	"testing"
)

// ValidateTransfer is the framing contract both callers share: `nutctl
// fixtures import-pcap` imports only exchanges that validate (docs/capture.md
// Method B), and corpus_test.go keeps the committed corpus to the same rule —
// one definition of "follows the §2 framing", not two copies that can drift.

func TestValidateTransferAcceptsRecordedExchanges(t *testing.T) {
	// A chunked read: requests and responses mirroring each other.
	var log []WireReport
	for i := 0; i < 3; i++ {
		log = append(log, WireReport{Sent: true, Report: reqReport(t, CmdGetKey, 56, uint16(i*56), i == 2)})
		log = append(log, WireReport{Report: resReport(CmdGetKey, 56, uint16(i*56))})
	}
	// A SET with its echoing acks (docs/protocol.md §2).
	setReq := setChunk(t, 0xAB)
	setRes := resReport(CmdSetLEDEffect, 16, 0)
	copy(setRes[8:], setReq[8:])
	log = append(log, WireReport{Sent: true, Report: setReq}, WireReport{Report: setRes})
	// Unsolicited input (device notify traffic), mixed cmds in one run.
	notify := resReport(CmdGetDeviceNotify, 2, 0)
	disconnect := resReport(CmdGet24GDisconnectNotify, 4, 0)
	log = append(log, WireReport{Report: notify}, WireReport{Report: disconnect})

	transfers, err := SplitTransfers(log)
	if err != nil {
		t.Fatalf("SplitTransfers: %v", err)
	}
	for _, tr := range transfers {
		if err := ValidateTransfer(tr); err != nil {
			t.Errorf("ValidateTransfer(%s) = %v, want nil", TransferName(tr), err)
		}
	}
}

func TestValidateTransferRejectsBrokenFraming(t *testing.T) {
	good := setChunk(t, 0xAB)
	for _, tc := range []struct {
		name string
		tr   WireTransfer
		want string
	}{
		{
			name: "unparseable request",
			tr:   WireTransfer{Cmd: CmdGetKey, Requests: [][]byte{{0x00, 0x01}}},
			want: "request report 0",
		},
		{
			name: "request of another command",
			tr: WireTransfer{Cmd: CmdGetKey,
				Requests: [][]byte{reqReport(t, CmdGetFnKey, 56, 0, true)}},
			want: "carries cmd",
		},
		{
			name: "addr walks backwards",
			tr: WireTransfer{Cmd: CmdGetKey, Requests: [][]byte{
				reqReport(t, CmdGetKey, 56, 56, true),
				reqReport(t, CmdGetKey, 56, 0, true),
			}},
			want: "backwards",
		},
		{
			name: "unparseable response",
			tr: WireTransfer{Cmd: CmdGetKey,
				Requests:  [][]byte{reqReport(t, CmdGetKey, 56, 0, true)},
				Responses: [][]byte{{0xAA, 0x00}}},
			want: "response report 0",
		},
		{
			name: "response answers another command",
			tr: WireTransfer{Cmd: CmdGetKey,
				Requests:  [][]byte{reqReport(t, CmdGetKey, 56, 0, true)},
				Responses: [][]byte{resReport(CmdGetFnKey, 56, 0)}},
			want: "answers cmd",
		},
		{
			name: "response does not mirror the request addr",
			tr: WireTransfer{Cmd: CmdGetKey,
				Requests:  [][]byte{reqReport(t, CmdGetKey, 56, 0, true)},
				Responses: [][]byte{resReport(CmdGetKey, 56, 56)}},
			want: "mirrors the request offset",
		},
		{
			name: "response does not mirror the chunk length",
			tr: WireTransfer{Cmd: CmdGetKey,
				Requests:  [][]byte{reqReport(t, CmdGetKey, 56, 0, true)},
				Responses: [][]byte{resReport(CmdGetKey, 48, 0)}},
			want: "mirrors the request chunk",
		},
		{
			name: "more responses than requests",
			tr: WireTransfer{Cmd: CmdGetKey,
				Requests:  [][]byte{reqReport(t, CmdGetKey, 56, 0, true)},
				Responses: [][]byte{resReport(CmdGetKey, 56, 0), resReport(CmdGetKey, 56, 0)}},
			want: "answer 1 request",
		},
		{
			name: "ack does not echo the written chunk",
			tr: WireTransfer{Cmd: CmdSetLEDEffect,
				Requests:  [][]byte{good},
				Responses: [][]byte{resReport(CmdSetLEDEffect, 16, 0)}},
			want: "echo",
		},
		{
			name: "unsolicited garbage",
			tr:   WireTransfer{Responses: [][]byte{{0xA6, 0xFF, 0x01, 0x00}}},
			want: "input report 0",
		},
		{
			name: "exchange without reports",
			tr:   WireTransfer{Cmd: CmdGetKey},
			want: "without",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateTransfer(tc.tr)
			if err == nil {
				t.Fatalf("ValidateTransfer accepted it, want a refusal")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("ValidateTransfer error %q does not mention %q", err, tc.want)
			}
		})
	}
}

// setChunk builds one SET chunk report carrying a real payload — the bytes
// an ack must echo.
func setChunk(t *testing.T, fill byte) []byte {
	t.Helper()
	r, err := (Request{
		Cmd: CmdSetLEDEffect, Length: 16, LastPacket: true,
		Payload: bytes.Repeat([]byte{fill}, 16),
	}).Marshal(64)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	return r
}

// Every problem is reported, not just the first: an import that refuses an
// exchange must say everything wrong with it.
func TestValidateTransferReportsEveryProblem(t *testing.T) {
	err := ValidateTransfer(WireTransfer{Cmd: CmdGetKey, Requests: [][]byte{
		reqReport(t, CmdGetFnKey, 56, 56, true),
		reqReport(t, CmdGetFnKey, 56, 0, true),
	}})
	if err == nil {
		t.Fatal("ValidateTransfer accepted it, want a refusal")
	}
	for _, want := range []string{"carries cmd", "backwards"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("ValidateTransfer error %q does not mention %q", err, want)
		}
	}
}

// TransferName is the fixture's command name (meta.json's `cmd`): the wire's
// own name for the exchange. Reports outside the framing are named UNPARSED
// rather than dropped.
func TestTransferName(t *testing.T) {
	req := reqReport(t, CmdSetKey, 56, 0, true)
	res := resReport(CmdGetDeviceNotify, 2, 0)
	for _, tc := range []struct {
		name string
		tr   WireTransfer
		want string
	}{
		{"request", WireTransfer{Cmd: CmdSetKey, Requests: [][]byte{req}}, "SET_KEY"},
		{"unsolicited", WireTransfer{Responses: [][]byte{res}}, "GET_DEVICE_NOTIFY"},
		{"unparseable input", WireTransfer{Responses: [][]byte{{0xA6, 0xFF, 0x01}}}, "UNPARSED"},
		{"unknown command", WireTransfer{Cmd: 200, Requests: [][]byte{reqReport(t, 200, 1, 0, true)}}, "UNKNOWN_200"},
		{"empty", WireTransfer{}, "UNPARSED"},
	} {
		if got := TransferName(tc.tr); got != tc.want {
			t.Errorf("TransferName(%s) = %q, want %q", tc.name, got, tc.want)
		}
	}
}
