package protocol

import (
	"context"
	"fmt"
	"time"
)

// ResetScope is a factory reset scope: the sub-command SET_FACTORY_RESET
// carries in header byte 2 (docs/protocol.md §3: KEY_RESET, LIGHTING_RESET,
// MACRO_RESET, RESET_ALL — the bundle's FACTORY_RESET_TYPE table). The CLI
// surface exposes exactly four (spec user story 27); CLEAR_CALIBRATION (5) is
// a scope for Hall-effect Devices and is deliberately not offered.
type ResetScope byte

// The four factory reset scopes the CLI maps its flags onto
// (`nutctl reset --keys|--lighting|--macros|--all`).
const (
	ResetKeys     ResetScope = 1
	ResetLighting ResetScope = 2
	ResetMacros   ResetScope = 4
	ResetAll      ResetScope = 255
)

// resetScopes is the one table of the reset scopes: the firmware sub-command
// name (docs/protocol.md §3), the CLI surface word, and what the scope
// destroys — the words the typed confirmation names (spec user story 27).
// RESET_ALL covers everything the factory-reset sub-commands name (the
// bundle's own wording is "reset all keyboard settings", en-cKmNgyvw.js
// `reset_message`); whether it also resets the Settings block is unverified —
// the read-back reports it either way rather than claiming either.
var resetScopes = map[ResetScope]struct {
	word     string
	firmware string
	destroys string
}{
	ResetKeys:     {"keys", "KEY_RESET", "both Layers' key bindings (every Key Slot)"},
	ResetLighting: {"lighting", "LIGHTING_RESET", "the Lighting Effect and Per-Key RGB"},
	ResetMacros:   {"macros", "MACRO_RESET", "all Macros"},
	ResetAll:      {"all", "RESET_ALL", "ALL keyboard configuration (key bindings, lighting and macros)"},
}

// String renders the scope as the CLI surface word its flag carries — one
// spelling shared by the confirmation prompt and the read-back report.
func (s ResetScope) String() string {
	if e, ok := resetScopes[s]; ok {
		return e.word
	}
	return fmt.Sprintf("unknown(%d)", byte(s))
}

// FirmwareName is the sub-command name the firmware documents for the scope
// (docs/protocol.md §3) — what the read-back report names on the wire.
func (s ResetScope) FirmwareName() string {
	if e, ok := resetScopes[s]; ok {
		return e.firmware
	}
	return fmt.Sprintf("unknown(%d)", byte(s))
}

// Destroys names what the scope destroys — the words the typed confirmation
// prompt names (spec user story 27).
func (s ResetScope) Destroys() string {
	if e, ok := resetScopes[s]; ok {
		return e.destroys
	}
	return "an unknown scope"
}

// ResetReport builds the SET_FACTORY_RESET output report, byte-exact with the
// bundle's factoryReset encoder (rs → xn(SET_FACTORY_RESET, scope, 0,
// undefined, reportCount) in decoded/layout-classic-DSv6_q0d.js):
// `AA 0F <scope> 00 00 00 00 00`, zero-padded to reportLen. The scope rides
// in header byte 2 (the "len" position of other commands); there is no
// payload and no response.
func ResetReport(scope ResetScope, reportLen int) ([]byte, error) {
	if reportLen < HeaderSize {
		return nil, fmt.Errorf("report length %d is smaller than the %d-byte header", reportLen, HeaderSize)
	}
	r := make([]byte, reportLen)
	r[0] = RequestMagic
	r[1] = CmdSetFactoryReset
	r[2] = byte(scope)
	return r, nil
}

// resetSettleDelay is the pause the bundle takes after a factory reset before
// touching the Device again (rs: `new Promise(k => ja(k, 100))`).
const resetSettleDelay = 100 * time.Millisecond

// FactoryReset performs one factory reset of the given scope
// (SET_FACTORY_RESET): one fire-and-forget report — the firmware answers
// nothing, exactly like the bundle's factoryReset — followed by the bundle's
// 100 ms settle before the wire is used again. The caller owns the
// confirmation ritual (the write gate and the typed confirmation of
// ADR-0003 / spec user story 27): this sends the scope it is given.
func (d *Device) FactoryReset(ctx context.Context, scope ResetScope) error {
	report, err := ResetReport(scope, d.t.ReportLength())
	if err != nil {
		return err
	}
	d.xferMu.Lock()
	defer d.xferMu.Unlock()
	if err := d.t.SendReport(0, report); err != nil {
		return fmt.Errorf("SET_FACTORY_RESET: %w", err)
	}
	select {
	case <-time.After(resetSettleDelay):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
