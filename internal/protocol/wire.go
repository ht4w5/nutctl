package protocol

import (
	"fmt"
)

// Recording and replaying the wire (ticket 09's `nutctl fixtures record`,
// ticket 10's pcap importer): a wire log is grouped into per-transfer
// exchanges — the unit the fixture corpus stores (internal/fixture). The
// grouping is the transfer layer's own request/response matching (§2 of
// docs/protocol.md) replayed backwards over a recording, so a fixture that
// groups wrong is a bug report about the framing, not about the recorder.

// WireReport is one report as it appeared on the wire, with its direction.
type WireReport struct {
	Sent   bool // true: host → Device (output report); false: Device → host
	Report []byte
}

// WireTransfer is one request/response exchange as it appeared on the wire:
// the request reports of one chunked transfer, in order, and the response
// reports that answered it (matched by cmd, optionally addr —
// docs/protocol.md §2). An exchange with no Requests is unsolicited input
// traffic (device notify reports and anything else the Device pushed,
// recorded as observed).
type WireTransfer struct {
	Cmd       byte
	Requests  [][]byte
	Responses [][]byte
}

// responselessCommands are the commands the Device never answers
// (docs/protocol.md §4: SET_FACTORY_RESET is one fire-and-forget report). A
// transfer of one of these is complete the moment its report is sent.
var responselessCommands = map[byte]bool{
	CmdSetFactoryReset: true,
}

// SplitTransfers groups a wire log into per-transfer exchanges.
//
// Requests chunk a transfer: a request continues the open transfer while it
// carries the same command and does not walk the chunk address backwards;
// the last-packet flag (and any responseless command) closes it. Responses
// are matched back to the transfer awaiting their cmd — never to whatever
// happens to be open — and anything unmatched (device notify traffic,
// unparseable input) is kept as an unsolicited exchange of its own, in
// stream order. Nothing is dropped: what the wire carried is what comes out.
//
// A request report that does not follow the framing is an error: a recorder
// that cannot parse its own output must fail loudly rather than commit a
// fixture that does not parse.
func SplitTransfers(log []WireReport) ([]WireTransfer, error) {
	type xfer struct {
		WireTransfer
		prevAddr uint16
		sawLast  bool
	}
	var out []xfer
	unsolicitedOpen := false

	for _, wr := range log {
		if wr.Sent {
			req, err := ParseRequest(wr.Report)
			if err != nil {
				return nil, fmt.Errorf("request report % X: %w", wr.Report, err)
			}
			continues := false
			if n := len(out); n > 0 {
				open := &out[n-1]
				continues = open.Cmd == req.Cmd && !open.sawLast &&
					!responselessCommands[open.Cmd] && req.Addr >= open.prevAddr
			}
			if !continues {
				out = append(out, xfer{WireTransfer: WireTransfer{Cmd: req.Cmd}})
			}
			open := &out[len(out)-1]
			open.Requests = append(open.Requests, wr.Report)
			open.prevAddr = req.Addr
			open.sawLast = req.LastPacket
			unsolicitedOpen = false
			continue
		}

		matched := false
		if resp, err := ParseResponse(wr.Report); err == nil {
			for i := len(out) - 1; i >= 0; i-- {
				t := &out[i]
				if t.Cmd == resp.Cmd && len(t.Requests) > len(t.Responses) {
					t.Responses = append(t.Responses, wr.Report)
					matched = true
					break
				}
			}
		}
		if matched {
			unsolicitedOpen = false
			continue
		}
		// Unsolicited input: a run of consecutive reports is one exchange.
		if !unsolicitedOpen {
			out = append(out, xfer{WireTransfer: WireTransfer{Cmd: unsolicitedCmd(wr.Report)}})
			unsolicitedOpen = true
		}
		out[len(out)-1].Responses = append(out[len(out)-1].Responses, wr.Report)
	}

	res := make([]WireTransfer, len(out))
	for i := range out {
		res[i] = out[i].WireTransfer
	}
	return res, nil
}

// unsolicitedCmd names the command an unsolicited input report claims to be
// (device notify traffic carries a real cmd, docs/protocol.md §4); reports
// outside the framing carry none (Cmd 0).
func unsolicitedCmd(report []byte) byte {
	if resp, err := ParseResponse(report); err == nil {
		return resp.Cmd
	}
	return 0
}
