package device

import (
	"testing"

	"github.com/ht4w5/nutctl/internal/protocol"
)

// Rebase is the merge behind every apply of local edits: edits keep their
// values, untouched regions follow the Device. It is behavior every edit
// screen depends on (an untouched field is never written back stale), so it
// is pinned here alongside the other device-model domain functions
// (RunChecks, Identify) — the wire-level proof lives in the TUI tests.
func TestRebaseKeepsEditsAndFollowsTheDevice(t *testing.T) {
	base := State{}
	base.Settings.ReportRate = protocol.ReportRate8K
	base.Settings.KeyDelay = 3
	base.Base[5] = protocol.KeyAction{Raw: [4]byte{2, 0, 4, 0}}
	base.Base[6] = protocol.KeyAction{Raw: [4]byte{2, 0, 5, 0}}

	fresh := base // the Device moved on where the user never looked
	fresh.Settings.KeyDelay = 7
	fresh.Base[6] = protocol.KeyAction{Raw: [4]byte{2, 0, 6, 0}}

	edited := base // the user's local edits
	edited.Settings.ReportRate = protocol.ReportRate1K
	edited.Base[5] = protocol.KeyAction{Raw: [4]byte{2, 0, 7, 0}}

	got := Rebase(base, fresh, edited)

	if got.Settings.ReportRate != protocol.ReportRate1K {
		t.Errorf("an edited field must keep the user's value, got %v", got.Settings.ReportRate)
	}
	if got.Settings.KeyDelay != 7 {
		t.Errorf("an untouched field must follow the Device, got %d", got.Settings.KeyDelay)
	}
	if got.Base[5].Raw != [4]byte{2, 0, 7, 0} {
		t.Errorf("an edited Key Slot must keep the user's action, got %v", got.Base[5].Raw)
	}
	if got.Base[6].Raw != [4]byte{2, 0, 6, 0} {
		t.Errorf("an untouched Key Slot must follow the Device, got %v", got.Base[6].Raw)
	}
}
