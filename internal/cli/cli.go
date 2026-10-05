// Package cli implements the nutctl command surface. Every command runs
// against the hid.Enumerator seam, so the CLI is testable end-to-end with the
// fake Device and committed fixtures — no hardware, no special build.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/ht4w5/nutctl/internal/device"
	"github.com/ht4w5/nutctl/internal/hid"
	"github.com/ht4w5/nutctl/internal/protocol"
	"github.com/ht4w5/nutctl/internal/tui"
)

// Deps carries the seams the CLI runs against.
type Deps struct {
	Devices hid.Enumerator
	Stdin   io.Reader // the write gate's golden-read prompt reads it
	Stdout  io.Writer
	Stderr  io.Writer
}

const usageText = `nutctl — configure the WEIKAV NUT87 keyboard

usage:
  nutctl                                    open the TUI (Device · Keys · Lighting · Settings)
  nutctl list [--json]                       enumerate connected Devices and identify their Model
  nutctl info [--json] [--device P]          print Model, firmware version, Report Rate and Device facts
  nutctl get keymap [--layer base|fn] [--json] [--device P]
                                             list every Key Slot with its current Key Action
  nutctl get lighting [--json] [--device P]  print the Lighting Effect and Per-Key RGB
  nutctl get settings [--json] [--device P]  print Settings (Report Rate, key delay, sleep, …)
  nutctl save <file> [--device P]            write the Device's current state to a State File
  nutctl load <file> [--device P] [--i-know-what-im-doing]
                                             apply a State File to the Device (write-gated)

Bare nutctl opens the TUI (ADR-0004); the commands above are its scriptable
side (see PLAN.md).
`

// Run executes one CLI invocation and returns the process exit code.
func Run(args []string, deps Deps) int {
	if deps.Stdout == nil {
		deps.Stdout = io.Discard
	}
	if deps.Stderr == nil {
		deps.Stderr = io.Discard
	}
	if len(args) == 0 {
		// Bare nutctl opens the TUI (spec user story 2): interactive
		// configuration is the default experience (ADR-0004).
		return tui.Run(tui.Deps{
			Devices: deps.Devices,
			Stdin:   deps.Stdin,
			Stdout:  deps.Stdout,
			Stderr:  deps.Stderr,
		})
	}
	switch args[0] {
	case "list":
		return runList(args[1:], deps)
	case "info":
		return runInfo(args[1:], deps)
	case "get":
		return runGet(args[1:], deps)
	case "save":
		return runSave(args[1:], deps)
	case "load":
		return runLoad(args[1:], deps)
	case "help", "--help", "-h":
		fmt.Fprint(deps.Stdout, usageText)
		return 0
	default:
		fmt.Fprintf(deps.Stderr, "nutctl: unknown command %q\n\n%s", args[0], usageText)
		return 2
	}
}

// entry statuses in --json output.
const (
	statusOK         = "ok"
	statusWrongModel = "wrong-model"
	statusUnknown    = "unknown"
	statusProbeFail  = "probe-failed"
)

// listEntry is one row of `nutctl list`.
type listEntry struct {
	Path         string `json:"path"`
	USBVendorID  uint16 `json:"usbVendorId"`
	USBProductID uint16 `json:"usbProductId"`
	ProductName  string `json:"productName"`
	Manufacturer string `json:"manufacturer"`
	Model        string `json:"model"`
	Supported    bool   `json:"supported"`
	Firmware     string `json:"firmware"`
	Status       string `json:"status"`
	Detail       string `json:"detail"`
}

func runList(args []string, deps Deps) int {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	jsonOut := fs.Bool("json", false, "stable JSON output")
	if err := fs.Parse(args); err != nil {
		return usageError(deps, err)
	}
	if fs.NArg() > 0 {
		return usageError(deps, fmt.Errorf("unexpected argument %q", fs.Arg(0)))
	}

	infos, err := deps.Devices.Enumerate()
	if err != nil {
		return fail(deps, fmt.Errorf("enumerate devices: %w", err))
	}

	entries := []listEntry{}
	for _, info := range infos {
		if !device.IsCandidate(info) {
			continue
		}
		entries = append(entries, probe(deps, info))
	}

	if *jsonOut {
		return printJSON(deps, entries)
	}

	if len(entries) == 0 {
		fmt.Fprintln(deps.Stdout, "no candidate devices found")
		return 0
	}
	w := tabwriter.NewWriter(deps.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "PATH\tUSB\tPRODUCT\tMODEL\tFIRMWARE\tSTATUS")
	for _, e := range entries {
		firmware := e.Firmware
		if firmware == "" {
			firmware = "-"
		}
		model := e.Model
		if model == "" {
			model = "-"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
			e.Path, fmt.Sprintf("%04x:%04x", e.USBVendorID, e.USBProductID),
			e.ProductName, model, firmware, humanStatus(e))
	}
	w.Flush()
	return 0
}

func humanStatus(e listEntry) string {
	switch e.Status {
	case statusOK:
		return "ok"
	case statusProbeFail:
		return "probe failed: " + e.Detail
	case statusWrongModel:
		if e.Model != "" {
			// known sibling Model
			return "wrong Model (not supported)"
		}
		return e.Detail
	default:
		return e.Detail
	}
}

// probe identifies the Model from the Device Identity and, for supported
// Models, proves the protocol speaks by reading GET_DEVICE_INFO (Session).
// It never opens a Device it does not support.
func probe(deps Deps, info hid.Info) listEntry {
	e := listEntry{
		Path:         info.Path,
		USBVendorID:  info.VendorID,
		USBProductID: info.ProductID,
		ProductName:  info.ProductName,
		Manufacturer: info.Manufacturer,
	}

	m, err := device.Identify(device.IdentityFromUSB(info))
	if err != nil {
		e.Status = statusUnknown
		var wrong *device.WrongModelError
		if errors.As(err, &wrong) {
			e.Status = statusWrongModel
		}
		e.Detail = err.Error()
		return e
	}
	e.Model = m.Name
	e.Supported = m.Supported
	if err := device.CheckSupported(m); err != nil {
		e.Status = statusWrongModel
		e.Detail = err.Error()
		return e
	}

	s, err := device.OpenInfo(deps.Devices, info, m)
	if err != nil {
		e.Status = statusProbeFail
		e.Detail = err.Error()
		return e
	}
	defer s.Close()
	e.Firmware = s.DeviceInfo.Version
	e.Status = statusOK
	return e
}

func runInfo(args []string, deps Deps) int {
	fs := flag.NewFlagSet("info", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	jsonOut := fs.Bool("json", false, "stable JSON output")
	selector := fs.String("device", "", "hidraw path of the Device to use")
	if err := fs.Parse(args); err != nil {
		return usageError(deps, err)
	}
	if fs.NArg() > 0 {
		return usageError(deps, fmt.Errorf("unexpected argument %q", fs.Arg(0)))
	}

	s, err := device.Open(deps.Devices, *selector)
	if err != nil {
		return fail(deps, err)
	}
	defer s.Close()

	settings, err := s.Dev.Settings(context.Background())
	if err != nil {
		return fail(deps, fmt.Errorf("probe %s: %w", s.Info.Path, err))
	}

	view := infoView{info: s.Info, model: s.Model, id: device.IdentityFromUSB(s.Info), di: s.DeviceInfo, settings: settings}
	if *jsonOut {
		return printInfoJSON(deps, view)
	}
	printInfoHuman(deps, view)
	return 0
}

// selectDevice, openProbe and readInfo are spelled once in the device layer
// (device.Select, device.OpenInfo, device.Session) and shared with the TUI.

// infoView is everything `nutctl info` reports about one Device.
type infoView struct {
	info     hid.Info
	model    device.Model
	id       device.Identity
	di       protocol.DeviceInfo
	settings protocol.Settings
}

// infoJSON is the stable --json schema of `nutctl info`.
type infoJSON struct {
	Model          string       `json:"model"`
	Connection     string       `json:"connection"`
	Path           string       `json:"path"`
	Identity       identityJSON `json:"identity"`
	Firmware       firmwareJSON `json:"firmware"`
	ReportRate     string       `json:"reportRate"`
	BatteryLevel   uint8        `json:"batteryLevel"`
	ChargeStatus   uint8        `json:"chargeStatus"`
	WorkMode       uint8        `json:"workMode"`
	RomSize        uint8        `json:"romSize"`
	MacroSpaceSize uint16       `json:"macroSpaceSize"`
}

type identityJSON struct {
	USBVendorID          uint16 `json:"usbVendorId"`
	USBProductID         uint16 `json:"usbProductId"`
	ProductName          string `json:"productName"`
	Manufacturer         string `json:"manufacturer"`
	FirmwareVendorID     uint16 `json:"firmwareVendorId"`
	FirmwareProductID    uint16 `json:"firmwareProductId"`
	FirmwareManufacturer uint16 `json:"firmwareManufacturer"`
	FirmwareProduct      uint16 `json:"firmwareProduct"`
}

type firmwareJSON struct {
	Version         string `json:"version"`
	FrameVersion    uint8  `json:"frameVersion"`
	LightingVersion uint8  `json:"lightingVersion"`
	Status          uint8  `json:"status"`
	StatusText      string `json:"statusText"`
}

func printInfoJSON(deps Deps, v infoView) int {
	return printJSON(deps, infoJSON{
		Model:      v.model.Name,
		Connection: string(v.model.Connection),
		Path:       v.info.Path,
		Identity: identityJSON{
			USBVendorID:          v.id.VendorID,
			USBProductID:         v.id.ProductID,
			ProductName:          v.id.ProductName,
			Manufacturer:         v.info.Manufacturer,
			FirmwareVendorID:     v.di.VID,
			FirmwareProductID:    v.di.PID,
			FirmwareManufacturer: v.di.Manufacturer,
			FirmwareProduct:      v.di.Product,
		},
		Firmware: firmwareJSON{
			Version:         v.di.Version,
			FrameVersion:    v.di.FrameVersion,
			LightingVersion: v.di.LightingVersion,
			Status:          v.di.FirmwareStatus,
			StatusText:      protocol.FirmwareStatusText(v.di.FirmwareStatus),
		},
		ReportRate:     v.settings.ReportRate.String(),
		BatteryLevel:   v.di.BatteryLevel,
		ChargeStatus:   v.di.ChargeStatus,
		WorkMode:       v.di.WorkMode,
		RomSize:        v.di.RomSize,
		MacroSpaceSize: v.di.MacroSpaceSize,
	})
}

func printInfoHuman(deps Deps, v infoView) {
	fmt.Fprintf(deps.Stdout, "Model:           %s\n", v.model.Name)
	fmt.Fprintf(deps.Stdout, "Connection:      %s\n", v.model.Connection)
	fmt.Fprintf(deps.Stdout, "Path:            %s\n", v.info.Path)
	fmt.Fprintf(deps.Stdout, "Device Identity: %s %q (manufacturer %d, product %d)\n",
		v.id.USBID(), v.id.ProductName, v.di.Manufacturer, v.di.Product)
	fmt.Fprintf(deps.Stdout, "Firmware:        %s\n", v.di.Version)
	fmt.Fprintf(deps.Stdout, "Firmware status: %s\n", protocol.FirmwareStatusText(v.di.FirmwareStatus))
	fmt.Fprintf(deps.Stdout, "Report Rate:     %s\n", v.settings.ReportRate)
	fmt.Fprintf(deps.Stdout, "Battery:         %d%% (charge status %d)\n", v.di.BatteryLevel, v.di.ChargeStatus)
	fmt.Fprintf(deps.Stdout, "Work mode:       %d\n", v.di.WorkMode)
	fmt.Fprintf(deps.Stdout, "Macro space:     %d bytes\n", v.di.MacroSpaceSize)
	fmt.Fprintf(deps.Stdout, "ROM size:        %d\n", v.di.RomSize)
}

// --- `nutctl get` ---
//
// Every `get` subcommand makes ONE full read pass over the Device (base
// keymap, fn keymap, Lighting Effect, Per-Key RGB, Settings) and runs
// device.RunChecks ONCE over it (ADR-0003). A Device that fails its
// self-checks still shows the requested view on stdout — the checks gate
// WRITES (ADR-0003), reads stay available, and the state display is exactly
// what a failing check needs for diagnosis — while every
// "self-check failed: …" line goes to stderr and the exit code is 1.

// deviceState is one full read pass over a Device plus the Model it came
// from: every block the v0 read surface shows (docs/protocol.md §3 init
// sequence), as the device layer's State.
type deviceState struct {
	model device.Model
	device.State
}

// checkedRead makes ONE full read pass and runs device.RunChecks ONCE over it
// (session.ReadChecked — ADR-0003) — the shared verification step of every
// get/save/load. A failed self-check comes back as *device.CheckError
// ALONGSIDE the state: the checks gate WRITES, not reads, and the state is
// exactly what a failing check needs for diagnosis.
func checkedRead(s *device.Session) (deviceState, error) {
	st, err := s.ReadChecked()
	return deviceState{model: s.Model, State: st}, err
}

// readFullState opens the selected Device and makes ONE checked full read
// pass. The error may be a *device.CheckError alongside the state.
func readFullState(deps Deps, selector string) (deviceState, error) {
	s, err := device.Open(deps.Devices, selector)
	if err != nil {
		return deviceState{}, err
	}
	defer s.Close()
	return checkedRead(s)
}

// runGet dispatches the `get` subcommands: keymap [--layer base|fn],
// lighting, settings — each with stable --json output. The one switch below
// both validates the subcommand and picks its printer; bad subcommands and
// bad flag values are usage errors (exit 2); `get macros` is deferred to
// v0.1 (the spec's Feature slicing defers Macros).
func runGet(args []string, deps Deps) int {
	if len(args) == 0 {
		return usageError(deps, errors.New("get needs a subcommand: keymap, lighting or settings"))
	}
	sub := args[0]
	var print func(deps Deps, st deviceState, jsonOut bool, layer string) int
	layered := false // only `get keymap` takes --layer
	switch sub {
	case "keymap":
		layered = true
		print = func(deps Deps, st deviceState, jsonOut bool, layer string) int {
			if jsonOut {
				return printKeymapJSON(deps, st, layer)
			}
			printKeymapHuman(deps, st, layer)
			return 0
		}
	case "lighting":
		print = func(deps Deps, st deviceState, jsonOut bool, _ string) int {
			if jsonOut {
				return printLightingJSON(deps, st)
			}
			printLightingHuman(deps, st)
			return 0
		}
	case "settings":
		print = func(deps Deps, st deviceState, jsonOut bool, _ string) int {
			if jsonOut {
				return printSettingsJSON(deps, st)
			}
			printSettingsHuman(deps, st)
			return 0
		}
	case "macros":
		return usageError(deps, errors.New("get macros is deferred to v0.1 (Macros are out of scope for v0; see the spec's Feature slicing)"))
	default:
		return usageError(deps, fmt.Errorf("unknown get subcommand %q (want keymap, lighting or settings)", sub))
	}

	fs := flag.NewFlagSet("get "+sub, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	jsonOut := fs.Bool("json", false, "stable JSON output")
	selector := fs.String("device", "", "hidraw path of the Device to use")
	var layer *string
	if layered {
		layer = fs.String("layer", "base", "Layer to show: base or fn")
	}
	if err := fs.Parse(args[1:]); err != nil {
		return usageError(deps, err)
	}
	if fs.NArg() > 0 {
		return usageError(deps, fmt.Errorf("unexpected argument %q", fs.Arg(0)))
	}
	if layer != nil && *layer != "base" && *layer != "fn" {
		return usageError(deps, fmt.Errorf("unknown --layer %q (want \"base\" or \"fn\")", *layer))
	}

	st, err := readFullState(deps, *selector)

	layerVal := ""
	if layer != nil {
		layerVal = *layer
	}
	if err != nil {
		var ce *device.CheckError
		if errors.As(err, &ce) {
			// Failed self-check: print the requested view to stdout AND the
			// joined "self-check failed: …" lines to stderr, exit 1.
			// Rationale: ADR-0003 gates WRITES on the checks; reads stay
			// available and the state display is exactly what a failing
			// check needs for diagnosis.
			print(deps, st, *jsonOut, layerVal)
			fmt.Fprintln(deps.Stderr, ce.Err)
			return 1
		}
		return fail(deps, err)
	}
	return print(deps, st, *jsonOut, layerVal)
}

// keymapJSON is the stable --json schema of `nutctl get keymap`. It covers
// all 128 Key Slots in slot order on both Layers. Every key is present on
// every slot row — a stability contract, so scripts never have to tell an
// absent key from a null one: name is the display name or null when the
// slot is out of the physical layout (the firmware-matrix extraSlots have no
// physical key, so their name is null too); knob is the Knob gesture or null
// when the slot is not a Knob gesture; fnDisabled is always a bool. Params
// and raw are lowercase hex bytes with the same spelling as the human view's
// markers; type "UNKNOWN" is the explicit marker for a page type this build
// does not know.
type keymapJSON struct {
	Model string           `json:"model"`
	Layer string           `json:"layer"`
	Slots []keymapSlotJSON `json:"slots"`
}

type keymapSlotJSON struct {
	Slot       int           `json:"slot"`
	Name       *string       `json:"name"`       // null when the slot is out of the physical layout
	Knob       *string       `json:"knob"`       // null when the slot is not a Knob gesture
	FnDisabled bool          `json:"fnDisabled"` // always a bool
	Action     keyActionJSON `json:"action"`
}

type keyActionJSON struct {
	Type   string `json:"type"`
	Params string `json:"params"` // hex bytes: param1..3
	Raw    string `json:"raw"`    // hex bytes: all 4 wire bytes
}

// keyActionJSONFor renders a Key Action for --json: the page-type label and
// the raw bytes as lowercase hex. Unknown page types keep their raw bytes —
// never dropped.
func keyActionJSONFor(a protocol.KeyAction) keyActionJSON {
	return keyActionJSON{
		Type:   a.Type.String(),
		Params: fmt.Sprintf("%02x %02x %02x", a.Params[0], a.Params[1], a.Params[2]),
		Raw:    fmt.Sprintf("%02x %02x %02x %02x", a.Raw[0], a.Raw[1], a.Raw[2], a.Raw[3]),
	}
}

// knobGestures returns slot → Knob gesture for the layout table.
func knobGestures(l device.Layout) map[int]string {
	out := make(map[int]string, 3)
	for _, k := range l.Knob() {
		out[k.Slot] = k.Gesture
	}
	return out
}

// fnDisabledSlots returns slot → true for the Key Slots that cannot be bound
// on the Fn Layer.
func fnDisabledSlots(l device.Layout) map[int]bool {
	out := make(map[int]bool)
	for _, s := range l.FnDisabledSlots() {
		out[s] = true
	}
	return out
}

// extraSlots returns slot → true for the firmware-matrix Key Slots: Key
// Slots the firmware binds by default but the Model has no physical key for
// (shared matrix with sibling Models), observed on firmware 1.20.
func extraSlots(l device.Layout) map[int]bool {
	out := make(map[int]bool)
	for _, s := range l.ExtraSlots() {
		out[s] = true
	}
	return out
}

func printKeymapJSON(deps Deps, st deviceState, layer string) int {
	layout, err := device.LayoutFor(st.model)
	if err != nil {
		return fail(deps, err)
	}
	km := st.Base
	if layer == "fn" {
		km = st.Fn
	}
	gestures := knobGestures(layout)
	disabled := fnDisabledSlots(layout)
	slots := make([]keymapSlotJSON, len(km))
	for i, action := range km {
		slots[i] = keymapSlotJSON{
			Slot:       i,
			FnDisabled: disabled[i],
			Action:     keyActionJSONFor(action),
		}
		if name, ok := layout.Name(i); ok {
			slots[i].Name = &name
		}
		if g, ok := gestures[i]; ok {
			slots[i].Knob = &g
		}
	}
	return printJSON(deps, keymapJSON{Model: st.model.Name, Layer: layer, Slots: slots})
}

// printKeymapHuman lists EVERY Key Slot (0..127) in slot order with its
// decoded Key Action — one row per Key Slot, DEFAULT included. A Key Slot
// the layout knows shows its display name (Knob gesture rows keep the
// name+" (knob …)" label); a firmware-matrix Key Slot shows "(firmware
// default — no physical key)" (the Model has no physical key there, but the
// firmware binds it by default — shared matrix with sibling Models); a
// truly out-of-layout Key Slot shows "(not in layout)". On the Fn Layer the
// Fn-disabled Key Slots are kept visible but marked "(fn-disabled)" — their
// Key Action is still what the Device reports.
func printKeymapHuman(deps Deps, st deviceState, layer string) {
	layout, err := device.LayoutFor(st.model)
	if err != nil {
		fail(deps, err)
		return
	}
	km := st.Base
	if layer == "fn" {
		km = st.Fn
	}
	disabled := fnDisabledSlots(layout)
	gestures := knobGestures(layout)
	extras := extraSlots(layout)

	fmt.Fprintf(deps.Stdout, "Model: %s\nLayer: %s\n\n", st.model.Name, layer)
	w := tabwriter.NewWriter(deps.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "SLOT\tNAME\tKEY ACTION")
	row := func(slot int, name string) {
		if layer == "fn" && disabled[slot] {
			name += " (fn-disabled)"
		}
		fmt.Fprintf(w, "%d\t%s\t%s\n", slot, name, protocol.KeyActionText(km[slot]))
	}
	for slot := range km {
		name, ok := layout.Name(slot)
		switch {
		case !ok && extras[slot]:
			name = "(firmware default — no physical key)"
		case !ok:
			// Truly out-of-layout Key Slot. A well-aligned read reports
			// DEFAULT here; anything else fails self-check 2 and names
			// this row — which is exactly why the row renders the Key
			// Action the Device reports, whatever it is.
			name = "(not in layout)"
		default:
			if g, isKnob := gestures[slot]; isKnob {
				name += " (knob " + g + ")"
			}
		}
		row(slot, name)
	}
	w.Flush()
}

// lightingJSON is the stable --json schema of `nutctl get lighting`: the
// Lighting Effect and the Per-Key RGB table. Colors are "#rrggbb".
type lightingJSON struct {
	Model     string             `json:"model"`
	Effect    lightingEffectJSON `json:"effect"`
	PerKeyRGB []perKeyLEDJSON    `json:"perKeyRgb"`
}

type lightingEffectJSON struct {
	Mode           uint8  `json:"mode"`
	RGB            string `json:"rgb"`
	DriverSetting  uint8  `json:"driverSetting"`
	SecondaryRGB   string `json:"secondaryRgb"`
	ColorMode      uint8  `json:"colorMode"`
	Brightness     uint8  `json:"brightness"`
	Speed          uint8  `json:"speed"`
	Direction      uint8  `json:"direction"`
	EffectModeType uint8  `json:"effectModeType"`
	CheckCode      string `json:"checkCode"`   // raw bytes at offsets 14..15, e.g. "00 00"
	CheckCodeOK    bool   `json:"checkCodeOk"` // check code is a recognized state (written or factory/unwritten)
}

type perKeyLEDJSON struct {
	LEDID uint8  `json:"ledId"`
	RGB   string `json:"rgb"`
}

// checkCodeText renders the Lighting Effect check code (offsets 14..15) for
// humans: the observed bytes and which recognized state they are — written
// (0xaa 0x55, put there by the vendor app's SET path) or factory/unwritten
// (0x00 0x00, the observed firmware-1.20 state of a Device that has never
// been written) — or FAILED naming the bytes.
func checkCodeText(cc [2]byte) string {
	switch cc {
	case [2]byte{protocol.CheckCodeByte0, protocol.CheckCodeByte1}:
		return fmt.Sprintf("0x%02x 0x%02x (written by the vendor app's SET path)", cc[0], cc[1])
	case [2]byte{0, 0}:
		return "0x00 0x00 (factory/unwritten)"
	default:
		return fmt.Sprintf("FAILED: 0x%02x 0x%02x (want 0xaa 0x55 or 0x00 0x00 at offsets 14-15)", cc[0], cc[1])
	}
}

func printLightingJSON(deps Deps, st deviceState) int {
	le := st.Lighting
	perKey := make([]perKeyLEDJSON, len(st.PerKey))
	for i, e := range st.PerKey {
		perKey[i] = perKeyLEDJSON{LEDID: e.LEDID, RGB: protocol.HexRGB(e.R, e.G, e.B)}
	}
	return printJSON(deps, lightingJSON{
		Model: st.model.Name,
		Effect: lightingEffectJSON{
			Mode:           le.Mode,
			RGB:            protocol.HexRGB(le.RGB[0], le.RGB[1], le.RGB[2]),
			DriverSetting:  le.DriverSetting,
			SecondaryRGB:   protocol.HexRGB(le.SecondaryRGB[0], le.SecondaryRGB[1], le.SecondaryRGB[2]),
			ColorMode:      le.ColorMode,
			Brightness:     le.Brightness,
			Speed:          le.Speed,
			Direction:      le.Direction,
			EffectModeType: le.EffectModeType,
			CheckCode:      fmt.Sprintf("%02x %02x", le.CheckCode[0], le.CheckCode[1]),
			CheckCodeOK:    le.CheckCodeOK,
		},
		PerKeyRGB: perKey,
	})
}

func printLightingHuman(deps Deps, st deviceState) {
	le := st.Lighting
	fmt.Fprintf(deps.Stdout, "Model: %s\n\n", st.model.Name)
	fmt.Fprintln(deps.Stdout, "Lighting Effect:")
	w := tabwriter.NewWriter(deps.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "  Mode:\t%d\n", le.Mode)
	fmt.Fprintf(w, "  Primary color:\t%s\n", protocol.HexRGB(le.RGB[0], le.RGB[1], le.RGB[2]))
	fmt.Fprintf(w, "  Driver setting:\t%d\n", le.DriverSetting)
	fmt.Fprintf(w, "  Secondary color:\t%s\n", protocol.HexRGB(le.SecondaryRGB[0], le.SecondaryRGB[1], le.SecondaryRGB[2]))
	fmt.Fprintf(w, "  Color mode:\t%d\n", le.ColorMode)
	fmt.Fprintf(w, "  Brightness:\t%d (range 1-6)\n", le.Brightness)
	fmt.Fprintf(w, "  Speed:\t%d (range 1-6)\n", le.Speed)
	fmt.Fprintf(w, "  Direction:\t%d\n", le.Direction)
	fmt.Fprintf(w, "  Effect mode type:\t%d\n", le.EffectModeType)
	fmt.Fprintf(w, "  Check code:\t%s\n", checkCodeText(le.CheckCode))
	w.Flush()

	fmt.Fprintf(deps.Stdout, "\nPer-Key RGB (%d entries):\n", len(st.PerKey))
	w = tabwriter.NewWriter(deps.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "  LED\tCOLOR")
	for _, e := range st.PerKey {
		fmt.Fprintf(w, "  %d\t%s\n", e.LEDID, protocol.HexRGB(e.R, e.G, e.B))
	}
	w.Flush()
}

// settingsJSON is the stable --json schema of `nutctl get settings`.
// Report Rate renders as its label ("8K").
type settingsJSON struct {
	Model              string  `json:"model"`
	GameMode           uint8   `json:"gameMode"`
	FnSwitch           uint8   `json:"fnSwitch"`
	SleepTime          uint8   `json:"sleepTime"`
	KeyDelay           uint8   `json:"keyDelay"`
	ReportRate         string  `json:"reportRate"`
	SystemMode         uint8   `json:"systemMode"`
	TFTDisplayTime     uint8   `json:"tftDisplayTime"`
	TopDeadZone        float64 `json:"topDeadZone"`
	BottomDeadZone     float64 `json:"bottomDeadZone"`
	StabilityMode      uint8   `json:"stabilityMode"`
	AutoCalibration    uint8   `json:"autoCalibration"`
	SingleKeyWakeup    uint8   `json:"singleKeyWakeup"`
	PushButtonMode     uint8   `json:"pushButtonMode"`
	NKROSwitch         uint8   `json:"nkroSwitch"`
	WirelessReportRate uint16  `json:"wirelessReportRate"`
	PowerMode          uint8   `json:"powerMode"`
}

func printSettingsJSON(deps Deps, st deviceState) int {
	s := st.Settings
	return printJSON(deps, settingsJSON{
		Model:              st.model.Name,
		GameMode:           s.GameMode,
		FnSwitch:           s.FnSwitch,
		SleepTime:          s.SleepTime,
		KeyDelay:           s.KeyDelay,
		ReportRate:         s.ReportRate.String(),
		SystemMode:         s.SystemMode,
		TFTDisplayTime:     s.TFTDisplayTime,
		TopDeadZone:        s.TopDeadZone,
		BottomDeadZone:     s.BottomDeadZone,
		StabilityMode:      s.StabilityMode,
		AutoCalibration:    s.AutoCalibration,
		SingleKeyWakeup:    s.SingleKeyWakeup,
		PushButtonMode:     s.PushButtonMode,
		NKROSwitch:         s.NKROSwitch,
		WirelessReportRate: s.WirelessReportRate,
		PowerMode:          s.PowerMode,
	})
}

func printSettingsHuman(deps Deps, st deviceState) {
	s := st.Settings
	fmt.Fprintf(deps.Stdout, "Model: %s\n\n", st.model.Name)
	w := tabwriter.NewWriter(deps.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "Report rate:\t%s\n", s.ReportRate)
	fmt.Fprintf(w, "Game mode:\t%d\n", s.GameMode)
	fmt.Fprintf(w, "Fn switch:\t%d\n", s.FnSwitch)
	fmt.Fprintf(w, "Sleep time:\t%d\n", s.SleepTime)
	fmt.Fprintf(w, "Key delay:\t%d\n", s.KeyDelay)
	fmt.Fprintf(w, "System mode:\t%d\n", s.SystemMode)
	fmt.Fprintf(w, "TFT display time:\t%d\n", s.TFTDisplayTime)
	fmt.Fprintf(w, "Top dead zone:\t%.2f\n", s.TopDeadZone)
	fmt.Fprintf(w, "Bottom dead zone:\t%.2f\n", s.BottomDeadZone)
	fmt.Fprintf(w, "Stability mode:\t%d\n", s.StabilityMode)
	fmt.Fprintf(w, "Auto calibration:\t%d\n", s.AutoCalibration)
	fmt.Fprintf(w, "Single key wakeup:\t%d\n", s.SingleKeyWakeup)
	fmt.Fprintf(w, "Push button mode:\t%d\n", s.PushButtonMode)
	fmt.Fprintf(w, "NKRO switch:\t%d\n", s.NKROSwitch)
	fmt.Fprintf(w, "Wireless report rate:\t%d\n", s.WirelessReportRate)
	fmt.Fprintf(w, "Power mode:\t%d\n", s.PowerMode)
	w.Flush()
}

// printJSON renders a stable --json view: 2-space indent plus a trailing
// newline (the convention every --json command shares).
func printJSON(deps Deps, v any) int {
	out, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fail(deps, err)
	}
	fmt.Fprintf(deps.Stdout, "%s\n", out)
	return 0
}

// fail prints an actionable error and returns the runtime exit code. The
// hints are the transport seam's (hid.Hint), so every UI offers the same
// fix for the same problem.
func fail(deps Deps, err error) int {
	fmt.Fprintf(deps.Stderr, "error: %v\n", err)
	if hint := hid.Hint(err); hint != "" {
		fmt.Fprintln(deps.Stderr, hint)
	}
	return 1
}

func usageError(deps Deps, err error) int {
	fmt.Fprintf(deps.Stderr, "nutctl: %v\n\n%s", err, usageText)
	return 2
}
