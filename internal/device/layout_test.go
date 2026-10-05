package device

import (
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// The NUT87 layout table (internal/device/layouts/nut87.json) decodes to the
// physical Device: 87 keys in the Key Slots 0..108 with the documented gaps,
// 3 Knob gestures on slots 13/14/15, and Fn-disabled Key Slots 1..12.
func TestLayoutNUT87(t *testing.T) {
	l, err := LayoutFor(NUT87)
	if err != nil {
		t.Fatalf("LayoutFor(NUT87): %v", err)
	}

	keys := l.Keys()
	if len(keys) != 87 {
		t.Fatalf("Keys() = %d entries, want 87", len(keys))
	}

	// Key Slot gaps of the 0..108 range (docs/protocol.md §4; the 13/14/15
	// gaps are the Knob gestures' Key Slots).
	slots := make(map[int]bool, len(keys))
	for _, k := range keys {
		if slots[k.Slot] {
			t.Errorf("Key Slot %d listed twice", k.Slot)
		}
		slots[k.Slot] = true
	}
	wantGaps := []int{13, 14, 15, 29, 30, 31, 45, 46, 47, 61, 62, 63, 77, 78, 79, 93, 94, 95, 96, 97, 98, 101}
	var gaps []int
	for slot := 0; slot <= 108; slot++ {
		if !slots[slot] {
			gaps = append(gaps, slot)
		}
	}
	if !reflect.DeepEqual(gaps, wantGaps) {
		t.Errorf("Key Slot gaps = %v, want %v", gaps, wantGaps)
	}

	// Spot-check names and event-code ids against the vendor layout table.
	wantKeys := map[int]KeyEntry{
		0:  {Slot: 0, Name: "Esc", KeyCode: 41, EventCode: "Escape"},
		16: {Slot: 16, Name: "` ~", KeyCode: 53, EventCode: "Backquote"},
		64: {Slot: 64, Name: "L-Shift", KeyCode: 225, EventCode: "ShiftLeft"},
		83: {Slot: 83, Name: "Spacebar", KeyCode: 44, EventCode: "Space"},
		85: {Slot: 85, Name: "Fn", KeyCode: 175, EventCode: "-1"},
		91: {Slot: 91, Name: "→", KeyCode: 79, EventCode: "ArrowRight"},
	}
	for slot, want := range wantKeys {
		got, ok := l.Name(slot)
		if !ok {
			t.Errorf("Name(%d) missing, want %q", slot, want.Name)
			continue
		}
		if got != want.Name {
			t.Errorf("Name(%d) = %q, want %q", slot, got, want.Name)
		}
	}
	for _, k := range keys {
		if want, ok := wantKeys[k.Slot]; ok && k != want {
			t.Errorf("key entry %+v, want %+v", k, want)
		}
	}
}

func TestLayoutNUT87Knob(t *testing.T) {
	l, err := LayoutFor(NUT87)
	if err != nil {
		t.Fatalf("LayoutFor(NUT87): %v", err)
	}

	// The Knob's three gestures occupy Key Slots 13/14/15. Quoting
	// docs/protocol.md §4 verbatim (its "wheel keys" is the vendor bundle's
	// wording for what CONTEXT.md calls Knob gestures): "wheel keys occupy
	// slots 13/14/15 = vol+/mute/vol−".
	want := []KnobEntry{
		{Slot: 13, Gesture: GestureClockwise, Name: "Volume Up"},
		{Slot: 15, Gesture: GesturePress, Name: "Mute"},
		{Slot: 14, Gesture: GestureCounterClockwise, Name: "Volume Down"},
	}
	if got := l.Knob(); !reflect.DeepEqual(got, want) {
		t.Errorf("Knob() = %+v, want %+v", got, want)
	}

	for _, slot := range []int{13, 14, 15} {
		if !l.HasSlot(slot) {
			t.Errorf("HasSlot(%d) = false, want true (Knob slot)", slot)
		}
		if _, ok := l.Name(slot); !ok {
			t.Errorf("Name(%d) missing, want the Knob gesture name", slot)
		}
	}
	// Physical-only: the firmware-matrix extraSlots are not physical keys or
	// Knob gestures (see TestLayoutNUT87ExtraSlots for their place in the
	// KnownSlots set self-check 2 measures against).
	for _, slot := range []int{29, 101, 109} {
		if l.HasSlot(slot) {
			t.Errorf("HasSlot(%d) = true, want false (no physical key or Knob gesture)", slot)
		}
	}
}

// The firmware-matrix extraSlots are data, not code: Key Slots the firmware
// binds by default but the Model has no physical key for (shared matrix with
// sibling Models), observed on firmware 1.20. Self-check 2 accepts Key
// Actions there — a healthy Device carries them on BOTH Layers — while
// HasSlot stays physical-only.
func TestLayoutNUT87ExtraSlots(t *testing.T) {
	l, err := LayoutFor(NUT87)
	if err != nil {
		t.Fatalf("LayoutFor(NUT87): %v", err)
	}

	want := []int{29, 30, 31, 45, 46, 47, 61, 62, 63, 77, 78, 79, 93, 94, 95, 96, 97, 98, 101, 109, 110, 111}
	if got := l.ExtraSlots(); !reflect.DeepEqual(got, want) {
		t.Errorf("ExtraSlots() = %v, want %v", got, want)
	}

	for _, slot := range want {
		if !l.HasKnownSlot(slot) {
			t.Errorf("HasKnownSlot(%d) = false, want true (extraSlot)", slot)
		}
		if l.HasSlot(slot) {
			t.Errorf("HasSlot(%d) = true, want false (extraSlot, not physical)", slot)
		}
		if _, ok := l.Name(slot); ok {
			t.Errorf("Name(%d) = ok, want no name (no physical key there)", slot)
		}
	}

	// KnownSlots = keys ∪ knob ∪ extraSlots: 87 + 3 + 22 = 112 Key Slots
	// the firmware may legitimately carry a Key Action in.
	known := l.KnownSlots()
	if len(known) != 87+3+22 {
		t.Errorf("KnownSlots() = %d entries, want 112: %v", len(known), known)
	}
	if !sort.IntsAreSorted(known) {
		t.Errorf("KnownSlots() = %v, want sorted", known)
	}
	for _, slot := range []int{0, 13, 16, 108, 111} {
		if !l.HasKnownSlot(slot) {
			t.Errorf("HasKnownSlot(%d) = false, want true", slot)
		}
	}
	for _, slot := range []int{112, 120, 127} {
		if l.HasKnownSlot(slot) {
			t.Errorf("HasKnownSlot(%d) = true, want false (nothing the firmware binds)", slot)
		}
	}
}

func TestLayoutNUT87FnDisabledSlots(t *testing.T) {
	l, err := LayoutFor(NUT87)
	if err != nil {
		t.Fatalf("LayoutFor(NUT87): %v", err)
	}
	// F1–F12 (Key Slots 1..12) cannot be bound on the Fn Layer.
	want := []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12}
	if got := l.FnDisabledSlots(); !reflect.DeepEqual(got, want) {
		t.Errorf("FnDisabledSlots() = %v, want %v", got, want)
	}
}

// A Model's layout table is data, not code: the same loader that serves the
// NUT87 accepts another Model's table with no code change. testdata/demo60.json
// plays the part of internal/device/layouts/<model>.json for a second Model.
func TestLayoutTableIsDataNotCode(t *testing.T) {
	m := Model{Name: "Demo60", Connection: ConnectionUSB, ProductName: "Demo60", Supported: true}
	l, err := layoutForFS(os.DirFS("testdata"), m)
	if err != nil {
		t.Fatalf("loading the second Model's layout table: %v", err)
	}

	if got := l.Keys(); !reflect.DeepEqual(got, []KeyEntry{
		{Slot: 0, Name: "Esc", KeyCode: 41, EventCode: "Escape"},
		{Slot: 4, Name: "A", KeyCode: 4, EventCode: "KeyA"},
		{Slot: 9, Name: "Spacebar", KeyCode: 44, EventCode: "Space"},
	}) {
		t.Errorf("Keys() = %+v", got)
	}
	if got := l.Knob(); !reflect.DeepEqual(got, []KnobEntry{
		{Slot: 1, Gesture: GestureClockwise, Name: "Volume Up"},
		{Slot: 2, Gesture: GesturePress, Name: "Play/Pause"},
		{Slot: 3, Gesture: GestureCounterClockwise, Name: "Volume Down"},
	}) {
		t.Errorf("Knob() = %+v", got)
	}
	if got := l.FnDisabledSlots(); !reflect.DeepEqual(got, []int{4}) {
		t.Errorf("FnDisabledSlots() = %v, want [4]", got)
	}
	if !l.HasSlot(2) {
		t.Error("HasSlot(2) = false, want true (Knob slot)")
	}
	if l.HasSlot(7) {
		t.Error("HasSlot(7) = true, want false")
	}
	// extraSlots is optional: a table without it has only physical Key Slots.
	if got := l.ExtraSlots(); len(got) != 0 {
		t.Errorf("ExtraSlots() = %v, want empty (the second Model's table has none)", got)
	}
	if !l.HasKnownSlot(2) || l.HasKnownSlot(7) {
		t.Error("HasKnownSlot tracks the physical Key Slots when there are no extraSlots")
	}
}

// A Model with no layout data file is a clear error naming the missing file:
// the table is data, so "unsupported Model" means "add the JSON file".
func TestLayoutForModelWithoutTable(t *testing.T) {
	_, err := LayoutFor(NUT75) // a known sibling Model with no layout table yet
	if err == nil {
		t.Fatal("LayoutFor(NUT75) = nil, want missing-data-file error")
	}
	msg := err.Error()
	for _, want := range []string{"NUT75", "nut75.json", "missing data file"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q does not mention %q", msg, want)
		}
	}
}
