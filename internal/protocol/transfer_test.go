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
