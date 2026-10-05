package protocol

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ht4w5/nutctl/internal/fixture"
	"github.com/ht4w5/nutctl/internal/hid"
	"github.com/ht4w5/nutctl/internal/hidfake"
)

func loadFixture(t *testing.T, cmd, name string) fixture.Exchange {
	t.Helper()
	x, err := fixture.Load(fixturesDir+"/"+cmd, name)
	if err != nil {
		t.Fatalf("load fixture %s/%s: %v", cmd, name, err)
	}
	return x
}

// newFake builds a scripted fake Device for a fixture. The report length
// comes from the fixture's wire bytes — the real transport takes it from the
// HID descriptor, and the chunker must follow it either way.
func newFake(t *testing.T, x fixture.Exchange, script func(*hidfake.Device)) *hidfake.Device {
	t.Helper()
	fake := hidfake.New(hid.Info{
		Path: "/dev/hidraw3", VendorID: 0x0C45, ProductID: 0x880C,
		ProductName: "NUT87", ReportLength: len(x.Requests[0]),
	})
	script(fake)
	return fake
}

func newDevice(t *testing.T, fake *hidfake.Device) *Device {
	t.Helper()
	dev, err := Open(fake, Options{Timeout: 20 * time.Millisecond})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { dev.Close() })
	return dev
}

func TestInfoOverFakeDevice(t *testing.T) {
	// Recorded from real hardware: the request reports our chunker produces
	// must be the ones the Device actually answered.
	x := loadFixture(t, "get_device_info", "nut87")
	fake := newFake(t, x, func(f *hidfake.Device) { f.Replay(x) })
	dev := newDevice(t, fake)

	info, err := dev.Info(context.Background())
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if info.VID != 0x0C45 || info.PID != 0x880C {
		t.Errorf("vid:pid = %#x:%#x, want 0c45:880c", info.VID, info.PID)
	}
	if info.Version != "1.20" {
		t.Errorf("Version = %q, want %q", info.Version, "1.20")
	}
	if info.MacroSpaceSize != 3072 || info.RomSize != 64 {
		t.Errorf("macroSpaceSize/romSize = %d/%d, want 3072/64", info.MacroSpaceSize, info.RomSize)
	}
	if info.FirmwareStatus != FirmwareOK {
		t.Errorf("FirmwareStatus = %d, want %d", info.FirmwareStatus, FirmwareOK)
	}

	sent := fake.Sent()
	if len(sent) != len(x.Requests) {
		t.Fatalf("sent %d request reports, want %d", len(sent), len(x.Requests))
	}
	for i := range sent {
		if !bytes.Equal(sent[i], x.Requests[i]) {
			t.Errorf("request report %d:\n got  %X\n want %X", i, sent[i], x.Requests[i])
		}
	}
}

func TestSettingsOverFakeDevice(t *testing.T) {
	x := loadFixture(t, "get_game_mode", "nut87")
	fake := newFake(t, x, func(f *hidfake.Device) { f.Replay(x) })
	dev := newDevice(t, fake)

	settings, err := dev.Settings(context.Background())
	if err != nil {
		t.Fatalf("Settings: %v", err)
	}
	if settings.ReportRate != ReportRate8K {
		t.Errorf("ReportRate = %v (wire %d), want 8K (wire 6)", settings.ReportRate, uint8(settings.ReportRate))
	}
	if settings.SleepTime != 5 || settings.KeyDelay != 3 {
		t.Errorf("sleepTime/keyDelay = %d/%d, want 5/3", settings.SleepTime, settings.KeyDelay)
	}
}

func TestMultiChunkTransferOverFakeDevice(t *testing.T) {
	// The seed fixtures use 32-byte reports, so a 48-byte read is two chunks
	// with two request reports — the chunker must follow the report length.
	x := loadFixture(t, "get_device_info", "seed-32byte")
	fake := newFake(t, x, func(f *hidfake.Device) { f.Replay(x) })
	dev := newDevice(t, fake)

	info, err := dev.Info(context.Background())
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if info.Version != "1.06" {
		t.Errorf("Version = %q, want 1.06", info.Version)
	}
	if got := len(fake.Sent()); got != 2 {
		t.Errorf("sent %d request reports, want 2 chunks", got)
	}
}

func TestDroppedResponseIsRetried(t *testing.T) {
	x := loadFixture(t, "get_device_info", "seed-32byte")
	fake := newFake(t, x, func(f *hidfake.Device) {
		f.ScriptDrop(x.Requests[0]) // first response is lost
		f.Script(x.Requests[0], x.Responses[0])
		f.Script(x.Requests[1], x.Responses[1])
	})
	dev := newDevice(t, fake)

	if _, err := dev.Info(context.Background()); err != nil {
		t.Fatalf("Info: %v", err)
	}
	want := [][]byte{x.Requests[0], x.Requests[0], x.Requests[1]}
	sent := fake.Sent()
	if len(sent) != len(want) {
		t.Fatalf("sent %d request reports, want %d: %X", len(sent), len(want), sent)
	}
	for i := range want {
		if !bytes.Equal(sent[i], want[i]) {
			t.Errorf("request report %d = %X, want %X", i, sent[i], want[i])
		}
	}
}

func TestNoResponseFailsAfterRetries(t *testing.T) {
	x := loadFixture(t, "get_device_info", "seed-32byte")
	fake := newFake(t, x, func(f *hidfake.Device) {
		for i := 0; i <= DefaultMaxRetries; i++ {
			f.ScriptDrop(x.Requests[0])
		}
	})
	dev := newDevice(t, fake)

	_, err := dev.Info(context.Background())
	if err == nil {
		t.Fatal("Info succeeded, want timeout error")
	}
	if !strings.Contains(err.Error(), "no response") {
		t.Errorf("error = %v, want a no-response error", err)
	}
	if got := len(fake.Sent()); got != 1+DefaultMaxRetries {
		t.Errorf("sent %d request reports, want %d (1 + %d retries)", got, 1+DefaultMaxRetries, DefaultMaxRetries)
	}
}

func TestGarbageHeaderResponseIsRetriedThenFails(t *testing.T) {
	x := loadFixture(t, "get_device_info", "seed-32byte")
	junk := []byte{0xAA, 0xAA, 0xAA, 0xAA, 0xAA, 0xAA, 0xAA, 0xAA} // wrong magic
	fake := newFake(t, x, func(f *hidfake.Device) {
		for i := 0; i <= DefaultMaxRetries; i++ {
			f.ScriptGarbage(x.Requests[0], junk)
		}
	})
	dev := newDevice(t, fake)

	if _, err := dev.Info(context.Background()); err == nil {
		t.Fatal("Info succeeded against garbage responses, want error")
	}
	if got := len(fake.Sent()); got != 1+DefaultMaxRetries {
		t.Errorf("sent %d request reports, want %d (garbage must not count as a response)", got, 1+DefaultMaxRetries)
	}
}

func TestGarbageThenGoodResponseSucceeds(t *testing.T) {
	x := loadFixture(t, "get_device_info", "seed-32byte")
	fake := newFake(t, x, func(f *hidfake.Device) {
		f.ScriptGarbage(x.Requests[0], []byte{0x00, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07})
		f.Script(x.Requests[0], x.Responses[0])
		f.Script(x.Requests[1], x.Responses[1])
	})
	dev := newDevice(t, fake)

	info, err := dev.Info(context.Background())
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if info.Version != "1.06" {
		t.Errorf("Version = %q, want 1.06", info.Version)
	}
}

// assertSentMatch proves the request reports our chunker produced are the
// ones the fixture's Device actually answered — byte-exact.
func assertSentMatch(t *testing.T, fake *hidfake.Device, x fixture.Exchange) {
	t.Helper()
	sent := fake.Sent()
	if len(sent) != len(x.Requests) {
		t.Fatalf("sent %d request reports, want %d", len(sent), len(x.Requests))
	}
	for i := range sent {
		if !bytes.Equal(sent[i], x.Requests[i]) {
			t.Errorf("request report %d:\n got  %X\n want %X", i, sent[i], x.Requests[i])
		}
	}
}

func TestKeymapOverFakeDevice(t *testing.T) {
	x := loadFixture(t, "get_key", "nut87")
	fake := newFake(t, x, func(f *hidfake.Device) { f.Replay(x) })
	dev := newDevice(t, fake)

	keymap, err := dev.Keymap(context.Background())
	if err != nil {
		t.Fatalf("Keymap: %v", err)
	}
	if want := (KeyAction{Type: ActionKeyboard, Params: [3]byte{0, 41, 0}, Raw: [4]byte{2, 0, 41, 0}}); keymap[0] != want {
		t.Errorf("slot 0 = %+v, want %+v (Esc = KEYBOARD keycode 41)", keymap[0], want)
	}
	if want := (KeyAction{Type: ActionKeyboard, Params: [3]byte{0, 0x3B, 0}, Raw: [4]byte{2, 0, 0x3B, 0}}); keymap[2] != want {
		t.Errorf("slot 2 = %+v, want %+v (F2)", keymap[2], want)
	}
	for slot, want := range map[int][4]byte{
		13: {3, 0xE9, 0, 0}, // knob clockwise: Volume Up
		14: {3, 0xEA, 0, 0}, // knob counter-clockwise: Volume Down
		15: {3, 0xE2, 0, 0}, // knob press: Mute
	} {
		if keymap[slot].Type != ActionConsumer || keymap[slot].Raw != want {
			t.Errorf("slot %d = %+v, want CONSUMER raw %X", slot, keymap[slot], want)
		}
	}
	// Slot 29 is a firmware-matrix Key Slot (shared matrix with sibling
	// boards): it carries a default KEYBOARD binding on every healthy Device.
	if want := (KeyAction{Type: ActionKeyboard, Params: [3]byte{0, 0x53, 0}, Raw: [4]byte{2, 0, 0x53, 0}}); keymap[29] != want {
		t.Errorf("slot 29 = %+v, want %+v (firmware-matrix default binding)", keymap[29], want)
	}
	if keymap[112] != (KeyAction{}) {
		t.Errorf("slot 112 = %+v, want DEFAULT", keymap[112])
	}
	// The block-tail marker 00 00 AA 55 lands in the last Key Slot's raw
	// bytes (block bytes 508..511).
	if want := (KeyAction{Type: ActionDefault, Params: [3]byte{0, 0xAA, 0x55}, Raw: [4]byte{0, 0, 0xAA, 0x55}}); keymap[127] != want {
		t.Errorf("slot 127 = %+v, want %+v (block-tail marker)", keymap[127], want)
	}
	assertSentMatch(t, fake, x)
}

func TestFnKeymapOverFakeDevice(t *testing.T) {
	x := loadFixture(t, "get_fn_key", "nut87")
	fake := newFake(t, x, func(f *hidfake.Device) { f.Replay(x) })
	dev := newDevice(t, fake)

	keymap, err := dev.FnKeymap(context.Background())
	if err != nil {
		t.Fatalf("FnKeymap: %v", err)
	}
	if want := (KeyAction{Type: ActionFunc, Params: [3]byte{0, 0, 1}, Raw: [4]byte{13, 0, 0, 1}}); keymap[0] != want {
		t.Errorf("slot 0 = %+v, want %+v (FUNC id 1)", keymap[0], want)
	}
	if want := (KeyAction{Type: ActionKeyboard, Params: [3]byte{0, 0x3B, 0}, Raw: [4]byte{2, 0, 0x3B, 0}}); keymap[2] != want {
		t.Errorf("slot 2 = %+v, want %+v (F2)", keymap[2], want)
	}
	// Fn-disabled Key Slot 1 keeps its base binding on the Fn Layer: the
	// firmware reports what it reports, the decoder never rewrites it.
	if want := (KeyAction{Type: ActionKeyboard, Params: [3]byte{0, 0x3A, 0}, Raw: [4]byte{2, 0, 0x3A, 0}}); keymap[1] != want {
		t.Errorf("slot 1 = %+v, want %+v (Fn-disabled F1 keeps its binding)", keymap[1], want)
	}
	assertSentMatch(t, fake, x)
}

func TestLightingEffectOverFakeDevice(t *testing.T) {
	x := loadFixture(t, "get_led_effect", "nut87")
	fake := newFake(t, x, func(f *hidfake.Device) { f.Replay(x) })
	dev := newDevice(t, fake)

	effect, err := dev.LightingEffect(context.Background())
	if err != nil {
		t.Fatalf("LightingEffect: %v", err)
	}
	want := LightingEffect{
		Mode:           11,
		RGB:            [3]byte{0xFF, 0xFF, 0xFF},
		DriverSetting:  0,
		SecondaryRGB:   [3]byte{0, 0, 0},
		ColorMode:      1,
		Brightness:     6,
		Speed:          3,
		Direction:      0,
		EffectModeType: 0,
		// Factory/unwritten check code — the observed firmware-1.20 state.
		CheckCode:   [2]byte{0, 0},
		CheckCodeOK: true,
	}
	if effect != want {
		t.Errorf("effect:\n got  %+v\n want %+v", effect, want)
	}
	assertSentMatch(t, fake, x)
}

func TestPerKeyRGBOverFakeDevice(t *testing.T) {
	x := loadFixture(t, "get_custom_led_data", "nut87")
	fake := newFake(t, x, func(f *hidfake.Device) { f.Replay(x) })
	dev := newDevice(t, fake)

	rgb, err := dev.PerKeyRGB(context.Background())
	if err != nil {
		t.Fatalf("PerKeyRGB: %v", err)
	}
	// The recorded Device has no custom colors: every entry is zero except
	// the last, where the 00 00 AA 55 block-tail marker lands in the entry's
	// raw bytes (block bytes 508..511).
	if want := (PerKeyLED{}); rgb[0] != want {
		t.Errorf("entry 0 = %+v, want %+v (no custom color)", rgb[0], want)
	}
	if want := (PerKeyLED{}); rgb[13] != want {
		t.Errorf("entry 13 = %+v, want %+v (no custom color)", rgb[13], want)
	}
	if want := (PerKeyLED{G: 0xAA, B: 0x55}); rgb[127] != want {
		t.Errorf("entry 127 = %+v, want %+v (block-tail marker in the raw bytes)", rgb[127], want)
	}
	assertSentMatch(t, fake, x)
}

func TestKeymapMultiChunkReassemblyOverFakeDevice(t *testing.T) {
	// The seed fixtures use 32-byte reports, so a 512-byte keymap read is
	// 22 chunks. What comes back out of the reassembled chunks must be the
	// seed fixture's own payload, byte for byte — the multi-chunk framing
	// golden (its payload is synthesized, see its meta.json).
	x := loadFixture(t, "get_key", "seed-32byte")
	fake := newFake(t, x, func(f *hidfake.Device) { f.Replay(x) })
	dev := newDevice(t, fake)

	keymap, err := dev.Keymap(context.Background())
	if err != nil {
		t.Fatalf("Keymap: %v", err)
	}
	want, err := DecodeKeymap(reassemble(x.Responses, KeymapSize))
	if err != nil {
		t.Fatalf("DecodeKeymap: %v", err)
	}
	if keymap != want {
		t.Errorf("reassembly differs from the fixture payload decode (e.g. slot 0: %+v vs %+v)", keymap[0], want[0])
	}
	// The seed payload still carries the unknown page-type marker at slot 2
	// (2a 01 02 03): unknown markers survive multi-chunk reassembly.
	if want := (KeyAction{Type: ActionUnknown, Params: [3]byte{1, 2, 3}, Raw: [4]byte{42, 1, 2, 3}}); keymap[2] != want {
		t.Errorf("slot 2 = %+v, want %+v (unknown pageType 42 marker)", keymap[2], want)
	}
	if got := len(fake.Sent()); got != len(x.Requests) {
		t.Errorf("sent %d request reports, want %d chunks", got, len(x.Requests))
	}
	assertSentMatch(t, fake, x)
}

// --- write path (ticket 03) ---
//
// SET_* transfers are batched writes: the whole block goes out as one
// chunked transfer (one request report per chunk, one ack per chunk), framed
// exactly like the read path. The expected request reports below are spelled
// out from the frame format of docs/protocol.md §2 (magic, cmd, len, addr u16
// LE, last-packet flag at byte 6); the payload bytes are pinned to literals
// by the codec tests above. The hardware pass records the real exchanges as
// testdata/captures/set_*/ fixtures.

// ack is a minimal SET response report: the cmd echo is all the transfer
// engine matches on (checkAddr is off for writes). The real Device's ack
// shape is recorded in the set_* fixtures.
func ack(cmd byte, addr uint16, length byte) []byte {
	r := make([]byte, 64)
	r[0], r[1], r[2] = ResponseMagic, cmd, length
	r[3], r[4] = byte(addr), byte(addr>>8)
	return r
}

// wantChunk builds one expected request report from the documented frame
// format — independent of Request.Marshal on purpose.
func wantChunk(cmd byte, addr uint16, length byte, last bool, payload []byte) []byte {
	r := make([]byte, 64)
	r[0], r[1], r[2] = RequestMagic, cmd, length
	r[3], r[4] = byte(addr), byte(addr>>8)
	if last {
		r[6] = 1
	}
	copy(r[8:], payload)
	return r
}

// SET_LED_EFFECT is one 16-byte block in one chunk: a single request report
// carrying the whole payload.
func TestSetLightingEffectOverFakeDevice(t *testing.T) {
	effect := LightingEffect{
		Mode: 5, RGB: [3]byte{1, 2, 3}, SecondaryRGB: [3]byte{4, 5, 6},
		ColorMode: 7, Brightness: 4, Speed: 2, Direction: 1, EffectModeType: 3,
	}
	want := wantChunk(CmdSetLEDEffect, 0, LEDEffectSize, true,
		[]byte{5, 1, 2, 3, 0xFF, 4, 5, 6, 7, 4, 2, 1, 3, 0, 0xAA, 0x55})

	fake := hidfake.New(hid.Info{Path: "/dev/hidraw3", ReportLength: 64})
	fake.Script(nil, ack(CmdSetLEDEffect, 0, LEDEffectSize))
	dev := newDevice(t, fake)

	if err := dev.SetLightingEffect(context.Background(), effect); err != nil {
		t.Fatalf("SetLightingEffect: %v", err)
	}
	sent := fake.Sent()
	if len(sent) != 1 {
		t.Fatalf("sent %d request reports, want 1 chunk", len(sent))
	}
	if !bytes.Equal(sent[0], want) {
		t.Errorf("request:\n got  %X\n want %X", sent[0], want)
	}
}

// SET_GAME_MODE is one 56-byte block in one chunk on 64-byte reports.
func TestSetSettingsOverFakeDevice(t *testing.T) {
	s := Settings{GameMode: 1, FnSwitch: 1, SleepTime: 5, KeyDelay: 3,
		ReportRate: ReportRate8K, TopDeadZone: 0.35, NKROSwitch: 1,
		WirelessReportRate: 1000, PowerMode: 3}
	want := wantChunk(CmdSetGameMode, 0, SettingsSize, true, EncodeSettings(s))

	fake := hidfake.New(hid.Info{Path: "/dev/hidraw3", ReportLength: 64})
	fake.Script(nil, ack(CmdSetGameMode, 0, SettingsSize))
	dev := newDevice(t, fake)

	if err := dev.SetSettings(context.Background(), s); err != nil {
		t.Fatalf("SetSettings: %v", err)
	}
	sent := fake.Sent()
	if len(sent) != 1 {
		t.Fatalf("sent %d request reports, want 1 chunk", len(sent))
	}
	if !bytes.Equal(sent[0], want) {
		t.Errorf("request:\n got  %X\n want %X", sent[0], want)
	}
}

// SET_KEY is one batched transfer: the whole 512-byte block chunked at the
// report payload capacity (56 bytes on 64-byte reports → 10 chunks: nine
// full and a tail of 8), each chunk answered by one ack.
func TestSetKeymapBatchedTransferOverFakeDevice(t *testing.T) {
	var km Keymap
	km[0] = KeyAction{Type: ActionKeyboard, Params: [3]byte{0, 0x29, 0}}
	km[127] = KeyAction{Type: ActionDefault, Params: [3]byte{0, 0xAA, 0x55}}
	payload := EncodeKeymap(km)

	fake := hidfake.New(hid.Info{Path: "/dev/hidraw3", ReportLength: 64})
	for i := 0; i < 10; i++ {
		fake.Script(nil, ack(CmdSetKey, uint16(i*56), 56))
	}
	dev := newDevice(t, fake)

	if err := dev.SetKeymap(context.Background(), km); err != nil {
		t.Fatalf("SetKeymap: %v", err)
	}
	sent := fake.Sent()
	if len(sent) != 10 {
		t.Fatalf("sent %d request reports, want 10 chunks", len(sent))
	}
	for i := 0; i < 10; i++ {
		length := 56
		if i == 9 {
			length = 8
		}
		chunk := payload[i*56 : i*56+length]
		want := wantChunk(CmdSetKey, uint16(i*56), byte(length), i == 9, chunk)
		if !bytes.Equal(sent[i], want) {
			t.Errorf("chunk %d:\n got  %X\n want %X", i, sent[i], want)
		}
	}
}

// A dropped SET ack is retried like a dropped read response: the same chunk
// goes out again, then the transfer continues.
func TestSetDroppedAckIsRetried(t *testing.T) {
	effect := LightingEffect{Mode: 5}
	fake := hidfake.New(hid.Info{Path: "/dev/hidraw3", ReportLength: 64})
	fake.ScriptDrop(nil) // first ack is lost
	fake.Script(nil, ack(CmdSetLEDEffect, 0, LEDEffectSize))
	dev := newDevice(t, fake)

	if err := dev.SetLightingEffect(context.Background(), effect); err != nil {
		t.Fatalf("SetLightingEffect: %v", err)
	}
	sent := fake.Sent()
	if len(sent) != 2 {
		t.Fatalf("sent %d request reports, want 2 (retried chunk)", len(sent))
	}
	if !bytes.Equal(sent[0], sent[1]) {
		t.Errorf("retry sent a different chunk:\n %X\n %X", sent[0], sent[1])
	}
}

// The vendor transfer engine gives each command its own timeout
// (docs/protocol.md §2, quoted from the bundle): 500 ms default, 1000 ms for
// SET_KEY, 2000 ms for SET_CUSTOM_LED_DATA and on frameVersion-1 firmware.
// A dropped-ack failure names the timeout it used.
func TestSetKeymapTimeoutIsTheDocumentedOne(t *testing.T) {
	if testing.Short() {
		t.Skip("waits out real transfer timeouts")
	}
	var km Keymap
	fake := hidfake.New(hid.Info{Path: "/dev/hidraw3", ReportLength: 64})
	for i := 0; i <= DefaultMaxRetries; i++ {
		fake.ScriptDrop(nil)
	}
	// Default Options: the documented per-command timeouts must apply.
	dev, err := Open(fake, Options{})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer dev.Close()

	err = dev.SetKeymap(context.Background(), km)
	if err == nil {
		t.Fatal("SetKeymap succeeded against dropped acks, want timeout error")
	}
	if !strings.Contains(err.Error(), "timeout 1s per attempt") {
		t.Errorf("error = %v, want the documented 1s SET_KEY timeout named", err)
	}
}

// The recorded set_* fixtures are the real write exchanges (docs/capture.md
// Method A, 2026-10-05: the Device's own state written back, acked, and read
// back byte-for-byte). The request reports our write path produces must be
// the ones the Device actually accepted — byte for byte — and the payloads
// must be exactly what encode(decode(committed read fixtures)) yields: the
// write path is the read path's inverse, locked against two recordings of
// the same Device state.
func TestSetKeymapOverRecordedFixture(t *testing.T) {
	assertSetMatchesFixture(t, "set_key", "get_key", func(dev *Device, payload []byte) error {
		km, err := DecodeKeymap(payload)
		if err != nil {
			return err
		}
		return dev.SetKeymap(context.Background(), km)
	})
}

func TestSetFnKeymapOverRecordedFixture(t *testing.T) {
	assertSetMatchesFixture(t, "set_fn_key", "get_fn_key", func(dev *Device, payload []byte) error {
		km, err := DecodeKeymap(payload)
		if err != nil {
			return err
		}
		return dev.SetFnKeymap(context.Background(), km)
	})
}

func TestSetLightingEffectOverRecordedFixture(t *testing.T) {
	assertSetMatchesFixture(t, "set_led_effect", "get_led_effect", func(dev *Device, payload []byte) error {
		e, err := DecodeLightingEffect(payload)
		if err != nil {
			return err
		}
		return dev.SetLightingEffect(context.Background(), e)
	})
}

func TestSetPerKeyRGBOverRecordedFixture(t *testing.T) {
	assertSetMatchesFixture(t, "set_custom_led_data", "get_custom_led_data", func(dev *Device, payload []byte) error {
		rgb, err := DecodePerKeyRGB(payload)
		if err != nil {
			return err
		}
		return dev.SetPerKeyRGB(context.Background(), rgb)
	})
}

func TestSetSettingsOverRecordedFixture(t *testing.T) {
	assertSetMatchesFixture(t, "set_game_mode", "get_game_mode", func(dev *Device, payload []byte) error {
		s, err := DecodeSettings(payload)
		if err != nil {
			return err
		}
		return dev.SetSettings(context.Background(), s)
	})
}

// assertSetMatchesFixture drives one SET from the committed read fixture's
// decoded state and demands the recorded write exchange, byte for byte.
func assertSetMatchesFixture(t *testing.T, setDir, getDir string, set func(*Device, []byte) error) {
	t.Helper()
	x := loadFixture(t, setDir, "nut87")
	fake := newFake(t, x, func(f *hidfake.Device) { f.Replay(x) })
	dev := newDevice(t, fake)

	r := loadFixture(t, getDir, "nut87")
	payload := reassemble(r.Responses, reassembledSize(getDir))
	if err := set(dev, payload); err != nil {
		t.Fatalf("SET %s: %v", setDir, err)
	}
	sent := fake.Sent()
	if len(sent) != len(x.Requests) {
		t.Fatalf("sent %d request reports, want %d (the recorded exchange)", len(sent), len(x.Requests))
	}
	for i := range sent {
		if !bytes.Equal(sent[i], x.Requests[i]) {
			t.Errorf("request report %d:\n got  %X\n want %X", i, sent[i], x.Requests[i])
		}
	}
}

// reassembledSize is the block size behind a fixture directory name.
func reassembledSize(dir string) int {
	switch dir {
	case "get_key", "get_fn_key", "get_custom_led_data":
		return 512
	case "get_led_effect":
		return 16
	default:
		return 56
	}
}
