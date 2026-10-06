package protocol

import (
	"path/filepath"
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

// checkExchangeFraming asserts one exchange against the framing of
// docs/protocol.md §2 as recorded — the shared protocol.ValidateTransfer,
// which `nutctl fixtures import-pcap` imports by (one framing contract, not
// two copies) — plus the shape the corpus documents: one transfer carries
// exactly one complete §4 block. Unsolicited input (device notify traffic,
// no requests) carries no block; an exchange named UNPARSED is recorded as
// observed traffic and claims no framing at all.
func checkExchangeFraming(t *testing.T, dir string, x fixture.Exchange) {
	t.Helper()
	id := dir + "/" + x.Case
	if x.Case == "" {
		t.Errorf("%s: empty case name", id)
	}
	tr := WireTransfer{Requests: x.Requests, Responses: x.Responses}
	if want := TransferName(tr); want != x.Cmd {
		t.Errorf("%s: meta cmd = %q, but the wire carries %q", id, x.Cmd, want)
	}
	if err := ValidateTransfer(tr); err != nil {
		if x.Cmd == "UNPARSED" {
			t.Logf("%s: recorded as observed (unparseable input kept, never dropped): %v", id, err)
		} else {
			t.Errorf("%s: %v", id, err)
		}
	}
	if len(x.Requests) == 0 {
		return
	}
	first, err := ParseRequest(x.Requests[0])
	if err != nil {
		return // reported above
	}
	var sumReq int
	for _, report := range x.Requests {
		req, err := ParseRequest(report)
		if err != nil {
			continue // reported above
		}
		sumReq += int(req.Length)
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
}
