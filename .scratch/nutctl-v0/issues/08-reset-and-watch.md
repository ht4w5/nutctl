# 08: Recovery — `nutctl reset` + `nutctl watch`

**What to build:** Recovery and observability from the CLI: factory reset when the configuration is garbage, and live device notify traffic when chasing quirks.

**Blocked by:** 03

**Status:** resolved

- [x] `nutctl reset --keys|--lighting|--macros|--all` maps onto the firmware's reset scopes
- [x] Reset requires typed confirmation naming the scope being destroyed
- [x] `nutctl watch` streams device notify commands live
- [x] Reset is refused on a bootloader/firmware-recovery Device

## Comments

**2026-10-06 — implemented (resolved).** On `main`, next to tickets 01–07.

- **`nutctl reset --keys|--lighting|--macros|--all` maps onto the firmware's
  reset scopes** (`internal/protocol/reset.go`, `internal/cli/reset.go`):
  SET_FACTORY_RESET is one fire-and-forget report `AA 0F <scope> 00 …`
  zero-padded to the report length — byte-exact with the bundle's own
  `factoryReset`/`xn` encoder (the scope rides in header byte 2, no payload,
  no response awaited, the bundle's 100 ms settle before the wire is used
  again; `docs/protocol.md` §4 quotes it). Scopes are the bundle's
  FACTORY_RESET_TYPE: KEY_RESET 1, LIGHTING_RESET 2, MACRO_RESET 4,
  RESET_ALL 255 — CLEAR_CALIBRATION (5) is a Hall-effect scope, deliberately
  not offered. The mapping is pinned byte-for-byte at the Transport seam for
  64- and 32-byte reports (report length from the descriptor, never
  hardcoded).
- **Reset requires typed confirmation naming the scope being destroyed**
  (spec user story 27): the prompt names what dies (`factory reset keys
  destroys both Layers' key bindings (every Key Slot) — type "reset keys" to
  confirm:`) and the phrase must come back exactly. The confirmation is the
  one gate with **no flag around it** — scripts pipe the phrase on stdin;
  EOF, a bare Enter and any typo refuse with "nothing was reset" and nothing
  on the wire. `--i-know-what-im-doing` skips only the golden read (its
  documented meaning).
- **`nutctl watch` streams device notify traffic live**
  (`internal/cli/watch.go`, spec user story 30): the unsolicited input
  reports the bundle's listeners match (`docs/protocol.md` §4, extracted):
  `55 FA <type>` device notify, `55 FC 04/05/06` = 2.4G disconnect / device
  reset (the keyboard's own "Restore factory settings (FN R_ALT ESC)" chord
  announces itself) / 2.4G sleep, and the out-of-band `A6 FF 01` wake
  report. Each line is timestamped, named and whole as hex; any other input
  report keeps a visible `input report` line (PLAN's "live input reports" —
  a late response or garbage is shown, not hidden). It ends on Ctrl-C
  (clean, exit 0) or with the Device (exit 1, "disconnected"). The protocol
  layer gained the unsolicited stream (`Device.Notifications` — bounded
  buffer, dropped when nobody reads: observability, never a transfer) and
  the `ParseNotify`/`NotifyText` classification.
- **Reset is refused on a bootloader/firmware-recovery Device** (spec user
  story 28) — the write gate's refusal verbatim, before any prompt and any
  report. Reset is CLI-only: no foot-gun in the TUI (US 27).
- **The recovery case is first-class** (the ticket's "configuration is
  garbage"): a state that fails the self-checks is resettable (the pre-reset
  read is unchecked), and a Device that cannot even be read is still
  resettable — loud warning, no proof. The golden read is still offered
  before the destruction (ADR-0003's prompt verbatim, offered never forced —
  the dying state is exactly when a snapshot is worth having), and the
  read-back counts what changed per in-scope block (`base: 1 of 128 Key
  Slots changed`, `lighting effect: changed`, `macros: not readable in this
  build (Macros are deferred to v0.1)`) — the proof is the read-back, never
  the fire-and-forget write. When the read-back is unavailable the reset
  still stands: loud warning, exit 0.
- **Testing (the agreed seam):** everything is external behavior through
  the Transport seam (`internal/cli/reset_test.go`, `watch_test.go`) —
  exit codes, stdout/stderr and the byte-exact reset report the fake
  receives: scope mapping, typed-confirmation refusals with nothing on the
  wire (incl. "the confirmation has no skip flag"), bootloader refusal,
  golden-read offer/decline, self-check-garbage recovery, read-back counts,
  watch's decoded lines and watch's disconnect exit. `internal/protocol`'s
  codec seam pins the frame and the notify table the established way (golden
  bytes from the bundle's encoders; `ParseNotify` refusals included).
  `hidfake` gained `Inject` (unsolicited input reports) — the seam's lever
  for notify traffic. Full suite green, `-race` clean.
- **Key decisions:** (1) the typed confirmation is unskippable — US 27 says
  "behind typed confirmation" without exception and a script can pipe the
  phrase; the noisy flag's documented meaning is the golden read (US 5), so
  it is not widened (this reverses the draft where it skipped both prompts —
  the Spec review caught it). (2) Self-checks do NOT gate a reset and the
  golden read is offered rather than required — recorded in **ADR-0003's new
  reset exception**, as its own amendment clause demands; the bootloader
  refusal and the prompt stay verbatim. (3) The read-back reports counts
  instead of dumping `StateDiffs` rows (a full `--all` diff is hundreds of
  per-key lines), with the "which bytes are state" rule shared with the diff
  as `device.ChangesBetween` — one rule, one module. (4) watch timestamps
  every line: a quirk chase needs "when", and the raw bytes always sit
  beside the decoded name.

**Hardware note (2026-10-06).** The wire frame is NOT hardware-verified: a
factory reset is destructive (Macros are unrecoverable in v0) and is never
run against hardware unattended. The frame is the bundle's own encoder
byte-for-byte (`docs/protocol.md` §4 marks the evidence extracted, not
observed). Read-only parts were exercised live on the real NUT87
(`0c45:880c`, firmware 1.20, `/dev/hidraw10`): `nutctl watch` streams,
prints its header and stops cleanly on Ctrl-C (exit 0), and `nutctl list`
probes as before.

**Code review (two-axis, post-implementation):** fixed both axes' hard
findings — the ADR-0003 amendment clause (the reset deviation had lived only
in code comments; the ADR now carries its own reset exception) and the
skip-flag foot-gun (typed confirmation mandatory, flag narrowed to the
golden read) — plus the Spec finding that `docs/protocol.md` claimed notify
reports are "never dropped" while the bounded stream drops unread ones
(reworded), and the Standards smell calls (three parallel `ResetScope`
switches → one scope table; `resetSummary`'s duplicated comparison rule and
Feature Envy on protocol internals → `device.Changes`/`ChangesBetween` beside
`StateDiffs`; the `flag`-shadowing closure renamed; the comment claiming
`protocol/text.go` for renderers that live with their type;
"Hall-effect-board" against CONTEXT.md's Avoid list).

**Suppressed findings (deliberate):** watch prints non-notify input reports
(PLAN's own surface says "live input reports / notify cmds (250/252)"; hiding
a late response during a quirk chase is what watch exists to prevent);
`ResetScope`'s renderers stay methods beside their type (the
`ReportRate.String`/`KeyActionType.String` precedent — `text.go` holds the
cross-type phrase renderers) with the scope strings in one table;
`resetScopeOf`'s four bools stay a signature (they ARE the four flags);
`Destroys` interprets the firmware's scope names while the reset semantics
are bundle-extracted, not hardware-verified (the confirmation names the
scope word and the read-back proves what actually died);
`ChangesBetween` does not refactor `StateDiffs` onto a shared field table
(adjacent, same rule, one module — a field-table rewrite is churn without a
second consumer); watch carries no `--json` (the ticket asks for a live
stream; the `--json` stability contract covers `get`/`list`/`info`).

**Follow-ups (not this ticket):** a hardware pass once an owner is ready to
destroy and `load` back (Macros are the unrecoverable part — `--macros` and
`--all` need a human go-ahead); README docs for `reset`/`watch` → ticket 11;
ticket 09's `fixtures record` can capture reset/notify exchanges when
hardware runs happen (the corpus has none — the frame is pinned against the
bundle's encoder instead).
