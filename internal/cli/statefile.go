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

	s, err := device.Open(deps.Devices, *selector)
	if err != nil {
		return fail(deps, err)
	}
	defer s.Close()

	st, checkErr := checkedRead(s)
	if err := device.SaveStateFile(path, device.StateFileFor(s.Model, s.DeviceInfo.Version, st.State)); err != nil {
		return fail(deps, err)
	}
	fmt.Fprintf(deps.Stdout, "saved %s state (firmware %s) to %s\n", s.Model.Name, s.DeviceInfo.Version, path)

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

	s, err := device.Open(deps.Devices, *selector)
	if err != nil {
		return fail(deps, err)
	}
	defer s.Close()
	info, model, di := s.Info, s.Model, s.DeviceInfo

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
			"refusing to write: the Device reports firmware status %s", protocol.FirmwareStatusText(di.FirmwareStatus)))
	}
	st, checkErr := checkedRead(s)
	if checkErr != nil {
		fmt.Fprintln(deps.Stderr, checkErr)
		fmt.Fprintln(deps.Stderr, "refusing to write: the Device must pass its self-checks before the first write (ADR-0003)")
		return 1
	}
	if err := writeGate(deps, *skipGolden, device.StateFileFor(model, di.Version, st.State)); err != nil {
		return fail(deps, err)
	}

	// Batched writes (four blocks, one complete transfer each on the wire;
	// the Lighting block is two transfers: SET_LED_EFFECT +
	// SET_CUSTOM_LED_DATA), verified by reading the Device back — the proof
	// the writes landed is the read-back, never the write itself.
	_, diffs, err := s.ApplyVerified(context.Background(), sf.State)
	if err != nil {
		return fail(deps, err)
	}
	if len(diffs) > 0 {
		fmt.Fprintln(deps.Stdout, "wrote 4 blocks in batched writes: base, fn, lighting (Lighting Effect + Per-Key RGB), settings")
		fmt.Fprintln(deps.Stdout, device.DiffHeader(len(diffs)))
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
	name := device.GoldenName(time.Now())
	reader := bufio.NewReader(deps.Stdin)
	for {
		fmt.Fprintf(deps.Stderr, "%s ", device.GoldenPrompt(name))
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
