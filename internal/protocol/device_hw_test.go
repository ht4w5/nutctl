//go:build device

// Hardware integration tests: run manually against a real NUT87, never in CI.
//
//	go test -tags device ./internal/protocol/ -run Hardware
//
// TestHardwareWriteRoundTrip is the ticket-03 write-path verification pass
// (docs/capture.md Method A): a golden read of every block first, then each
// block written BACK to the Device as one batched transfer (the Device's own
// state — only the SET wire format's forced markers differ), with a read-back
// verification after each write and an observations report that answers the
// open questions (docs/protocol.md §6.8: what the check code reads after a
// write). Nothing is written that the Device did not just report.
//
// Set NUTCTL_FIXTURES_OUT to an ABSOLUTE path (e.g. $(pwd)/testdata/captures)
// to record the raw request/ack exchanges as fixtures (the set_* cases)
// alongside the experiment; the path is resolved against the package
// directory.
package protocol

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/ht4w5/nutctl/internal/hid"
)

// recTransport wraps a Transport and records every report in each direction.
type recTransport struct {
	hid.Transport
	inner chan []byte

	mu   sync.Mutex
	sent [][]byte
	recv [][]byte
}

func newRecTransport(t hid.Transport) *recTransport {
	r := &recTransport{Transport: t, inner: make(chan []byte, 256)}
	go r.pump()
	return r
}

func (r *recTransport) pump() {
	for raw := range r.Transport.Reports() {
		r.mu.Lock()
		r.recv = append(r.recv, bytes.Clone(raw))
		r.mu.Unlock()
		r.inner <- raw
	}
	close(r.inner)
}

func (r *recTransport) SendReport(id uint8, report []byte) error {
	r.mu.Lock()
	r.sent = append(r.sent, bytes.Clone(report))
	r.mu.Unlock()
	return r.Transport.SendReport(id, report)
}

func (r *recTransport) Reports() <-chan []byte { return r.inner }

func (r *recTransport) take() (sent, recv [][]byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	sent, recv = r.sent, r.recv
	r.sent, r.recv = nil, nil
	return sent, recv
}

// hwBlock is one Device block and its write path.
type hwBlock struct {
	setName string                                       // e.g. "SET_KEY"
	dir     string                                       // fixture directory name, e.g. "set_key"
	cmd     byte                                         // the SET command id
	size    int                                          // block size
	read    func(*Device) ([]byte, error)                // raw block bytes as the Device reports them
	write   func(context.Context, *Device, []byte) error // write raw block bytes back
	wire    func([]byte) ([]byte, error)                 // raw block bytes → the SET payload the write path puts on the wire
}

func rawRead(d *Device, cmd byte, size int) ([]byte, error) {
	responses, err := d.runTransfer(context.Background(), transferSpec{
		cmd:                cmd,
		contentSize:        size,
		needLastPacketFlag: true,
	})
	if err != nil {
		return nil, err
	}
	return reassemble(responses, size), nil
}

func hwBlocks() []hwBlock {
	return []hwBlock{
		{setName: "SET_KEY", dir: "set_key", cmd: CmdSetKey, size: KeymapSize,
			read: func(d *Device) ([]byte, error) { return rawRead(d, CmdGetKey, KeymapSize) },
			wire: func(b []byte) ([]byte, error) { km, err := DecodeKeymap(b); return EncodeKeymap(km), err },
			write: func(ctx context.Context, d *Device, b []byte) error {
				km, err := DecodeKeymap(b)
				if err != nil {
					return err
				}
				return d.SetKeymap(ctx, km)
			}},
		{setName: "SET_FN_KEY", dir: "set_fn_key", cmd: CmdSetFnKey, size: KeymapSize,
			read: func(d *Device) ([]byte, error) { return rawRead(d, CmdGetFnKey, KeymapSize) },
			wire: func(b []byte) ([]byte, error) { km, err := DecodeKeymap(b); return EncodeKeymap(km), err },
			write: func(ctx context.Context, d *Device, b []byte) error {
				km, err := DecodeKeymap(b)
				if err != nil {
					return err
				}
				return d.SetFnKeymap(ctx, km)
			}},
		{setName: "SET_LED_EFFECT", dir: "set_led_effect", cmd: CmdSetLEDEffect, size: LEDEffectSize,
			read: func(d *Device) ([]byte, error) { return rawRead(d, CmdGetLEDEffect, LEDEffectSize) },
			wire: func(b []byte) ([]byte, error) { e, err := DecodeLightingEffect(b); return EncodeLightingEffect(e), err },
			write: func(ctx context.Context, d *Device, b []byte) error {
				e, err := DecodeLightingEffect(b)
				if err != nil {
					return err
				}
				return d.SetLightingEffect(ctx, e)
			}},
		{setName: "SET_CUSTOM_LED_DATA", dir: "set_custom_led_data", cmd: CmdSetCustomLEDData, size: PerKeyRGBSize,
			read: func(d *Device) ([]byte, error) { return rawRead(d, CmdGetCustomLEDData, PerKeyRGBSize) },
			wire: func(b []byte) ([]byte, error) { rgb, err := DecodePerKeyRGB(b); return EncodePerKeyRGB(rgb), err },
			write: func(ctx context.Context, d *Device, b []byte) error {
				rgb, err := DecodePerKeyRGB(b)
				if err != nil {
					return err
				}
				return d.SetPerKeyRGB(ctx, rgb)
			}},
		{setName: "SET_GAME_MODE", dir: "set_game_mode", cmd: CmdSetGameMode, size: SettingsSize,
			read: func(d *Device) ([]byte, error) { return rawRead(d, CmdGetGameMode, SettingsSize) },
			wire: func(b []byte) ([]byte, error) { s, err := DecodeSettings(b); return EncodeSettings(s), err },
			write: func(ctx context.Context, d *Device, b []byte) error {
				s, err := DecodeSettings(b)
				if err != nil {
					return err
				}
				return d.SetSettings(ctx, s)
			}},
	}
}

func TestHardwareWriteRoundTrip(t *testing.T) {
	info, dev, rec := openHardware(t)
	defer dev.Close()
	rec.take() // drop the GET_DEVICE_INFO exchange from the log
	t.Logf("device: USB %04x:%04x firmware %s (frameVersion %d, firmwareStatus %d)",
		info.VID, info.PID, info.Version, info.FrameVersion, info.FirmwareStatus)
	if info.FirmwareStatus != FirmwareOK {
		t.Fatalf("device reports firmwareStatus %d (bootloader/recovery) — refusing to write", info.FirmwareStatus)
	}

	// --- golden read first (ADR-0003): every block as raw bytes, saved
	// before any write.
	golden := map[string][]byte{}
	for _, b := range hwBlocks() {
		raw, err := b.read(dev)
		if err != nil {
			t.Fatalf("GET_%s: %v", b.dir, err)
		}
		golden[b.dir] = raw
		writeHexFile(t, filepath.Join(os.TempDir(), "nut87-golden-"+b.dir+".hex"), raw)
		rec.take()
	}

	// --- write each block back, verify by read-back after each one.
	for _, b := range hwBlocks() {
		if err := b.write(context.Background(), dev, golden[b.dir]); err != nil {
			t.Fatalf("%s write: %v (stopping before later blocks are touched)", b.setName, err)
		}
		sentW, recvW := rec.take()
		wire, err := b.wire(golden[b.dir])
		if err != nil {
			t.Fatalf("%s wire encode: %v", b.setName, err)
		}
		recordFixture(t, b, sentW, recvW)

		back, err := b.read(dev)
		if err != nil {
			t.Fatalf("%s read-back: %v", b.setName, err)
		}
		rec.take()
		if diff := blockDiff(b, wire, back); len(diff) > 0 {
			t.Errorf("%s: read-back differs from what the wire carried:\n%s", b.setName, strings.Join(diff, "\n"))
		} else {
			t.Logf("%s: read-back matches the written block byte for byte", b.setName)
		}
		if diff := blockDiff(b, golden[b.dir], back); len(diff) > 0 {
			t.Logf("OBSERVATION %s: %d byte(s) the Device reports differently after the write than before:\n%s",
				b.setName, len(diff), strings.Join(diff, "\n"))
		}
	}

	// --- observations (docs/protocol.md §6.8 and friends).
	e, err := dev.LightingEffect(context.Background())
	if err != nil {
		t.Fatalf("GET_LED_EFFECT: %v", err)
	}
	t.Logf("OBSERVATION lighting check code after SET_LED_EFFECT: %02x %02x at offsets 14..15 (driverSetting %02x)",
		e.CheckCode[0], e.CheckCode[1], e.DriverSetting)
	km, err := dev.Keymap(context.Background())
	if err != nil {
		t.Fatalf("GET_KEY: %v", err)
	}
	t.Logf("OBSERVATION keymap block tail after SET_KEY: %02x %02x %02x %02x",
		km[127].Raw[0], km[127].Raw[1], km[127].Raw[2], km[127].Raw[3])
	rgb, err := dev.PerKeyRGB(context.Background())
	if err != nil {
		t.Fatalf("GET_CUSTOM_LED_DATA: %v", err)
	}
	t.Logf("OBSERVATION per-key ledIds after SET_CUSTOM_LED_DATA: entry 0 = %d, entry 127 = %d",
		rgb[0].LEDID, rgb[127].LEDID)
}

// blockDiff names every byte position where two blocks differ (with
// Key-Slot/entry naming where the block has structure).
func blockDiff(b hwBlock, want, got []byte) []string {
	var out []string
	for i := range want {
		if want[i] == got[i] {
			continue
		}
		switch b.dir {
		case "set_key", "set_fn_key":
			out = append(out, fmt.Sprintf("  byte %d (Key Slot %d raw[%d]): %02x → %02x", i, i/4, i%4, want[i], got[i]))
		case "set_custom_led_data":
			out = append(out, fmt.Sprintf("  byte %d (entry %d raw[%d]): %02x → %02x", i, i/4, i%4, want[i], got[i]))
		default:
			out = append(out, fmt.Sprintf("  byte %d: %02x → %02x", i, want[i], got[i]))
		}
	}
	return out
}

// recordFixture writes one set_* exchange as a fixture (only when
// NUTCTL_FIXTURES_OUT is set).
func recordFixture(t *testing.T, b hwBlock, sent, recv [][]byte) {
	t.Helper()
	outDir := os.Getenv("NUTCTL_FIXTURES_OUT")
	if outDir == "" {
		return
	}
	dir := filepath.Join(outDir, b.dir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	caseName := "nut87"
	writeReports(t, filepath.Join(dir, caseName+".req.hex"), sent)
	writeReports(t, filepath.Join(dir, caseName+".res.hex"), recv)
	meta := fmt.Sprintf(`{
  "case": %q,
  "cmd": %q,
  "model": "NUT87",
  "connection": "USB",
  "firmware": "1.20",
  "captureMethod": "active-probing",
  "source": "recorded 2026-10-05 from real hardware (USB 0c45:880c, interface usage page 0xFF68, firmware 1.20) via internal/protocol over the hidraw transport — docs/capture.md Method A; write-back of the Device's own state read moments earlier (ticket 03 write path verification); 64-byte reports, %d request reports and %d ack reports",
  "unverified": "the ack reports beyond the echoed cmd are ignored by the transfer engine (write responses match by cmd only); their content is recorded as observed"
}
`, caseName, b.setName, len(sent), len(recv))
	if err := os.WriteFile(filepath.Join(dir, caseName+".meta.json"), []byte(meta), 0o644); err != nil {
		t.Fatalf("write meta: %v", err)
	}
	t.Logf("recorded fixture %s/%s (%d request reports, %d acks)", b.dir, caseName, len(sent), len(recv))
}

func writeReports(t *testing.T, path string, reports [][]byte) {
	t.Helper()
	var sb strings.Builder
	sb.WriteString("# one report per line\n")
	for _, r := range reports {
		sb.WriteString(strings.ToUpper(hex.EncodeToString(r)))
		sb.WriteString("\n")
	}
	if err := os.WriteFile(path, []byte(sb.String()), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func writeHexFile(t *testing.T, path string, payload []byte) {
	t.Helper()
	if err := os.WriteFile(path, []byte(hex.EncodeToString(payload)+"\n"), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// openHardware finds the NUT87 on the wire and opens a protocol session over
// a recording transport.
func openHardware(t *testing.T) (DeviceInfo, *Device, *recTransport) {
	t.Helper()
	enum := hid.NewEnumerator()
	infos, err := enum.Enumerate()
	if err != nil {
		t.Fatalf("enumerate: %v", err)
	}
	for _, info := range infos {
		if info.ProductName != "NUT87" || info.UsagePage != 0xFF68 {
			continue
		}
		tr, err := enum.Open(info)
		if err != nil {
			t.Fatalf("open %s: %v", info.Path, err)
		}
		rec := newRecTransport(tr)
		dev, err := Open(rec, Options{})
		if err != nil {
			t.Fatalf("open protocol: %v", err)
		}
		di, err := dev.Info(context.Background())
		if err != nil {
			t.Fatalf("GET_DEVICE_INFO: %v", err)
		}
		return di, dev, rec
	}
	t.Fatal("no NUT87 with usage page 0xFF68 found — is the keyboard attached?")
	return DeviceInfo{}, nil, nil
}
