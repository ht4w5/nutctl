package cli

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/ht4w5/nutctl/internal/device"
	"github.com/ht4w5/nutctl/internal/protocol"
)

// --- `nutctl reset` ---
//
// Factory reset is the recovery action (ticket 08): the configuration is
// garbage and the only way back is the firmware's own reset scopes. It is
// CLI-only behind a typed confirmation naming the scope being destroyed
// (spec user story 27) — deliberately no foot-gun in the TUI — and it reuses
// the write gate of ADR-0003. Two deviations from `load`'s gate are
// deliberate and recorded in ADR-0003's reset exception: the self-checks do
// NOT gate a reset (a configuration that fails them is what the reset
// repairs), and the golden read is offered rather than required (a Device
// that cannot be read is still resettable). The bootloader refusal and the
// typed confirmation are kept verbatim.

// runReset factory-resets one scope of the Device: exactly one of
// --keys|--lighting|--macros|--all maps onto the firmware's reset
// sub-commands (docs/protocol.md §3), behind the typed confirmation and the
// write gate. The read-back counts what the reset changed — the write itself
// proves nothing (spec user story 22).
func runReset(args []string, deps Deps) int {
	fs := flag.NewFlagSet("reset", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	keys := fs.Bool("keys", false, "destroy both Layers' key bindings")
	lighting := fs.Bool("lighting", false, "destroy the Lighting Effect and Per-Key RGB")
	macros := fs.Bool("macros", false, "destroy all Macros")
	all := fs.Bool("all", false, "destroy ALL keyboard configuration")
	selector := fs.String("device", "", "hidraw path of the Device to use")
	skipPrompts := fs.Bool("i-know-what-im-doing", false, "skip the golden-read prompt (the typed confirmation is never skipped)")
	if err := fs.Parse(args); err != nil {
		return usageError(deps, err)
	}
	if fs.NArg() > 0 {
		return usageError(deps, fmt.Errorf("unexpected argument %q (want one of --keys, --lighting, --macros, --all)", fs.Arg(0)))
	}
	scope, ok := resetScopeOf(*keys, *lighting, *macros, *all)
	if !ok {
		return usageError(deps, errors.New("reset needs exactly one scope: --keys, --lighting, --macros or --all"))
	}

	s, err := device.Open(deps.Devices, *selector)
	if err != nil {
		return fail(deps, err)
	}
	defer s.Close()
	model, di := s.Model, s.DeviceInfo

	// The write gate (ADR-0003): never in bootloader/firmware-recovery state
	// (spec user story 28) — a half-updated keyboard is never made worse.
	if di.FirmwareStatus != protocol.FirmwareOK {
		return fail(deps, fmt.Errorf(
			"refusing to write: the Device reports firmware status %s", protocol.FirmwareStatusText(di.FirmwareStatus)))
	}

	// The pre-reset snapshot: what the golden read saves and what the
	// read-back is compared against. It is read UNchecked — reset is the
	// recovery for a configuration the self-checks reject — and a Device
	// that cannot even be read is still resettable, with a loud warning and
	// no proof.
	before, readErr := s.Read()
	if readErr != nil {
		fmt.Fprintf(deps.Stderr,
			"warning: cannot read the current state (%v) — no golden read is offered and the read-back is skipped\n", readErr)
	}

	// The typed confirmation names the scope being destroyed (spec user
	// story 27) and is never skipped by any flag; the write gate's golden
	// read (ADR-0003) then precedes the write as usual. Both prompts share
	// one reader over stdin.
	reader := stdinReader(deps)
	if err := confirmReset(deps, reader, scope); err != nil {
		return fail(deps, err)
	}
	if readErr == nil {
		if err := writeGate(deps, reader, *skipPrompts, device.StateFileFor(model, di.Version, before)); err != nil {
			return fail(deps, err)
		}
	}

	if err := s.Dev.FactoryReset(deps.ctx(), scope); err != nil {
		return fail(deps, err)
	}
	fmt.Fprintf(deps.Stdout, "factory reset sent: %s (firmware %s)\n", scope, scope.FirmwareName())
	if readErr != nil {
		return 0
	}

	after, err := s.Read()
	if err != nil {
		// The reset is sent; when its proof is unavailable, say so loudly
		// rather than failing the recovery it was meant to confirm.
		fmt.Fprintf(deps.Stderr, "warning: read-back unavailable: %v\n", err)
		return 0
	}
	fmt.Fprintln(deps.Stdout, "read-back (compared with the pre-reset state):")
	for _, line := range resetSummary(scope, before, after) {
		fmt.Fprintf(deps.Stdout, "  %s\n", line)
	}
	return 0
}

// resetScopeOf maps the four scope flags onto the firmware's reset scopes
// (docs/protocol.md §3). Exactly one flag is a scope; none or several is not.
func resetScopeOf(keys, lighting, macros, all bool) (protocol.ResetScope, bool) {
	switch {
	case keys && !lighting && !macros && !all:
		return protocol.ResetKeys, true
	case lighting && !keys && !macros && !all:
		return protocol.ResetLighting, true
	case macros && !keys && !lighting && !all:
		return protocol.ResetMacros, true
	case all && !keys && !lighting && !macros:
		return protocol.ResetAll, true
	}
	return 0, false
}

// confirmReset is the typed confirmation of spec user story 27: the prompt
// names the scope being destroyed and the user must type the scope back
// (`reset keys`). It is the one gate with no flag around it — a factory
// reset is only ever sent after that phrase, interactively or piped on stdin.
// No input is not consent: the reset is refused and nothing is sent.
func confirmReset(deps Deps, reader *bufio.Reader, scope protocol.ResetScope) error {
	if reader == nil {
		return errNoResetConfirmation
	}
	phrase := "reset " + scope.String()
	fmt.Fprintf(deps.Stderr, "factory reset %s destroys %s — type %q to confirm: ",
		scope, scope.Destroys(), phrase)
	line, err := reader.ReadString('\n')
	answer := strings.TrimSpace(line)
	if answer == phrase {
		return nil
	}
	if answer == "" && err != nil {
		return errNoResetConfirmation
	}
	return fmt.Errorf("confirmation %q did not match %q — nothing was reset", answer, phrase)
}

// errNoResetConfirmation is the typed-confirmation refusal for a session
// that cannot answer (no input at all): the phrase can be piped on stdin, but
// its absence is never consent.
var errNoResetConfirmation = errors.New(
	"typed confirmation not answered — nothing was reset; run interactively or pipe the confirmation phrase (e.g. \"reset keys\") on stdin")

// resetSummary reports what the reset changed, per block the scope covers:
// counts computed from the read-back against the pre-reset state (the same
// "which bytes are state" rule the read-back diff applies, device.Changes) —
// the proof is the read-back, never the write. A block the v0 read surface
// cannot see (Macros, deferred to v0.1) says so instead of pretending.
func resetSummary(scope protocol.ResetScope, before, after device.State) []string {
	c := device.ChangesBetween(before, after)
	slots := func(n int) string {
		if n == 0 {
			return "unchanged"
		}
		return fmt.Sprintf("%d of 128 Key Slots changed", n)
	}
	entries := func(n int) string {
		if n == 0 {
			return "unchanged"
		}
		return fmt.Sprintf("%d of 128 entries changed", n)
	}
	word := func(changed bool) string {
		if changed {
			return "changed"
		}
		return "unchanged"
	}

	var lines []string
	if scope == protocol.ResetKeys || scope == protocol.ResetAll {
		lines = append(lines,
			"base: "+slots(c.Base),
			"fn: "+slots(c.Fn))
	}
	if scope == protocol.ResetLighting || scope == protocol.ResetAll {
		lines = append(lines,
			"lighting effect: "+word(c.Lighting),
			"per-key RGB: "+entries(c.PerKey))
	}
	if scope == protocol.ResetAll {
		lines = append(lines, "settings: "+word(c.Settings))
	}
	if scope == protocol.ResetMacros || scope == protocol.ResetAll {
		lines = append(lines, "macros: not readable in this build (Macros are deferred to v0.1)")
	}
	return lines
}
