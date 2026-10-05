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
)

// Deps carries the seams the CLI runs against.
type Deps struct {
	Devices hid.Enumerator
	Stdin   io.Reader // the write gate's golden-read prompt reads it
	Stdout  io.Writer
	Stderr  io.Writer
}

// acceptedUsagePages are the HID collection usage pages the vendor protocol
// speaks on (docs/protocol.md §1). Interfaces without one of these cannot be
// candidates.
var acceptedUsagePages = map[uint16]bool{
	0xFF68: true,
	0xFF80: true,
	0xFF60: true,
	0xFF00: true,
	0xFF01: true,
	0xFF1B: true,
}

const usageText = `nutctl — configure the WEIKAV NUT87 keyboard

usage:
  nutctl list [--json]                       enumerate connected Devices and identify their Model
  nutctl info [--json] [--device P]          print Model, firmware version, Report Rate and Device facts
  nutctl get keymap [--layer base|fn] [--json] [--device P]
                                             list every Key Slot with its current Key Action
  nutctl get lighting [--json] [--device P]  print the Lighting Effect and Per-Key RGB
  nutctl get settings [--json] [--device P]  print Settings (Report Rate, key delay, sleep, …)
  nutctl save <file> [--device P]            write the Device's current state to a State File
  nutctl load <file> [--device P] [--i-know-what-im-doing]
                                             apply a State File to the Device (write-gated)

The interactive TUI lands in a later milestone; see PLAN.md.
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
		fmt.Fprint(deps.Stdout, usageText)
		return 0
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
		if !acceptedUsagePages[info.UsagePage] {
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

// usbIdentity is the Device Identity as seen at enumeration (CONTEXT.md): the
// USB ids and the product-name string the Model is matched on. The USB product
// string is what the vendor bundle calls `productName` and matches config names
// with; GET_DEVICE_INFO carries no name fields.
func usbIdentity(info hid.Info) device.Identity {
	return device.Identity{
		VendorID:    info.VendorID,
		ProductID:   info.ProductID,
		ProductName: info.ProductName,
	}
}

// firmwareIdentity is the identity the firmware reports in GET_DEVICE_INFO:
// the vendor/product ids and the u16 manufacturer/product fields.
// GET_DEVICE_INFO carries no name strings (docs/protocol.md §4), so the
// product-name half of the Device Identity can only be matched from USB
// enumeration (see usbIdentity).
func firmwareIdentity(di protocol.DeviceInfo) device.Identity {
	return device.Identity{
		VendorID:     di.VID,
		ProductID:    di.PID,
		Manufacturer: di.Manufacturer,
		Product:      di.Product,
	}
}

// probe identifies the Model from the Device Identity and, for supported
// Models, proves the protocol speaks by reading GET_DEVICE_INFO. It never
// opens a Device it does not support.
func probe(deps Deps, info hid.Info) listEntry {
	e := listEntry{
		Path:         info.Path,
		USBVendorID:  info.VendorID,
		USBProductID: info.ProductID,
		ProductName:  info.ProductName,
		Manufacturer: info.Manufacturer,
	}

	m, err := device.Identify(usbIdentity(info))
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

	di, err := readInfo(deps, info)
	if err != nil {
		e.Status = statusProbeFail
		e.Detail = err.Error()
		return e
	}
	e.Firmware = di.Version
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

	infos, err := deps.Devices.Enumerate()
	if err != nil {
		return fail(deps, fmt.Errorf("enumerate devices: %w", err))
	}

	info, m, err := selectDevice(infos, *selector)
	if err != nil {
		return fail(deps, err)
	}

	dev, di, err := openProbe(deps, info)
	if err != nil {
		return fail(deps, err)
	}
	defer dev.Close()

	settings, err := dev.Settings(context.Background())
	if err != nil {
		return fail(deps, fmt.Errorf("probe %s: %w", info.Path, err))
	}

	view := infoView{info: info, model: m, id: usbIdentity(info), di: di, settings: settings}
	if *jsonOut {
		return printInfoJSON(deps, view)
	}
	printInfoHuman(deps, view)
	return 0
}

// selectDevice picks the Device: an explicit --device path, or the single
// supported candidate. Ambiguity and refusals are errors — never a guess.
func selectDevice(infos []hid.Info, selector string) (hid.Info, device.Model, error) {
	type candidate struct {
		info  hid.Info
		model device.Model
	}
	var supported []candidate
	var rejected error // a concrete refusal (wrong Model / unknown device), if any
	for _, info := range infos {
		if !acceptedUsagePages[info.UsagePage] {
			continue
		}
		if selector != "" && info.Path != selector {
			continue
		}
		id := usbIdentity(info)
		m, err := device.Identify(id)
		if err != nil {
			if selector != "" {
				return hid.Info{}, device.Model{}, err
			}
			// Prefer the specific wrong-Model refusal over a generic "none found".
			if rejected == nil {
				rejected = err
			}
			continue
		}
		if err := device.CheckSupported(m); err != nil {
			if selector != "" {
				return hid.Info{}, device.Model{}, err
			}
			var wrong *device.WrongModelError
			if errors.As(err, &wrong) && rejected == nil {
				rejected = err
			}
			continue
		}
		supported = append(supported, candidate{info: info, model: m})
	}

	if selector != "" {
		if len(supported) == 0 {
			return hid.Info{}, device.Model{}, fmt.Errorf("no device at %s", selector)
		}
		return supported[0].info, supported[0].model, nil
	}
	switch len(supported) {
	case 0:
		if rejected != nil {
			return hid.Info{}, device.Model{}, rejected
		}
		return hid.Info{}, device.Model{}, errors.New("no supported device found (looking for a NUT87)\n" + udevHint)
	case 1:
		return supported[0].info, supported[0].model, nil
	default:
		paths := ""
		for i, c := range supported {
			if i > 0 {
				paths += ", "
			}
			paths += c.info.Path
		}
		return hid.Info{}, device.Model{}, fmt.Errorf(
			"multiple supported devices connected (%s); select one with --device PATH", paths)
	}
}

// openProbe opens a supported Device, reads its identity block and verifies
// it against the enumerated identity (the self-check of docs/capture.md
// Method A: the firmware must report the USB ids we enumerated).
func openProbe(deps Deps, info hid.Info) (*protocol.Device, protocol.DeviceInfo, error) {
	tr, err := deps.Devices.Open(info)
	if err != nil {
		return nil, protocol.DeviceInfo{}, fmt.Errorf("open %s: %w", info.Path, err)
	}
	dev, err := protocol.Open(tr, protocol.Options{})
	if err != nil {
		tr.Close()
		return nil, protocol.DeviceInfo{}, err
	}
	di, err := dev.Info(context.Background())
	if err != nil {
		dev.Close()
		return nil, protocol.DeviceInfo{}, fmt.Errorf("probe %s: %w", info.Path, err)
	}
	if err := device.Verify(usbIdentity(info), firmwareIdentity(di)); err != nil {
		dev.Close()
		return nil, protocol.DeviceInfo{}, err
	}
	return dev, di, nil
}

// readInfo opens a supported Device and reads its identity block, for `list`.
func readInfo(deps Deps, info hid.Info) (protocol.DeviceInfo, error) {
	dev, di, err := openProbe(deps, info)
	if err != nil {
		return protocol.DeviceInfo{}, err
	}
	dev.Close()
	return di, nil
}

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
			StatusText:      firmwareStatusText(v.di.FirmwareStatus),
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
	fmt.Fprintf(deps.Stdout, "Firmware status: %s\n", firmwareStatusText(v.di.FirmwareStatus))
	fmt.Fprintf(deps.Stdout, "Report Rate:     %s\n", v.settings.ReportRate)
	fmt.Fprintf(deps.Stdout, "Battery:         %d%% (charge status %d)\n", v.di.BatteryLevel, v.di.ChargeStatus)
	fmt.Fprintf(deps.Stdout, "Work mode:       %d\n", v.di.WorkMode)
	fmt.Fprintf(deps.Stdout, "Macro space:     %d bytes\n", v.di.MacroSpaceSize)
	fmt.Fprintf(deps.Stdout, "ROM size:        %d\n", v.di.RomSize)
}

func firmwareStatusText(status uint8) string {
	switch status {
	case protocol.FirmwareOK:
		return "ok"
	case protocol.FirmwareBootloader:
		return "bootloader (writes will be refused)"
	default:
		return fmt.Sprintf("unknown(%d)", status)
	}
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

// deviceState is one full read pass over a Device: every block the v0 read
// surface shows (docs/protocol.md §3 init sequence).
type deviceState struct {
	model    device.Model
	base     protocol.Keymap
	fn       protocol.Keymap
	lighting protocol.LightingEffect
	perKey   protocol.PerKeyRGB
	settings protocol.Settings
}

// checkError wraps a device.RunChecks failure. Its lines already name
// themselves (each starts with "self-check failed: "), so callers print them
// verbatim instead of re-prefixing them as a generic error.
type checkError struct{ err error }

func (e *checkError) Error() string { return e.err.Error() }
func (e *checkError) Unwrap() error { return e.err }

// readFullState opens the selected Device and makes ONE full read pass
// (GET_KEY, GET_FN_KEY, GET_LED_EFFECT, GET_CUSTOM_LED_DATA, GET_GAME_MODE),
// then runs device.RunChecks ONCE over the pass. A failed self-check comes
// back as *checkError ALONGSIDE the state, so callers can show the requested
// view (the diagnosis a failing check needs) and still fail loudly.
func readFullState(deps Deps, selector string) (deviceState, error) {
	info, model, dev, di, err := openSelected(deps, selector)
	if err != nil {
		return deviceState{}, err
	}
	defer dev.Close()
	return checkedRead(info, model, dev, di)
}

// checkedRead makes ONE full read pass and runs device.RunChecks ONCE over it
// (ADR-0003) — the shared verification step of every get/save/load. A failed
// self-check comes back as *checkError ALONGSIDE the state: the checks gate
// WRITES, not reads, and the state is exactly what a failing check needs for
// diagnosis.
func checkedRead(info hid.Info, model device.Model, dev *protocol.Device, di protocol.DeviceInfo) (deviceState, error) {
	st, err := readStatePass(dev, info.Path)
	if err != nil {
		return deviceState{}, err
	}
	st.model = model
	if err := device.RunChecks(model, device.CheckInput{
		USB:      usbIdentity(info),
		Reported: firmwareIdentity(di),
		Base:     st.base,
		Fn:       st.fn,
		Lighting: st.lighting,
	}); err != nil {
		return st, &checkError{err}
	}
	return st, nil
}

// openSelected identifies the Model, opens the selected Device and verifies
// it against its own firmware report (openProbe). The caller closes the
// Device. It is the one way save/load/get reach the wire.
func openSelected(deps Deps, selector string) (hid.Info, device.Model, *protocol.Device, protocol.DeviceInfo, error) {
	infos, err := deps.Devices.Enumerate()
	if err != nil {
		return hid.Info{}, device.Model{}, nil, protocol.DeviceInfo{}, fmt.Errorf("enumerate devices: %w", err)
	}
	info, model, err := selectDevice(infos, selector)
	if err != nil {
		return hid.Info{}, device.Model{}, nil, protocol.DeviceInfo{}, err
	}
	dev, di, err := openProbe(deps, info)
	if err != nil {
		return hid.Info{}, device.Model{}, nil, protocol.DeviceInfo{}, err
	}
	return info, model, dev, di, nil
}

// readStatePass makes ONE full read pass over an open Device: GET_KEY,
// GET_FN_KEY, GET_LED_EFFECT, GET_CUSTOM_LED_DATA, GET_GAME_MODE.
func readStatePass(dev *protocol.Device, path string) (deviceState, error) {
	ctx := context.Background()
	st := deviceState{}
	var err error
	// The block names come from the protocol layer's errors (they already say
	// "GET_KEY: …" and friends); here only the probe path is added — the
	// established openProbe pattern, never a doubled prefix.
	if st.base, err = dev.Keymap(ctx); err != nil {
		return deviceState{}, fmt.Errorf("probe %s: %w", path, err)
	}
	if st.fn, err = dev.FnKeymap(ctx); err != nil {
		return deviceState{}, fmt.Errorf("probe %s: %w", path, err)
	}
	if st.lighting, err = dev.LightingEffect(ctx); err != nil {
		return deviceState{}, fmt.Errorf("probe %s: %w", path, err)
	}
	if st.perKey, err = dev.PerKeyRGB(ctx); err != nil {
		return deviceState{}, fmt.Errorf("probe %s: %w", path, err)
	}
	if st.settings, err = dev.Settings(ctx); err != nil {
		return deviceState{}, fmt.Errorf("probe %s: %w", path, err)
	}
	return st, nil
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
		var ce *checkError
		if errors.As(err, &ce) {
			// Failed self-check: print the requested view to stdout AND the
			// joined "self-check failed: …" lines to stderr, exit 1.
			// Rationale: ADR-0003 gates WRITES on the checks; reads stay
			// available and the state display is exactly what a failing
			// check needs for diagnosis.
			print(deps, st, *jsonOut, layerVal)
			fmt.Fprintln(deps.Stderr, ce.err)
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

// keyActionText renders a Key Action for humans: the page type with its
// params, e.g. "KEYBOARD(00 29 00)"; DEFAULT renders bare. An unknown page
// type is the explicit marker with all four wire bytes, e.g.
// "UNKNOWN(2a 01 02 03)" — never dropped, never a crash.
func keyActionText(a protocol.KeyAction) string {
	switch a.Type {
	case protocol.ActionDefault:
		return a.Type.String()
	case protocol.ActionUnknown:
		return fmt.Sprintf("UNKNOWN(%02x %02x %02x %02x)",
			a.Raw[0], a.Raw[1], a.Raw[2], a.Raw[3])
	default:
		return fmt.Sprintf("%s(%02x %02x %02x)", a.Type, a.Params[0], a.Params[1], a.Params[2])
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
	km := st.base
	if layer == "fn" {
		km = st.fn
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
	km := st.base
	if layer == "fn" {
		km = st.fn
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
		fmt.Fprintf(w, "%d\t%s\t%s\n", slot, name, keyActionText(km[slot]))
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

// hexRGB renders a color as "#rrggbb".
func hexRGB(r, g, b byte) string {
	return fmt.Sprintf("#%02x%02x%02x", r, g, b)
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
	le := st.lighting
	perKey := make([]perKeyLEDJSON, len(st.perKey))
	for i, e := range st.perKey {
		perKey[i] = perKeyLEDJSON{LEDID: e.LEDID, RGB: hexRGB(e.R, e.G, e.B)}
	}
	return printJSON(deps, lightingJSON{
		Model: st.model.Name,
		Effect: lightingEffectJSON{
			Mode:           le.Mode,
			RGB:            hexRGB(le.RGB[0], le.RGB[1], le.RGB[2]),
			DriverSetting:  le.DriverSetting,
			SecondaryRGB:   hexRGB(le.SecondaryRGB[0], le.SecondaryRGB[1], le.SecondaryRGB[2]),
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
	le := st.lighting
	fmt.Fprintf(deps.Stdout, "Model: %s\n\n", st.model.Name)
	fmt.Fprintln(deps.Stdout, "Lighting Effect:")
	w := tabwriter.NewWriter(deps.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "  Mode:\t%d\n", le.Mode)
	fmt.Fprintf(w, "  Primary color:\t%s\n", hexRGB(le.RGB[0], le.RGB[1], le.RGB[2]))
	fmt.Fprintf(w, "  Driver setting:\t%d\n", le.DriverSetting)
	fmt.Fprintf(w, "  Secondary color:\t%s\n", hexRGB(le.SecondaryRGB[0], le.SecondaryRGB[1], le.SecondaryRGB[2]))
	fmt.Fprintf(w, "  Color mode:\t%d\n", le.ColorMode)
	fmt.Fprintf(w, "  Brightness:\t%d (range 1-6)\n", le.Brightness)
	fmt.Fprintf(w, "  Speed:\t%d (range 1-6)\n", le.Speed)
	fmt.Fprintf(w, "  Direction:\t%d\n", le.Direction)
	fmt.Fprintf(w, "  Effect mode type:\t%d\n", le.EffectModeType)
	fmt.Fprintf(w, "  Check code:\t%s\n", checkCodeText(le.CheckCode))
	w.Flush()

	fmt.Fprintf(deps.Stdout, "\nPer-Key RGB (%d entries):\n", len(st.perKey))
	w = tabwriter.NewWriter(deps.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "  LED\tCOLOR")
	for _, e := range st.perKey {
		fmt.Fprintf(w, "  %d\t%s\n", e.LEDID, hexRGB(e.R, e.G, e.B))
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
	s := st.settings
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
	s := st.settings
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

const udevHint = `hint: install the udev rule so an unprivileged user can open the Device:
  sudo cp udev/60-nut87.rules /etc/udev/rules.d/
  sudo udevadm control --reload && sudo udevadm trigger
then re-plug the keyboard`

// fail prints an actionable error and returns the runtime exit code.
func fail(deps Deps, err error) int {
	fmt.Fprintf(deps.Stderr, "error: %v\n", err)
	switch {
	case errors.Is(err, hid.ErrPermission):
		fmt.Fprintf(deps.Stderr, "%s\n", udevHint)
	case errors.Is(err, hid.ErrBusy):
		fmt.Fprintln(deps.Stderr, "hint: another nutctl session (or another tool) is reading this Device — close it and retry (also quit the vendor app if it is running)")
	}
	return 1
}

func usageError(deps Deps, err error) int {
	fmt.Fprintf(deps.Stderr, "nutctl: %v\n\n%s", err, usageText)
	return 2
}
