package cli

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/ht4w5/nutctl/internal/device"
	"github.com/ht4w5/nutctl/internal/protocol"
)

// --- `nutctl save` / `nutctl load` ---
//
// State Files (CONTEXT.md) are written and read only at a path the user
// chose (ADR-0005 — no preset management, no background file writes). `save`
// is the golden read of ADR-0003 and the write gate's golden save in one: one
// code path, one format. `load` is the write path, gated (ADR-0003): writes
// unlock only on a Device whose self-checks pass, after the golden-read
// prompt, and never in bootloader/firmware-recovery state.

// parseWithFile parses one command with flags and exactly one positional
// file argument, in either order: `nutctl save state.json --device P` and
// `nutctl save --device P state.json` both work. Bad input names the
// offending argument — never a bare count.
func parseWithFile(fs *flag.FlagSet, args []string) (string, error) {
	if err := fs.Parse(args); err != nil {
		return "", err
	}
	switch {
	case fs.NArg() == 1:
		return fs.Arg(0), nil
	case fs.NArg() > 1 && !strings.HasPrefix(args[0], "-"):
		// The file came first: `nutctl save state.json --device P`.
		file := args[0]
		if err := fs.Parse(args[1:]); err != nil {
			return "", err
		}
		if fs.NArg() == 0 {
			return file, nil
		}
		return "", fmt.Errorf("unexpected argument %q (want one State File path)", fs.Arg(0))
	case fs.NArg() == 0:
		return "", errors.New("needs a State File path")
	default:
		return "", fmt.Errorf("unexpected argument %q (want one State File path)", fs.Arg(1))
	}
}

// stateFileFor is the envelope around one read pass: which Model it came
// from, on which firmware, under which schema. The golden read of ADR-0003 is
// just this — one code path for save, golden and restore (ADR-0005).
func stateFileFor(model device.Model, firmware string, st deviceState) device.StateFile {
	return device.StateFile{
		Model:    model.Name,
		Firmware: firmware,
		Schema:   device.SchemaCurrent,
		State:    st.snapshot(),
	}
}

// snapshot is the State File's view of one read pass: the five blocks as
// Device state. The wire markers the SET format forces are not state and do
// not travel through a State File (device.StateFile).
func (st deviceState) snapshot() device.State {
	return device.State{
		Base:     st.base,
		Fn:       st.fn,
		Lighting: st.lighting,
		PerKey:   st.perKey,
		Settings: st.settings,
	}
}

// runSave writes the Device's current state to a State File at the path the
// user chose. Like `get`, it saves what the Device reports even when the
// self-checks fail — the checks gate WRITES to the Device (ADR-0003), and a
// failing check is exactly when a snapshot is worth having — while the
// "self-check failed: …" lines go to stderr and the exit code is 1.
func runSave(args []string, deps Deps) int {
	fs := flag.NewFlagSet("save", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	selector := fs.String("device", "", "hidraw path of the Device to use")
	path, err := parseWithFile(fs, args)
	if err != nil {
		return usageError(deps, err)
	}

	info, model, dev, di, err := openSelected(deps, *selector)
	if err != nil {
		return fail(deps, err)
	}
	defer dev.Close()

	st, checkErr := checkedRead(info, model, dev, di)
	if err := device.SaveStateFile(path, stateFileFor(model, di.Version, st)); err != nil {
		return fail(deps, err)
	}
	fmt.Fprintf(deps.Stdout, "saved %s state (firmware %s) to %s\n", model.Name, di.Version, path)

	if checkErr != nil {
		fmt.Fprintln(deps.Stderr, checkErr)
		return 1
	}
	return 0
}

// runLoad applies a State File to the Device — the write path (ADR-0003).
// Everything a write needs is proven first: the file must be for this Model,
// the Device must not be in bootloader/firmware-recovery state, its
// self-checks must pass, and the golden-read prompt (or the noisy skip flag)
// must answer where the way back lives. Then the four blocks (Base Layer, Fn
// Layer, Lighting Effect + Per-Key RGB, Settings) are applied in batched
// writes and verified by reading the Device back and printing the diff — the
// proof the writes landed is the read-back, never the write itself.
func runLoad(args []string, deps Deps) int {
	fs := flag.NewFlagSet("load", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	selector := fs.String("device", "", "hidraw path of the Device to use")
	skipGolden := fs.Bool("i-know-what-im-doing", false, "skip the golden-read prompt before the session's first write")
	path, err := parseWithFile(fs, args)
	if err != nil {
		return usageError(deps, err)
	}

	sf, err := device.LoadStateFile(path)
	if err != nil {
		return fail(deps, err)
	}

	info, model, dev, di, err := openSelected(deps, *selector)
	if err != nil {
		return fail(deps, err)
	}
	defer dev.Close()

	// A State File from another Model is refused before anything is written
	// (spec user story 25); a firmware mismatch warns but proceeds (26).
	warning, err := sf.Check(model, di.Version)
	if err != nil {
		return fail(deps, err)
	}
	if warning != "" {
		fmt.Fprintln(deps.Stderr, warning)
	}

	// The write gate (ADR-0003): never in bootloader/firmware-recovery
	// state, self-checks must pass, then the golden-read prompt.
	if di.FirmwareStatus != protocol.FirmwareOK {
		return fail(deps, fmt.Errorf(
			"refusing to write: the Device reports firmware status %s", firmwareStatusText(di.FirmwareStatus)))
	}
	st, checkErr := checkedRead(info, model, dev, di)
	if checkErr != nil {
		fmt.Fprintln(deps.Stderr, checkErr)
		fmt.Fprintln(deps.Stderr, "refusing to write: the Device must pass its self-checks before the first write (ADR-0003)")
		return 1
	}
	if err := writeGate(deps, *skipGolden, stateFileFor(model, di.Version, st)); err != nil {
		return fail(deps, err)
	}

	// Batched writes: the four blocks, one complete transfer each on the
	// wire (docs/protocol.md §4; the Lighting block is two transfers:
	// SET_LED_EFFECT + SET_CUSTOM_LED_DATA).
	if err := device.Apply(context.Background(), dev, sf.State); err != nil {
		return fail(deps, err)
	}

	// Read-back verification: the proof the writes landed.
	back, err := readStatePass(dev, info.Path)
	if err != nil {
		return fail(deps, err)
	}
	if diffs := stateDiffs(sf.State, back.snapshot(), model); len(diffs) > 0 {
		fmt.Fprintln(deps.Stdout, "wrote 4 blocks in batched writes: base, fn, lighting (Lighting Effect + Per-Key RGB), settings")
		fmt.Fprintf(deps.Stdout, "read-back verification: %d difference(s) (State File → Device):\n", len(diffs))
		for _, d := range diffs {
			fmt.Fprintf(deps.Stdout, "  %s\n", d)
		}
		return 1
	}
	fmt.Fprintf(deps.Stdout, "loaded %s onto the %s at %s (firmware %s)\n", path, model.Name, info.Path, di.Version)
	fmt.Fprintln(deps.Stdout, "wrote 4 blocks in batched writes: base, fn, lighting (Lighting Effect + Per-Key RGB), settings")
	fmt.Fprintln(deps.Stdout, "read-back verified: the Device matches the State File")
	return 0
}

// writeGate is the golden-read prompt of ADR-0003 before the session's first
// write: it OFFERS to save the current Device state to a State File
// (golden-<ts>.json — the default filename is offered, never forced,
// ADR-0005). Answering n skips the save (the dismissible escape hatch of
// spec user story 5) and unlocks the write; the noisy --i-know-what-im-doing
// flag skips the prompt for scripts. No input at all is not consent either
// way: the write is refused with the flag named.
func writeGate(deps Deps, skip bool, golden device.StateFile) error {
	if skip {
		fmt.Fprintln(deps.Stderr, "warning: --i-know-what-im-doing: skipping the golden read — the Device's current state is NOT saved anywhere (ADR-0003)")
		return nil
	}
	if deps.Stdin == nil {
		return errNoGoldenAnswer
	}
	name := "golden-" + time.Now().Format("20060102-150405") + ".json"
	reader := bufio.NewReader(deps.Stdin)
	for {
		fmt.Fprintf(deps.Stderr, "save current state to ./%s? [Y/n] ", name)
		line, err := reader.ReadString('\n')
		answer := strings.ToLower(strings.TrimSpace(line))
		switch {
		case answer == "" && err != nil:
			return errNoGoldenAnswer
		case answer == "" || answer == "y" || answer == "yes":
			if err := device.SaveStateFile(name, golden); err != nil {
				return fmt.Errorf("golden read not saved, refusing to write: %w", err)
			}
			fmt.Fprintf(deps.Stderr, "golden read saved to ./%s\n", name)
			return nil
		case answer == "n" || answer == "no":
			fmt.Fprintln(deps.Stderr, "skipped the golden read — the Device's current state is not saved (the offer stands next session)")
			return nil
		default:
			fmt.Fprintln(deps.Stderr, "please answer y or n")
			if err != nil {
				return errNoGoldenAnswer
			}
		}
	}
}

// errNoGoldenAnswer is the write-gate refusal for a session that cannot
// answer the golden-read prompt.
var errNoGoldenAnswer = errors.New(
	"cannot ask about the golden read (no input): rerun with --i-know-what-im-doing to skip it, or run interactively to save it")

// stateDiffs names every difference between the State File's state (want)
// and the Device's read-back (got) — the read-back verification diff. Wire
// markers are never compared: the SET format forces them and the State File
// does not carry them (device.StateFile). Key Slots are named from the
// Model's layout table where it has a name.
func stateDiffs(want, got device.State, model device.Model) []string {
	var diffs []string
	add := func(format string, a ...any) { diffs = append(diffs, fmt.Sprintf(format, a...)) }

	name := func(slot int) string {
		if l, err := device.LayoutFor(model); err == nil {
			if n, ok := l.Name(slot); ok {
				return fmt.Sprintf("Key Slot %d (%s)", slot, n)
			}
		}
		return fmt.Sprintf("Key Slot %d", slot)
	}
	for _, layer := range []struct {
		label string
		want  protocol.Keymap
		got   protocol.Keymap
	}{{"base", want.Base, got.Base}, {"fn", want.Fn, got.Fn}} {
		for slot := range layer.want {
			if layer.want[slot].Raw == layer.got[slot].Raw {
				continue
			}
			add("%s %s: %s → %s", layer.label, name(slot),
				keyActionText(layer.want[slot]), keyActionText(layer.got[slot]))
		}
	}

	for _, f := range []struct {
		label string
		want  any
		got   any
	}{
		{"lighting mode", want.Lighting.Mode, got.Lighting.Mode},
		{"lighting primary color", hexRGB(want.Lighting.RGB[0], want.Lighting.RGB[1], want.Lighting.RGB[2]), hexRGB(got.Lighting.RGB[0], got.Lighting.RGB[1], got.Lighting.RGB[2])},
		{"lighting secondary color", hexRGB(want.Lighting.SecondaryRGB[0], want.Lighting.SecondaryRGB[1], want.Lighting.SecondaryRGB[2]), hexRGB(got.Lighting.SecondaryRGB[0], got.Lighting.SecondaryRGB[1], got.Lighting.SecondaryRGB[2])},
		{"lighting color mode", want.Lighting.ColorMode, got.Lighting.ColorMode},
		{"lighting brightness", want.Lighting.Brightness, got.Lighting.Brightness},
		{"lighting speed", want.Lighting.Speed, got.Lighting.Speed},
		{"lighting direction", want.Lighting.Direction, got.Lighting.Direction},
		{"lighting effect mode type", want.Lighting.EffectModeType, got.Lighting.EffectModeType},
	} {
		if f.want != f.got {
			add("%s: %v → %v", f.label, f.want, f.got)
		}
	}

	for i := range want.PerKey {
		w, g := want.PerKey[i], got.PerKey[i]
		// Colors only: the ledId byte is the entry index on the wire (derived
		// on write, device.StateFile) and is never state to compare.
		if w.R == g.R && w.G == g.G && w.B == g.B {
			continue
		}
		add("per-key RGB entry %d: %s → %s", i, hexRGB(w.R, w.G, w.B), hexRGB(g.R, g.G, g.B))
	}

	for _, f := range []struct {
		label string
		want  any
		got   any
	}{
		{"report rate", want.Settings.ReportRate, got.Settings.ReportRate},
		{"game mode", want.Settings.GameMode, got.Settings.GameMode},
		{"Fn switch", want.Settings.FnSwitch, got.Settings.FnSwitch},
		{"sleep time", want.Settings.SleepTime, got.Settings.SleepTime},
		{"key delay", want.Settings.KeyDelay, got.Settings.KeyDelay},
		{"system mode", want.Settings.SystemMode, got.Settings.SystemMode},
		{"TFT display time", want.Settings.TFTDisplayTime, got.Settings.TFTDisplayTime},
		{"top dead zone", want.Settings.TopDeadZone, got.Settings.TopDeadZone},
		{"bottom dead zone", want.Settings.BottomDeadZone, got.Settings.BottomDeadZone},
		{"stability mode", want.Settings.StabilityMode, got.Settings.StabilityMode},
		{"auto calibration", want.Settings.AutoCalibration, got.Settings.AutoCalibration},
		{"single key wakeup", want.Settings.SingleKeyWakeup, got.Settings.SingleKeyWakeup},
		{"push button mode", want.Settings.PushButtonMode, got.Settings.PushButtonMode},
		{"NKRO switch", want.Settings.NKROSwitch, got.Settings.NKROSwitch},
		{"wireless report rate", want.Settings.WirelessReportRate, got.Settings.WirelessReportRate},
		{"power mode", want.Settings.PowerMode, got.Settings.PowerMode},
	} {
		if f.want != f.got {
			add("settings %s: %v → %v", f.label, f.want, f.got)
		}
	}
	return diffs
}
