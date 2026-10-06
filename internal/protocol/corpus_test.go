package protocol

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ht4w5/nutctl/internal/fixture"
)

// The committed corpus (testdata/captures) is the recorded reality the
// protocol notes are reconciled against (ticket 09). Two properties are
// pinned here:
//
//   - Coverage: every command the v0 read and write paths put on the wire has
//     recorded fixtures — `go test ./...` without hardware keeps testing what
//     the tool actually does.
//   - Framing: every committed exchange follows docs/protocol.md §2 as
//     recorded, byte for byte — a fixture that disagrees with the framing is
//     a protocol bug report, and its failure names the fixture.
//
// Not covered here, by design: SET_FACTORY_RESET (one fire-and-forget report
// with no response — its frame is pinned against the bundle's encoder in
// reset_test.go; a factory reset is destructive and is recorded by
// `nutctl fixtures record` when a hardware reset run happens) and the device
// notify commands (unsolicited input, not part of a transfer — recorded the
// same way from `nutctl watch` runs).

// v0ReadPath is every command one full read pass puts on the wire: the
// identity probe (device.OpenInfo) plus the five blocks of Session.Read.
var v0ReadPath = []byte{
	CmdGetDeviceInfo,
	CmdGetKey, CmdGetFnKey, CmdGetLEDEffect, CmdGetCustomLEDData, CmdGetGameMode,
}

// v0WritePath is every command the batched write path (device.Apply) puts on
// the wire: one complete-block transfer per state block.
var v0WritePath = []byte{
	CmdSetKey, CmdSetFnKey, CmdSetLEDEffect, CmdSetCustomLEDData, CmdSetGameMode,
}

// blockSizes is the documented payload size of each block command
// (docs/protocol.md §4): one transfer carries exactly one complete block.
var blockSizes = map[byte]int{
	CmdGetDeviceInfo:    DeviceInfoSize,
	CmdGetGameMode:      SettingsSize,
	CmdGetKey:           KeymapSize,
	CmdGetFnKey:         KeymapSize,
	CmdGetLEDEffect:     LEDEffectSize,
	CmdGetCustomLEDData: PerKeyRGBSize,
	CmdSetKey:           KeymapSize,
	CmdSetFnKey:         KeymapSize,
	CmdSetLEDEffect:     LEDEffectSize,
	CmdSetCustomLEDData: PerKeyRGBSize,
	CmdSetGameMode:      SettingsSize,
}

func TestCorpusCoversV0ReadAndWritePaths(t *testing.T) {
	for _, tc := range []struct {
		what string
		cmds []byte
	}{{"read path", v0ReadPath}, {"write path", v0WritePath}} {
		for _, cmd := range tc.cmds {
			name := CommandName(cmd)
			dir := filepath.Join(fixturesDir, fixture.DirFor(name))
			xs, err := fixture.LoadDir(dir)
			if err != nil {
				t.Errorf("%s %s: no readable fixtures in %s: %v", tc.what, name, dir, err)
				continue
			}
			if len(xs) == 0 {
				t.Errorf("%s %s: corpus has no fixtures for it (%s is empty)", tc.what, name, dir)
				continue
			}
			for _, x := range xs {
				if x.Cmd != name {
					t.Errorf("%s/%s: meta cmd = %q, want %q", filepath.Base(dir), x.Case, x.Cmd, name)
				}
			}
			t.Logf("%s %s: %d fixture(s) in %s", tc.what, name, len(xs), dir)
		}
	}
}

func TestCorpusFollowsFraming(t *testing.T) {
	dirs, err := filepath.Glob(filepath.Join(fixturesDir, "*"))
	if err != nil {
		t.Fatalf("glob corpus: %v", err)
	}
	exchanges := 0
	for _, dir := range dirs {
		xs, err := fixture.LoadDir(dir)
		if err != nil {
			t.Fatalf("LoadDir %s: %v", dir, err)
		}
		for _, x := range xs {
			exchanges++
			checkExchangeFraming(t, filepath.Base(dir), x)
		}
	}
	if exchanges == 0 {
		t.Fatal("no committed fixtures found — the corpus is the evidence")
	}
	t.Logf("%d committed exchanges follow the documented framing", exchanges)
}

// checkExchangeFraming asserts one exchange against docs/protocol.md §2 as
// recorded: request and response reports parse, responses answer their own
// command at their own chunk offset, and one transfer carries exactly one
// complete block.
func checkExchangeFraming(t *testing.T, dir string, x fixture.Exchange) {
	t.Helper()
	id := dir + "/" + x.Case
	if x.Case == "" {
		t.Errorf("%s: empty case name", id)
	}
	if len(x.Requests) == 0 {
		t.Errorf("%s: exchange without request reports", id)
		return
	}
	first, err := ParseRequest(x.Requests[0])
	if err != nil {
		t.Errorf("%s: %v", id, err)
		return
	}
	if name := CommandName(first.Cmd); name != x.Cmd {
		t.Errorf("%s: meta cmd = %q, but the wire carries %q", id, x.Cmd, name)
	}

	var sumReq int
	for i, report := range x.Requests {
		req, err := ParseRequest(report)
		if err != nil {
			t.Errorf("%s: request report %d: %v", id, i, err)
			continue
		}
		if req.Cmd != first.Cmd {
			t.Errorf("%s: request report %d carries cmd %d, want %d", id, i, req.Cmd, first.Cmd)
		}
		sumReq += int(req.Length)
		if i > 0 {
			prev, _ := ParseRequest(x.Requests[i-1])
			if req.Addr < prev.Addr {
				t.Errorf("%s: request report %d walks addr backwards (%d → %d)", id, i, prev.Addr, req.Addr)
			}
		}
	}
	for i, report := range x.Responses {
		resp, err := ParseResponse(report)
		if err != nil {
			t.Errorf("%s: response report %d: %v", id, i, err)
			continue
		}
		if resp.Cmd != first.Cmd {
			t.Errorf("%s: response report %d answers cmd %d, want %d", id, i, resp.Cmd, first.Cmd)
		}
		if i < len(x.Requests) {
			req, err := ParseRequest(x.Requests[i])
			if err == nil && resp.Addr != req.Addr {
				t.Errorf("%s: response report %d addr %#x ≠ request addr %#x (§2: the response mirrors the request offset)",
					id, i, resp.Addr, req.Addr)
			}
			if err == nil && resp.LenOrType != req.Length {
				t.Errorf("%s: response report %d lenOrType %d ≠ request chunk length %d (§2: the response mirrors the request chunk)",
					id, i, resp.LenOrType, req.Length)
			}
		}
	}

	size, known := blockSizes[first.Cmd]
	if !known {
		return
	}
	if sumReq != size {
		t.Errorf("%s: request chunks carry %d payload bytes in total, want one %d-byte block (§4)",
			id, sumReq, size)
	}
	if len(x.Responses) == 0 {
		return
	}
	if got := Reassemble(x.Responses, size); len(got) != size {
		t.Errorf("%s: responses reassemble to %d bytes, want %d (§2 reassembly)", id, len(got), size)
	}
	// §2: "the NUT87's ack echoes the written block back". For a write, the
	// response data of each chunk is the chunk payload — what the read-back
	// verification depends on. The bytes a chunk does not claim (padding past
	// the chunk length) are the Device's own and are not compared.
	if strings.HasPrefix(x.Cmd, "SET_") {
		for i := range x.Responses {
			if i >= len(x.Requests) {
				break
			}
			req, err1 := ParseRequest(x.Requests[i])
			resp, err2 := ParseResponse(x.Responses[i])
			if err1 != nil || err2 != nil {
				continue
			}
			want := req.Payload
			if len(resp.Data) < len(want) {
				t.Errorf("%s: ack %d carries %d data bytes, shorter than the chunk it echoes (%d)",
					id, i, len(resp.Data), len(want))
				continue
			}
			if got := resp.Data[:len(want)]; !bytes.Equal(got, want) {
				t.Errorf("%s: ack %d does not echo the written chunk payload:\n got % X\nwant % X", id, i, got, want)
			}
		}
	}
}
