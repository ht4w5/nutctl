package protocol

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ht4w5/nutctl/internal/hid"
	"github.com/ht4w5/nutctl/internal/hidfake"
)

// SET_FACTORY_RESET is one fire-and-forget output report, byte-exact with the
// vendor bundle's factoryReset (rs in decoded/layout-classic-DSv6_q0d.js):
// xn(SET_FACTORY_RESET, scope, 0, undefined, reportCount) builds
// `AA 0F <scope> 00 00 00 00 00` zero-padded to the report length — the scope
// rides in header byte 2 (the "len" position of other commands), there is no
// payload, and no response is awaited (the bundle sleeps 100 ms and returns).
// The wire values of the scopes are the bundle's FACTORY_RESET_TYPE table.
func TestFactoryResetSendsTheBundlesFrame(t *testing.T) {
	for _, tc := range []struct {
		name      string
		scope     ResetScope
		wireByte  byte
		reportLen int
	}{
		{"keys", ResetKeys, 1, 64},
		{"lighting", ResetLighting, 2, 64},
		{"macros", ResetMacros, 4, 64},
		{"all", ResetAll, 255, 64},
		// Report length comes from the HID descriptor, never hardcoded:
		// the same frame shape must pad to a 32-byte report too.
		{"keys on a 32-byte device", ResetKeys, 1, 32},
	} {
		t.Run(tc.name, func(t *testing.T) {
			want := make([]byte, tc.reportLen)
			want[0] = RequestMagic
			want[1] = CmdSetFactoryReset
			want[2] = tc.wireByte

			fake := hidfake.New(hid.Info{Path: "/dev/hidraw3", ReportLength: tc.reportLen})
			fake.Script(want) // the fake answers nothing and rejects any other frame
			dev := newDevice(t, fake)

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := dev.FactoryReset(ctx, tc.scope); err != nil {
				t.Fatalf("FactoryReset(%s): %v", tc.scope, err)
			}

			sent := fake.Sent()
			if len(sent) != 1 {
				t.Fatalf("sent %d reports, want exactly 1 (fire-and-forget)", len(sent))
			}
			if !bytes.Equal(sent[0], want) {
				t.Errorf("reset report:\n got  %X\n want %X", sent[0], want)
			}
		})
	}
}

// The scope names the confirmation prompt and the read-back report print
// (one spelling, protocol/text.go): the CLI surface word, the firmware's own
// sub-command name (docs/protocol.md §3), and what the scope destroys — the
// words the typed confirmation names (spec user story 27).
func TestResetScopeText(t *testing.T) {
	for _, tc := range []struct {
		scope    ResetScope
		name     string
		firmware string
		destroys string
	}{
		{ResetKeys, "keys", "KEY_RESET", "both Layers' key bindings"},
		{ResetLighting, "lighting", "LIGHTING_RESET", "the Lighting Effect and Per-Key RGB"},
		{ResetMacros, "macros", "MACRO_RESET", "all Macros"},
		{ResetAll, "all", "RESET_ALL", "ALL keyboard configuration"},
	} {
		if got := tc.scope.String(); got != tc.name {
			t.Errorf("ResetScope(%d).String() = %q, want %q", tc.scope, got, tc.name)
		}
		if got := tc.scope.FirmwareName(); got != tc.firmware {
			t.Errorf("ResetScope(%s).FirmwareName() = %q, want %q", tc.name, got, tc.firmware)
		}
		if d := tc.scope.Destroys(); !strings.Contains(d, tc.destroys) {
			t.Errorf("ResetScope(%s).Destroys() = %q, want it to name %q", tc.name, d, tc.destroys)
		}
	}
	if got := ResetScope(99).String(); got != "unknown(99)" {
		t.Errorf("unknown scope String() = %q, want %q", got, "unknown(99)")
	}
}
