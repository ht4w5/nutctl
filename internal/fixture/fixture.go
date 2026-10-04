// Package fixture loads recorded request/response exchanges from
// testdata/captures/<cmd>/<case>.{req,res}.hex plus <case>.meta.json.
//
// One exchange is one transfer as it appeared on the wire: every request report
// (32-byte output report) and every response report (input report), in order.
// Recording live sessions into this format is issue 09; this package only
// reads what is committed to the repo.
package fixture

import (
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
