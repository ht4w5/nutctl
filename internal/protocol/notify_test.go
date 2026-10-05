package protocol

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/ht4w5/nutctl/internal/hidfake"
)

// The Device pushes notify traffic unsolicited — the vendor bundle only ever
// listens for it (decoded/layout-classic-DSv6_q0d.js): `startDeviceStateListener`
// matches `55 FA <type>` (GET_DEVICE_NOTIFY) and reads byte 3 as a state flag
// for type 6; `start24GDisconnectListener` matches `55 FC 04`,
// `startResetListener` `55 FC 05`, `start24GSleepListener` `55 FC 06`
// (GET_24G_DISCONNECT_NOTIFY subtypes), and `start24GWakeListener` matches the
// out-of-band `A6 FF 01` wake report. `nutctl watch` decodes exactly these.
func TestParseNotifyRecognizesDeviceNotifyTraffic(t *testing.T) {
	for _, tc := range []struct {
		name   string
		report []byte
		want   NotifyKind
		typ    byte
		text   string
	}{
		{"device notify", []byte{0x55, 250, 6, 1, 0, 0, 0, 0, 0xFF}, NotifyDeviceNotify, 6, "device notify (type 6)"},
		{"2.4G disconnect", []byte{0x55, 252, 4, 0, 0, 0, 0, 0}, Notify24GDisconnect, 4, "2.4G disconnect"},
		{"device reset", []byte{0x55, 252, 5, 0, 0, 0, 0, 0}, NotifyDeviceReset, 5, "device reset"},
		{"2.4G sleep", []byte{0x55, 252, 6, 0, 0, 0, 0, 0}, Notify24GSleep, 6, "2.4G sleep"},
		{"2.4G wake", []byte{0xA6, 0xFF, 0x01}, Notify24GWake, 0, "2.4G wake"},
		{"unnamed 2.4G subtype keeps its bytes", []byte{0x55, 252, 7, 0, 0, 0, 0, 0}, Notify24GOther, 7, "2.4G notify (type 7)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n, ok := ParseNotify(tc.report)
			if !ok {
				t.Fatalf("ParseNotify(%X) = _, false, want true", tc.report)
			}
			if n.Kind != tc.want || n.Type != tc.typ {
				t.Errorf("ParseNotify(%X) = kind %v type %d, want kind %v type %d",
					tc.report, n.Kind, n.Type, tc.want, tc.typ)
			}
			if got := NotifyText(n); got != tc.text {
				t.Errorf("NotifyText = %q, want %q", got, tc.text)
			}
		})
	}
}

// Reports that are not notify traffic are refused: response frames to
// requests, garbage, short reports and the wake frame's near misses stay out
// of the stream (the transfer layer retries on those).
func TestParseNotifyRefusesOtherReports(t *testing.T) {
	for _, report := range [][]byte{
		{0x55, CmdGetKey, 0x04, 0, 0, 0, 0, 0}, // an ordinary response frame
		{0x55, 253, 4, 0, 0, 0, 0, 0},          // an unknown command
		{0xA6, 0xFF, 0x02},                     // wake magic, wrong type
		{0xA6, 0xFE, 0x01},                     // near-miss wake report
		{0x01, 0x02, 0x03},                     // garbage
		{0x55, 252},                            // short report
		{},
	} {
		if n, ok := ParseNotify(report); ok {
			t.Errorf("ParseNotify(%X) = %+v, want refused", report, n)
		}
	}
}

// Notifications delivers the input reports no transfer consumed — the `nutctl
// watch` stream. Notify traffic injected by the Device arrives there while
// ordinary transfers are undisturbed (docs/protocol.md §2: notify frames are
// never answers to a request).
func TestNotificationsForwardUnsolicitedReports(t *testing.T) {
	x := loadFixture(t, "get_device_info", "nut87")
	fake := newFake(t, x, func(f *hidfake.Device) { f.Replay(x) })
	notify := []byte{0x55, 252, 4, 0, 0, 0, 0, 0}
	garbage := []byte{0x01, 0x02, 0x03}
	fake.Inject(notify, garbage)

	dev := newDevice(t, fake)
	if _, err := dev.Info(context.Background()); err != nil {
		t.Fatalf("Info: %v", err)
	}

	for i, want := range [][]byte{notify, garbage} {
		select {
		case got := <-dev.Notifications():
			if !bytes.Equal(got, want) {
				t.Errorf("report %d = %X, want %X", i, got, want)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("report %d never arrived on Notifications()", i)
		}
	}
}
