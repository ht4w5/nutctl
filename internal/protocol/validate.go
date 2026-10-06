package protocol

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
)

// ValidateTransfer checks one exchange against the framing the corpus is kept
// to (docs/protocol.md §2, refined by the recordings of §7): every report
// parses, requests chunk one transfer (one command, the chunk address never
// walking backwards), and every response answers its own request — same
// command, mirroring the chunk's `addr` and `lenOrType` — while a SET ack
// echoes the chunk payload it was sent (and nothing past the chunk length).
// Unsolicited input (device notify traffic, no requests) is validated as the
// framed input reports it holds; commands may vary within one run, as
// SplitTransfers groups them.
//
// Both callers share this one rule: `nutctl fixtures import-pcap` imports
// only exchanges that validate (docs/capture.md Method B) and corpus_test.go
// keeps the committed corpus to the same contract. Every problem found is
// reported, not just the first.
func ValidateTransfer(tr WireTransfer) error {
	var problems []error
	if len(tr.Requests) == 0 && len(tr.Responses) == 0 {
		return errors.New("exchange without any reports")
	}
	if len(tr.Requests) == 0 {
		for i, report := range tr.Responses {
			if _, err := ParseResponse(report); err != nil {
				problems = append(problems, fmt.Errorf("input report %d: %w", i, err))
			}
		}
		return errors.Join(problems...)
	}

	// The claimed command: what the grouping (SplitTransfers) asserts, or —
	// for an exchange loaded from a fixture — what the first request carries.
	claim := tr.Cmd
	if claim == 0 {
		if req, err := ParseRequest(tr.Requests[0]); err == nil {
			claim = req.Cmd
		}
	}

	prevAddr := uint16(0)
	for i, report := range tr.Requests {
		req, err := ParseRequest(report)
		if err != nil {
			problems = append(problems, fmt.Errorf("request report %d: %w", i, err))
			continue
		}
		if req.Cmd != claim {
			problems = append(problems, fmt.Errorf("request report %d carries cmd %d, want %d", i, req.Cmd, claim))
		}
		if i > 0 && req.Addr < prevAddr {
			problems = append(problems, fmt.Errorf("request report %d walks addr backwards (%d → %d)", i, prevAddr, req.Addr))
		}
		prevAddr = req.Addr
	}
	if len(tr.Responses) > len(tr.Requests) {
		problems = append(problems, fmt.Errorf("%d response reports answer %d request reports",
			len(tr.Responses), len(tr.Requests)))
	}
	for i, report := range tr.Responses {
		resp, err := ParseResponse(report)
		if err != nil {
			problems = append(problems, fmt.Errorf("response report %d: %w", i, err))
			continue
		}
		if resp.Cmd != claim {
			problems = append(problems, fmt.Errorf("response report %d answers cmd %d, want %d", i, resp.Cmd, claim))
		}
		if i >= len(tr.Requests) {
			continue
		}
		req, err := ParseRequest(tr.Requests[i])
		if err != nil {
			continue
		}
		if resp.Addr != req.Addr {
			problems = append(problems, fmt.Errorf("response report %d addr %#x ≠ request addr %#x (§2: the response mirrors the request offset)",
				i, resp.Addr, req.Addr))
		}
		if resp.LenOrType != req.Length {
			problems = append(problems, fmt.Errorf("response report %d lenOrType %d ≠ request chunk length %d (§2: the response mirrors the request chunk)",
				i, resp.LenOrType, req.Length))
		}
		// A SET writes its chunk and the Device echoes it back (§2, §7
		// refinement 3); read chunks carry no request payload and echo
		// nothing. The bytes past the chunk length are the Device's own.
		if !strings.HasPrefix(CommandName(claim), "SET_") || len(req.Payload) == 0 {
			continue
		}
		if len(resp.Data) < len(req.Payload) {
			problems = append(problems, fmt.Errorf("response report %d carries %d data bytes, shorter than the %d-byte chunk it echoes",
				i, len(resp.Data), len(req.Payload)))
		} else if !bytes.Equal(resp.Data[:len(req.Payload)], req.Payload) {
			problems = append(problems, fmt.Errorf("response report %d does not echo the written chunk payload (§2: the ack echoes the chunk back)", i))
		}
	}
	return errors.Join(problems...)
}

// TransferName is the fixture's command name (meta.json's `cmd` and the
// corpus directory, internal/fixture.DirFor): the wire's own name for the
// exchange, named after the firmware's table (§3). Reports outside the
// framing are named UNPARSED rather than dropped — a recording keeps what the
// wire carried, parseable or not.
func TransferName(tr WireTransfer) string {
	if len(tr.Requests) > 0 {
		if req, err := ParseRequest(tr.Requests[0]); err == nil {
			return CommandName(req.Cmd)
		}
		return "UNPARSED"
	}
	if len(tr.Responses) > 0 {
		if resp, err := ParseResponse(tr.Responses[0]); err == nil {
			return CommandName(resp.Cmd)
		}
		return "UNPARSED"
	}
	return "UNPARSED"
}
