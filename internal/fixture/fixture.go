// Package fixture loads and stores recorded request/response exchanges from
// testdata/captures/<cmd>/<case>.{req,res}.hex plus <case>.meta.json.
//
// One exchange is one transfer as it appeared on the wire: every request
// report (output report) and every response report (input report), in order.
// Load reads what is committed to the repo; Save writes the same format —
// `nutctl fixtures record` (docs/capture.md Method A) records live sessions
// with it, so probing the protocol and building the corpus are one activity.
package fixture

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Exchange is one recorded request/response transfer.
type Exchange struct {
	Case      string
	Cmd       string
	Requests  [][]byte
	Responses [][]byte
	Meta      Meta
}

// Meta is the provenance recorded alongside an exchange (docs/capture.md).
type Meta struct {
	Case          string `json:"case"`
	Cmd           string `json:"cmd"`
	Model         string `json:"model"`
	Connection    string `json:"connection"`
	Firmware      string `json:"firmware"`
	CaptureMethod string `json:"captureMethod"`
	Source        string `json:"source"`
	Unverified    string `json:"unverified,omitempty"`
}

// Load reads one exchange from dir: <name>.req.hex, <name>.res.hex and
// <name>.meta.json.
func Load(dir, name string) (Exchange, error) {
	reqs, err := readReports(filepath.Join(dir, name+".req.hex"))
	if err != nil {
		return Exchange{}, err
	}
	resps, err := readReports(filepath.Join(dir, name+".res.hex"))
	if err != nil {
		return Exchange{}, err
	}
	metaRaw, err := os.ReadFile(filepath.Join(dir, name+".meta.json"))
	if err != nil {
		return Exchange{}, err
	}
	var meta Meta
	if err := json.Unmarshal(metaRaw, &meta); err != nil {
		return Exchange{}, fmt.Errorf("fixture %s/%s: meta.json: %w", dir, name, err)
	}
	return Exchange{
		Case:      name,
		Cmd:       meta.Cmd,
		Requests:  reqs,
		Responses: resps,
		Meta:      meta,
	}, nil
}

// LoadDir reads every exchange in dir (one per .req.hex file, sorted).
func LoadDir(dir string) ([]Exchange, error) {
	matches, err := filepath.Glob(filepath.Join(dir, "*.req.hex"))
	if err != nil {
		return nil, err
	}
	var out []Exchange
	for _, m := range matches {
		base := strings.TrimSuffix(filepath.Base(m), ".req.hex")
		x, err := Load(dir, base)
		if err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, nil
}

// Save writes one exchange to dir as <case>.req.hex, <case>.res.hex and
// <case>.meta.json — the exact format Load reads back (the round trip is
// pinned by the package tests over the committed corpus). Existing files for
// the same case are replaced: re-recording a case refreshes it.
func Save(dir string, x Exchange) error {
	if x.Case == "" {
		return fmt.Errorf("fixture: empty case name")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := writeReports(filepath.Join(dir, x.Case+".req.hex"), x.Requests); err != nil {
		return err
	}
	if err := writeReports(filepath.Join(dir, x.Case+".res.hex"), x.Responses); err != nil {
		return err
	}
	meta := x.Meta
	meta.Case = x.Case
	meta.Cmd = x.Cmd
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(meta); err != nil {
		return fmt.Errorf("fixture %s/%s: meta.json: %w", dir, x.Case, err)
	}
	return os.WriteFile(filepath.Join(dir, x.Case+".meta.json"), buf.Bytes(), 0o644)
}

// writeReports writes a hex report file: one report per line, uppercase hex.
func writeReports(path string, reports [][]byte) error {
	var sb strings.Builder
	sb.WriteString(reportFileHeader + "\n")
	for _, r := range reports {
		sb.WriteString(strings.ToUpper(hex.EncodeToString(r)))
		sb.WriteString("\n")
	}
	return os.WriteFile(path, []byte(sb.String()), 0o644)
}

// reportFileHeader is the first line of every .hex report file.
const reportFileHeader = "# one report per line"

// DirFor maps a wire command name (meta.json's `cmd`, e.g. "GET_KEY") to its
// corpus directory (testdata/captures/get_key). The corpus is organized one
// directory per command, named after the command in lower case.
func DirFor(cmdName string) string { return strings.ToLower(cmdName) }

// readReports parses a hex report file: one report per line, '#' comments and
// blank lines ignored.
func readReports(path string) ([][]byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out [][]byte
	for n, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.ReplaceAll(line, " ", "")
		b, err := hex.DecodeString(line)
		if err != nil {
			return nil, fmt.Errorf("%s:%d: %w", path, n+1, err)
		}
		out = append(out, b)
	}
	return out, nil
}
