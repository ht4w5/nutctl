package protocol

import (
	"reflect"
	"testing"
)

// The Lighting Effect's effect modes are bundle data (ticket 07): the
// vendor's own effect table, its names, and which of the block's fields each
// mode uses. These tests pin the extraction at the protocol seam — the same
// domain-function precedent as the Key Action catalog (catalog_test.go).

// The NUT87's offering is the bundle's default effect table plus the
// appended entries its config selects (lightingConfig.customEffect =
// [23, 24, 25] in 3141-34828-NUT87.ts), in the vendor's display order
// (the table's sort field). Values and names are quoted from the bundle —
// the expected list below is written out from decoded/lighting-C48tIL8G.js,
// not recomputed.
func TestNUT87EffectModes(t *testing.T) {
	modes := LightingModes(23, 24, 25)

	want := []struct {
		value uint8
		name  string
	}{
		{1, "Static Bright"},
		{2, "Single Point On"},
		{3, "Single Point Off"},
		{4, "Starry Sky"},
		{5, "Snowfall"},
		{6, "Floral Competition"},
		{7, "Dynamic Breathing"},
		{8, "Spectrum Cycle"},
		{9, "Color Fountain"},
		{10, "Colorful Interchange"},
		{11, "Flowing with the Waves"},
		{12, "Turning Peaks"},
		{13, "One Touch to Fire"},
		{14, "Two Birds with One Stone"},
		{15, "Ripples Spread"},
		{16, "Endless Flow"},
		{17, "Layered Mountains"},
		{18, "Gentle Rain and Wind"},
		{19, "Back and Forth"},
		{23, "Rainbow Windmill"},
		{24, "Colorful gathering"},
		{25, "Neon shadows"},
		{128, "Custom"},
	}
	if len(modes) != len(want) {
		t.Fatalf("LightingModes(23,24,25) = %d modes, want %d", len(modes), len(want))
	}
	for i, w := range want {
		if modes[i].Value != w.value || modes[i].Name != w.name {
			t.Errorf("mode %d = %d %q, want %d %q", i, modes[i].Value, modes[i].Name, w.value, w.name)
		}
	}
}

// The selection rule is the bundle's own: an appended entry is offered only
// when the Model's customEffect says so. (Expected values written from the
// bundle's appendLightingModeList, not recomputed.)
func TestEffectModeSelectionFollowsCustomEffect(t *testing.T) {
	modes := LightingModes(23)
	var values []uint8
	for _, m := range modes {
		values = append(values, m.Value)
	}
	// Only 23 of the appended list; 24, 25 and the rest stay out.
	want := []uint8{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 23, 128}
	if !reflect.DeepEqual(values, want) {
		t.Errorf("LightingModes(23) values = %v, want %v", values, want)
	}
}

// The Per-Key RGB mode is the bundle's CUSTOM_LIGHTING_MODE constant: its
// colors are the Per-Key RGB table (CONTEXT.md), not the effect's own bytes.
func TestCustomLightingModeIs128(t *testing.T) {
	if CustomLightingMode != 128 {
		t.Fatalf("CustomLightingMode = %d, want 128 (the bundle's CUSTOM_LIGHTING_MODE)", CustomLightingMode)
	}
	m, ok := LightingModeFor(CustomLightingMode)
	if !ok || m.Name != "Custom" {
		t.Errorf("LightingModeFor(128) = %+v, %v; want the Custom mode", m, ok)
	}
}

// Which of the block's fields a mode uses is data, not guesswork: speed and
// color come from the table's isShowSpeed/isShowColor, and direction from the
// pair of arrows the bundle's own direction buttons write (directionPosition
// "left" = 1 left / 0 right, "top" = 2 up / 3 down — the values its
// lighting pane passes to the direction handler).
func TestEffectModeFieldUse(t *testing.T) {
	cases := []struct {
		value     uint8
		speed     bool
		direction []uint8
		color     bool
	}{
		{1, false, nil, true},                  // Static Bright: no speed, no direction
		{2, true, nil, true},                   // Single Point On: speed only
		{6, true, nil, false},                  // Floral Competition: no color
		{11, true, []uint8{1, 0}, true},        // Flowing with the Waves: left/right
		{10, true, []uint8{2, 3}, true},        // Colorful Interchange: up/down
		{23, true, []uint8{1, 0}, false},       // Rainbow Windmill: left/right
		{CustomLightingMode, false, nil, true}, // Custom: the Per-Key RGB table colors it
	}
	for _, tc := range cases {
		m, ok := LightingModeFor(tc.value)
		if !ok {
			t.Fatalf("LightingModeFor(%d) missing", tc.value)
		}
		if m.ShowsSpeed != tc.speed {
			t.Errorf("mode %d ShowsSpeed = %v, want %v", tc.value, m.ShowsSpeed, tc.speed)
		}
		if !reflect.DeepEqual(m.DirectionValues(), tc.direction) {
			t.Errorf("mode %d DirectionValues() = %v, want %v", tc.value, m.DirectionValues(), tc.direction)
		}
		if m.ShowsColor != tc.color {
			t.Errorf("mode %d ShowsColor = %v, want %v", tc.value, m.ShowsColor, tc.color)
		}
	}
}

// The one spelling of an effect mode and a direction: the value always stays
// visible beside its name (naming is exact — a value no table names keeps
// its raw form), and a direction renders as the arrow the bundle's buttons
// show for it.
func TestEffectModeAndDirectionText(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{LightingModeText(0), "0 off"}, // the bundle's lighting switch writes mode 0
		{LightingModeText(11), "11 Flowing with the Waves"},
		{LightingModeText(128), "128 Custom"},
		{LightingModeText(20), "20 (unknown mode)"},
		{DirectionText(0), "right"},
		{DirectionText(1), "left"},
		{DirectionText(2), "up"},
		{DirectionText(3), "down"},
		{DirectionText(7), "unknown(7)"},
		{RangeText(6, 1, 6), "6 (range 1-6)"},
	} {
		if tc.in != tc.want {
			t.Errorf("text = %q, want %q", tc.in, tc.want)
		}
	}
}

// A value the table does not carry is never named, never a crash.
func TestLightingModeForUnknown(t *testing.T) {
	if m, ok := LightingModeFor(20); ok {
		t.Errorf("LightingModeFor(20) = %+v, want unknown", m)
	}
}
