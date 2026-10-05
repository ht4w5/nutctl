package protocol

import (
	_ "embed"
	"encoding/json"
	"slices"
	"sync"
)

// The effect modes of the Lighting Effect block (CONTEXT.md: "effect mode"):
// which modes exist, what each is called, and which of the block's fields a
// mode actually uses. Like the Key Action catalog (catalog.go) this is
// bundle data, not code — the vendor's own effect table (lighting.json, see
// its source field). A Model's offering is that table filtered by its
// capability set (internal/device): the modes the firmware accepts, in the
// order the vendor lists them.

//go:embed lighting.json
var lightingModesJSON []byte

// CustomLightingMode is the effect mode whose colors come from the Per-Key
// RGB table (CONTEXT.md), not from the effect's own color bytes — the
// bundle's CUSTOM_LIGHTING_MODE constant.
const CustomLightingMode uint8 = 128

// LightingOff is the effect mode that means "backlight off": the bundle's
// lighting switch writes mode 0 when switched off, and its effect table
// carries no entry for it (docs/protocol.md §4). It is a state the Device
// reports, not one the Model's table offers.
const LightingOff uint8 = 0

// LightingMode is one effect mode of the Lighting Effect block: its wire
// Value, its Name, and which of the block's fields the mode animates
// (ShowsSpeed, ShowsDirection, ShowsColor — the bundle's isShowSpeed /
// isShowDirection / isShowColor flags). A field a mode does not show is
// ignored by that mode: an editor may still carry the byte, but must not
// pretend the effect uses it.
type LightingMode struct {
	Value             uint8  `json:"value"`
	Name              string `json:"name"`
	Sort              int    `json:"sort"`
	ShowsSpeed        bool   `json:"isShowSpeed"`
	ShowsDirection    bool   `json:"isShowDirection"`
	DirectionPosition string `json:"directionPosition"`
	ShowsColor        bool   `json:"isShowColor"`
}

// Direction values (docs/protocol.md §4): the direction byte is the arrow
// the bundle's own direction buttons write for it — its left-position pair
// writes 1 for arrow-left and 0 for arrow-right, its top-position pair
// writes 2 for arrow-up and 3 for arrow-down.
const (
	DirectionRight uint8 = 0
	DirectionLeft  uint8 = 1
	DirectionUp    uint8 = 2
	DirectionDown  uint8 = 3
)

// DirectionValues is the pair of directions the mode animates — the two
// values the bundle's direction buttons write for its directionPosition
// ("left" = left/right, "top" = up/down), in the order those buttons stand
// in its lighting pane. nil means the mode has no direction: that byte is
// not its business.
func (m LightingMode) DirectionValues() []uint8 {
	switch m.DirectionPosition {
	case "left":
		return []uint8{DirectionLeft, DirectionRight}
	case "top":
		return []uint8{DirectionUp, DirectionDown}
	}
	return nil
}

// lightingModeDoc is the schema of lighting.json: the bundle's two effect
// tables. "default" is its defalutLightingModeList (sic), "appended" its
// appendLightingModeList.
type lightingModeDoc struct {
	Source   string         `json:"source"`
	Default  []LightingMode `json:"default"`
	Appended []LightingMode `json:"appended"`
}

var (
	lightingOnce     sync.Once
	lightingDefault  []LightingMode
	lightingAppended []LightingMode
	lightingByValue  map[uint8]LightingMode
)

// loadLighting parses the embedded effect table once. Like the catalog, the
// data is compiled in and pinned by tests: a corrupt file is a build-time
// bug, not a broken editor.
func loadLighting() {
	lightingOnce.Do(func() {
		var doc lightingModeDoc
		if err := json.Unmarshal(lightingModesJSON, &doc); err != nil {
			panic("protocol: lighting.json does not decode: " + err.Error())
		}
		lightingDefault, lightingAppended = doc.Default, doc.Appended
		lightingByValue = make(map[uint8]LightingMode, len(doc.Default)+len(doc.Appended))
		for _, e := range slices.Concat(doc.Default, doc.Appended) {
			lightingByValue[e.Value] = e
		}
	})
}

// LightingModes returns the effect modes one Model offers, in the vendor's
// display order: the default effect table plus the appended entries the
// Model's lightingConfig.customEffect selects, sorted by the table's sort
// field (the bundle's own rule — see lighting.json's source). The
// capability set of internal/device carries the result.
func LightingModes(customEffect ...uint8) []LightingMode {
	loadLighting()
	out := slices.Clone(lightingDefault)
	for _, e := range lightingAppended {
		if slices.Contains(customEffect, e.Value) {
			out = append(out, e)
		}
	}
	slices.SortStableFunc(out, func(a, b LightingMode) int { return a.Sort - b.Sort })
	return out
}

// LightingModeFor returns the effect mode the table defines for a wire
// value — its name and its field use. ok is false for a value no table
// names: shown as read, never rewritten, never invented.
func LightingModeFor(value uint8) (LightingMode, bool) {
	loadLighting()
	e, ok := lightingByValue[value]
	return e, ok
}
