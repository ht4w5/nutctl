package protocol

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"sync"
)

// The Key Action catalog: what a Key Slot can be bound to (CONTEXT.md: a
// keyboard key, consumer key, mouse button, or Function) and what each
// binding is called. It is the picker's offering and the one naming of Key
// Actions in KeyActionText — the wire encodings are the vendor bundle's own
// lo() writer (docs/protocol.md §4), the names its picker tables. The data
// lives in catalog.json (extracted from the bundle; see its source field),
// not in code: a firmware that grows its Function table edits data.

//go:embed catalog.json
var catalogJSON []byte

// ActionKind is one kind of Key Action — CONTEXT.md's own enumeration of
// what a Key Slot is bound to ("a keyboard key, consumer key, mouse button,
// Macro, Advanced Key, or Function"), minus the kinds the catalog does not
// carry yet. String is that vocabulary: the one spelling of the picker's
// kinds, in KeyActionText ("keyboard key \"Esc\"") and in the picker alike.
type ActionKind int

const (
	KindKeyboard ActionKind = iota
	KindConsumer
	KindMouse
	KindFunction
)

// kindWords names each kind as CONTEXT.md names it.
var kindWords = [...]string{
	KindKeyboard: "keyboard key",
	KindConsumer: "consumer key",
	KindMouse:    "mouse button",
	KindFunction: "Function",
}

// String is the kind's name in CONTEXT.md's Key Action vocabulary.
func (k ActionKind) String() string {
	if k >= 0 && int(k) < len(kindWords) {
		return kindWords[k]
	}
	return fmt.Sprintf("kind(%d)", int(k))
}

// ActionKinds lists every kind the catalog carries, in picker order.
var ActionKinds = []ActionKind{KindKeyboard, KindConsumer, KindMouse, KindFunction}

// ActionChoice is one bindable Key Action: the Name the picker shows and the
// Key Action it binds.
type ActionChoice struct {
	Name   string
	Action KeyAction
}

// Catalog returns every bindable Key Action of a kind, in offering order.
func Catalog(k ActionKind) []ActionChoice {
	c := loadCatalog()
	if k < 0 || int(k) >= len(c.groups) {
		return nil
	}
	return c.groups[k]
}

// KeyboardKey binds a keyboard key by its HID keycode (docs/protocol.md §4
// KEYBOARD page: the keycode travels in param2, param1 and param3 are 0 —
// the bundle's lo() writer).
func KeyboardKey(keyCode uint16) KeyAction {
	return action(ActionKeyboard, 0, byte(keyCode), 0)
}

// ConsumerKey binds a consumer/media key by its HID consumer usage id
// (docs/protocol.md §4 CONSUMER_KEY page: usage u16 LE in param1..2).
func ConsumerKey(usage uint16) KeyAction {
	return action(ActionConsumer, byte(usage), byte(usage>>8), 0)
}

// MouseButton binds a mouse button or wheel action: the axis byte (1 =
// buttons, 3 = wheel) and the value byte of docs/protocol.md §4's MOUSE row
// (button bit, or wheel delta as its wire byte).
func MouseButton(axis, value byte) KeyAction {
	return action(ActionMouse, axis, value, 0)
}

// FuncKey binds a firmware Function by its 24-bit id (CONTEXT.md;
// docs/protocol.md §4 FUNC page: the id travels big-endian in param1..3).
func FuncKey(id uint32) KeyAction {
	return action(ActionFunc, byte(id>>16), byte(id>>8), byte(id))
}

// action builds a Key Action from its page type and params, with the raw
// bytes filled in from the same values (Raw is the wire truth).
func action(t KeyActionType, p1, p2, p3 byte) KeyAction {
	return KeyAction{
		Type:   t,
		Params: [3]byte{p1, p2, p3},
		Raw:    [4]byte{byte(t), p1, p2, p3},
	}
}

// KeyActionName names a Key Action from the catalog — the name the picker
// offers it under ("Esc", "Volume +", "Restore Factory Settings") — or ""
// when the catalog carries no name for these exact wire bytes. A Key Action
// is named ONLY when all four wire bytes match the catalog entry exactly:
// names never collide, so a diff rendered from names never hides a byte
// difference. Everything else renders as its raw form (KeyActionText).
func KeyActionName(a KeyAction) string {
	c := loadCatalog()
	if name, ok := c.byRaw[a.Raw]; ok {
		return name
	}
	return ""
}

// kindOf names which ActionKind a page type binds — the kind a named Key
// Action renders under.
func kindOf(t KeyActionType) ActionKind {
	switch t {
	case ActionKeyboard:
		return KindKeyboard
	case ActionConsumer:
		return KindConsumer
	case ActionMouse:
		return KindMouse
	default:
		return KindFunction
	}
}

// catalog is the parsed Key Action catalog, one offering per ActionKind,
// with each entry indexed by its wire bytes (the lookup KeyActionName
// matches exactly against).
type catalog struct {
	groups [len(kindWords)][]ActionChoice
	byRaw  map[[4]byte]string
}

// catalogDoc is the schema of catalog.json: four kinds of named entries,
// each carrying the exact wire values its Key Action encodes (the bundle's
// lo() writer, docs/protocol.md §4).
type catalogDoc struct {
	Source   string `json:"source"`
	Keyboard []struct {
		Name    string `json:"name"`
		KeyCode uint16 `json:"keyCode"`
	} `json:"keyboard"`
	Consumer []struct {
		Name  string `json:"name"`
		Usage uint16 `json:"usage"`
	} `json:"consumer"`
	Mouse []struct {
		Name   string `json:"name"`
		Param1 byte   `json:"param1"`
		Value  byte   `json:"value"`
	} `json:"mouse"`
	Functions []struct {
		ID   uint32 `json:"id"`
		Name string `json:"name"`
	} `json:"functions"`
}

var (
	catalogOnce sync.Once
	catalogAll  catalog
)

// loadCatalog parses the embedded catalog once. The data is compiled into
// the binary and pinned by tests; a corrupt file is a build-time bug, so
// load failure panics here rather than surfacing as a broken picker.
func loadCatalog() catalog {
	catalogOnce.Do(func() {
		var doc catalogDoc
		if err := json.Unmarshal(catalogJSON, &doc); err != nil {
			panic("protocol: catalog.json does not decode: " + err.Error())
		}
		c := catalog{byRaw: make(map[[4]byte]string)}
		for _, e := range doc.Keyboard {
			a := KeyboardKey(e.KeyCode)
			c.groups[KindKeyboard] = append(c.groups[KindKeyboard], ActionChoice{Name: e.Name, Action: a})
		}
		for _, e := range doc.Consumer {
			a := ConsumerKey(e.Usage)
			c.groups[KindConsumer] = append(c.groups[KindConsumer], ActionChoice{Name: e.Name, Action: a})
		}
		for _, e := range doc.Mouse {
			a := MouseButton(e.Param1, e.Value)
			c.groups[KindMouse] = append(c.groups[KindMouse], ActionChoice{Name: e.Name, Action: a})
		}
		for _, e := range doc.Functions {
			a := FuncKey(e.ID)
			c.groups[KindFunction] = append(c.groups[KindFunction], ActionChoice{Name: e.Name, Action: a})
		}
		for _, group := range c.groups {
			for _, ch := range group {
				c.byRaw[ch.Action.Raw] = ch.Name
			}
		}
		catalogAll = c
	})
	return catalogAll
}
