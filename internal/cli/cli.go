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
  nutctl list [--json]                enumerate connected Devices and identify their Model
  nutctl info [--json] [--device P]   print Model, firmware version, Report Rate and Device facts

The interactive TUI and the rest of the read/write surface land in later
milestones; see PLAN.md.
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
		out, err := json.MarshalIndent(entries, "", "  ")
		if err != nil {
			return fail(deps, err)
		}
		fmt.Fprintf(deps.Stdout, "%s\n", out)
		return 0
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

// firmwareIdentity is the identity the firmware reports in GET_DEVICE_INFO.
func firmwareIdentity(di protocol.DeviceInfo) device.Identity {
	return device.Identity{VendorID: di.VID, ProductID: di.PID}
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
	out, err := json.MarshalIndent(infoJSON{
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
	}, "", "  ")
	if err != nil {
		return fail(deps, err)
	}
	fmt.Fprintf(deps.Stdout, "%s\n", out)
	return 0
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
		fmt.Fprintln(deps.Stderr, "hint: another process holds the Device (close the vendor app and any other nutctl session)")
	}
	return 1
}

func usageError(deps Deps, err error) int {
	fmt.Fprintf(deps.Stderr, "nutctl: %v\n\n%s", err, usageText)
	return 2
}
