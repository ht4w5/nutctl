package device

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"slices"
	"strings"
)

// The layout table of a Model is data, not code: one JSON file per Model in
// internal/device/layouts/<model>.json, loaded here via go:embed. Adding
// another Model's table is adding a JSON file — no code change.

//go:embed layouts/*.json
var layoutFiles embed.FS

// KeyEntry is one physical key of a Model's layout table: the Key Slot the
// key occupies (CONTEXT.md), its display Name, its HID usage (KeyCode), and
// its EventCode (the event-code id like "Escape"/"KeyA"; the data-file key
// stays "code" — it is the layout-table schema).
type KeyEntry struct {
	Slot      int    `json:"slot"`
	Name      string `json:"name"`
	KeyCode   uint16 `json:"keyCode"`
	EventCode string `json:"code"`
}

// Knob gestures (CONTEXT.md: the Knob has three gestures).
const (
	GestureClockwise        = "clockwise"
	GesturePress            = "press"
	GestureCounterClockwise = "counter-clockwise"
)

// KnobEntry is one Knob gesture of a Model's layout table: the Key Slot the
// gesture occupies, its Gesture (clockwise, press, counter-clockwise) and
// its display Name.
type KnobEntry struct {
	Slot    int    `json:"slot"`
	Gesture string `json:"gesture"`
	Name    string `json:"name"`
}

// Layout is a Model's physical layout table (CONTEXT.md): the keys, the Knob
// gestures, the Fn-disabled Key Slots and the firmware-matrix extraSlots. It
// is loaded from data (internal/device/layouts/<model>.json via LayoutFor),
// never from code.
type Layout struct {
	model      string
	keys       []KeyEntry
	knob       []KnobEntry
	fnDisabled []int
	extra      []int
	nameOf     map[int]string
	knownOf    map[int]bool // keys ∪ knob ∪ extraSlots
}

// LayoutFor returns the layout table for a Model. The table is data: the
// JSON file internal/device/layouts/<model name in lower case>.json. An
// unknown Model is an error naming the missing data file — adding the
// Model's table means adding that file, no code change.
func LayoutFor(m Model) (Layout, error) {
	return layoutForFS(mustSub(layoutFiles, "layouts"), m)
}

// Keys returns the physical keys of the layout table, ordered for display.
func (l Layout) Keys() []KeyEntry { return slices.Clone(l.keys) }

// Knob returns the Knob gesture entries of the layout table: clockwise,
// press, counter-clockwise.
func (l Layout) Knob() []KnobEntry { return slices.Clone(l.knob) }

// FnDisabledSlots returns the Key Slots that cannot be bound on the Fn
// Layer (NUT87: 1..12, the F1–F12 keys).
func (l Layout) FnDisabledSlots() []int { return slices.Clone(l.fnDisabled) }

// ExtraSlots returns the Key Slots the firmware binds by default but the
// Model has no physical key for (shared firmware matrix with sibling
// Models), observed on firmware 1.20. A healthy Device carries Key Actions
// there — default bindings for keys this Model does not physically have —
// so self-check 2 accepts them, distinctly from the physical keys and Knob
// gestures HasSlot reports.
func (l Layout) ExtraSlots() []int { return slices.Clone(l.extra) }

// KnownSlots returns every Key Slot the firmware may legitimately carry a
// Key Action in: the physical keys, the Knob gestures and the extraSlots
// combined. Self-check 2 measures every non-DEFAULT Key Action against this
// set.
func (l Layout) KnownSlots() []int {
	out := make([]int, 0, len(l.knownOf))
	for slot := range l.knownOf {
		out = append(out, slot)
	}
	slices.Sort(out)
	return out
}

// HasKnownSlot reports whether the Key Slot is one the firmware may carry a
// Key Action in: a physical key, a Knob gesture or a firmware-matrix
// extraSlot (see KnownSlots).
func (l Layout) HasKnownSlot(slot int) bool { return l.knownOf[slot] }

// HasSlot reports whether the layout table knows the Key Slot as a physical
// key or a Knob gesture. The firmware-matrix extraSlots are not physical —
// use HasKnownSlot for the full set.
func (l Layout) HasSlot(slot int) bool {
	_, ok := l.nameOf[slot]
	return ok
}

// Name returns the display Name of the Key Slot — a physical key or a Knob
// gesture. ok is false for a Key Slot outside the layout table.
func (l Layout) Name(slot int) (string, bool) {
	name, ok := l.nameOf[slot]
	return name, ok
}

// layoutJSON is the schema of internal/device/layouts/<model>.json.
// extraSlots is optional: Key Slots the firmware binds by default but the
// Model has no physical key for (shared firmware matrix with sibling
// Models), observed on firmware 1.20.
type layoutJSON struct {
	Model           string      `json:"model"`
	Keys            []KeyEntry  `json:"keys"`
	Knob            []KnobEntry `json:"knob"`
	FnDisabledSlots []int       `json:"fnDisabledSlots"`
	ExtraSlots      []int       `json:"extraSlots"`
}

// layoutForFS loads the Model's layout table from the data files in fsys —
// the shared loader behind LayoutFor (and the tests' testdata/ copy of a
// second Model's table).
func layoutForFS(fsys fs.FS, m Model) (Layout, error) {
	file := layoutFileName(m)
	data, err := fs.ReadFile(fsys, file)
	if err != nil {
		return Layout{}, fmt.Errorf(
			"no layout table for Model %q: missing data file internal/device/layouts/%s — a Model's layout table is data; add the file to add the Model",
			m.Name, file)
	}
	var doc layoutJSON
	if err := json.Unmarshal(data, &doc); err != nil {
		return Layout{}, fmt.Errorf("layout data file %s does not decode: %w", file, err)
	}
	if doc.Model != m.Name {
		return Layout{}, fmt.Errorf(
			"layout data file %s declares Model %q, not %q", file, doc.Model, m.Name)
	}
	if len(doc.Keys) == 0 {
		return Layout{}, fmt.Errorf("layout data file %s lists no keys", file)
	}

	l := Layout{
		model:      doc.Model,
		keys:       doc.Keys,
		knob:       doc.Knob,
		fnDisabled: slices.Clone(doc.FnDisabledSlots),
		extra:      slices.Clone(doc.ExtraSlots),
		nameOf:     make(map[int]string, len(doc.Keys)+len(doc.Knob)),
		knownOf:    make(map[int]bool, len(doc.Keys)+len(doc.Knob)+len(doc.ExtraSlots)),
	}
	for _, k := range l.keys {
		if err := l.addName(k.Slot, k.Name); err != nil {
			return Layout{}, fmt.Errorf("layout data file %s: %w", file, err)
		}
	}
	for _, k := range l.knob {
		switch k.Gesture {
		case GestureClockwise, GesturePress, GestureCounterClockwise:
		default:
			return Layout{}, fmt.Errorf(
				"layout data file %s: Knob slot %d has unknown gesture %q", file, k.Slot, k.Gesture)
		}
		if err := l.addName(k.Slot, k.Name); err != nil {
			return Layout{}, fmt.Errorf("layout data file %s: %w", file, err)
		}
	}
	for _, slot := range l.extra {
		if slot < 0 {
			return Layout{}, fmt.Errorf("layout data file %s: negative Key Slot %d", file, slot)
		}
		if l.knownOf[slot] {
			return Layout{}, fmt.Errorf(
				"layout data file %s: extraSlot %d is already a key or Knob slot", file, slot)
		}
		l.knownOf[slot] = true
	}
	return l, nil
}

func (l *Layout) addName(slot int, name string) error {
	if slot < 0 {
		return fmt.Errorf("negative Key Slot %d", slot)
	}
	if _, dup := l.nameOf[slot]; dup {
		return fmt.Errorf("Key Slot %d listed twice", slot)
	}
	l.nameOf[slot] = name
	l.knownOf[slot] = true
	return nil
}

// layoutFileName is the data-file name of a Model's layout table: the Model
// name in lower case, e.g. internal/device/layouts/nut87.json.
func layoutFileName(m Model) string {
	return strings.ToLower(m.Name) + ".json"
}

func mustSub(fsys fs.FS, dir string) fs.FS {
	sub, err := fs.Sub(fsys, dir)
	if err != nil {
		panic(err)
	}
	return sub
}
