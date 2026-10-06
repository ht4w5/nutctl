package fixture

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// The corpus write half (ticket 09: `nutctl fixtures record`) is the exact
// inverse of Load: whatever Save writes, Load reads back as the same
// exchange. These tests pin the format round-trip both on a synthetic
// exchange and on every fixture committed to the repo — a recorder that
// changes the format would corrupt the corpus, so the corpus itself is the
// test input.

func TestSaveLoadRoundTrip(t *testing.T) {
	x := Exchange{
		Case: "example",
		Cmd:  "GET_KEY",
		Requests: [][]byte{
			{0xAA, 0x12, 0x38, 0x00, 0x00, 0x00, 0x00, 0x00},
			{0xAA, 0x12, 0x38, 0x38, 0x00, 0x00, 0x01, 0x00},
		},
		Responses: [][]byte{
			{0x55, 0x12, 0x38, 0x00, 0x00, 0x00, 0x00, 0x00},
		},
		Meta: Meta{
			Case:          "example",
			Cmd:           "GET_KEY",
			Model:         "NUT87",
			Connection:    "USB",
			Firmware:      "1.20",
			CaptureMethod: "active-probing",
			Source:        "recorded 2026-10-06 from real hardware — docs/capture.md Method A",
			Unverified:    "some caveat, with § punctuation and <angle> brackets",
		},
	}

	dir := filepath.Join(t.TempDir(), "get_key")
	if err := Save(dir, x); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load(dir, "example")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !reflect.DeepEqual(got, x) {
		t.Errorf("Load(Save(x)) =\n%+v\nwant\n%+v", got, x)
	}
}

// A saved meta.json is readable JSON with every field visible (a fixture's
// provenance is the evidence behind the corpus — it must survive a round
// trip through tools that do not know this struct).
func TestSavedMetaIsPlainJSON(t *testing.T) {
	dir := t.TempDir()
	x := Exchange{Case: "c", Cmd: "GET_KEY", Meta: Meta{Case: "c", Cmd: "GET_KEY", Firmware: "1.20 §"}}
	if err := Save(dir, x); err != nil {
		t.Fatalf("Save: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "c.meta.json"))
	if err != nil {
		t.Fatalf("read meta: %v", err)
	}
	for _, want := range []string{`"case": "c"`, `"firmware": "1.20 §"`, "\n"} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("meta.json missing %q:\n%s", want, raw)
		}
	}
	for _, bad := range []string{`\u00a7`, `\u2014`} {
		if strings.Contains(string(raw), bad) {
			t.Errorf("meta.json escapes non-ASCII (%s); it is UTF-8:\n%s", bad, raw)
		}
	}
}

// Every fixture committed to the repo survives Save → Load unchanged: the
// write half of the corpus format reproduces what the read half accepts, so
// `nutctl fixtures record` output and hand-recorded captures are one format.
func TestCommittedCorpusRoundTripsThroughSave(t *testing.T) {
	root := filepath.Join("..", "..", "testdata", "captures")
	dirs, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("read corpus: %v", err)
	}
	n := 0
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		for _, x := range loadDir(t, filepath.Join(root, d.Name())) {
			dir := t.TempDir()
			if err := Save(dir, x); err != nil {
				t.Fatalf("Save %s/%s: %v", d.Name(), x.Case, err)
			}
			got, err := Load(dir, x.Case)
			if err != nil {
				t.Fatalf("Load %s/%s: %v", d.Name(), x.Case, err)
			}
			if !reflect.DeepEqual(got, x) {
				t.Errorf("%s/%s: Load(Save(x)) differs from the committed fixture", d.Name(), x.Case)
			}
			n++
		}
	}
	if n == 0 {
		t.Fatal("no committed fixtures found — the corpus is the test input")
	}
	t.Logf("%d committed fixtures round-trip through Save", n)
}

func loadDir(t *testing.T, dir string) []Exchange {
	t.Helper()
	xs, err := LoadDir(dir)
	if err != nil {
		t.Fatalf("LoadDir %s: %v", dir, err)
	}
	return xs
}
