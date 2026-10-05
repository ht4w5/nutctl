# Reads before writes: self-validation gate

The tool opens a Device read-only until protocol self-checks pass (identity matches a
known Model, keymap decodes to the expected physical layout, lighting check code reads
`0xAA 0x55` at offsets 14..15) and a golden read of the full device state has been
saved to a **State File** (explicit path, no library — see ADR-0005). Writes unlock per
session after that. OTA/flash commands are never implemented in these phases.

Why: no vendor software runs on this machine, so there is no cross-check against the
official implementation — a wrong encoder would silently corrupt keyboard state, and
the golden read is the only way back. This is a deliberate deviation from the usual
"driver just writes" expectation.

Consequences: the first write session starts with the current state saved to a State
File; some CLI/TUI paths refuse to write
with an explanation instead of failing later. If this policy is ever relaxed, say so
here.

## Exception: `nutctl reset` (2026-10-06, ticket 08)

Factory reset is the recovery for the state this gate protects, so two of its halves
are relaxed there — recorded here as this ADR requires:

- **Self-checks do not gate a reset.** A configuration that fails them is exactly what
  the reset repairs; requiring them would refuse recovery where recovery is the point.
  The read is taken unchecked, and a Device that cannot even be read is still
  resettable — with a loud warning and no proof.
- **The golden read is offered, not required.** Same prompt, same "offered, never
  forced" answer (ADR-0005) — but when the state cannot be read at all there is
  nothing to save, and the reset proceeds without one.

What is NOT relaxed: a Device in bootloader/firmware-recovery state is never written
to (a half-updated keyboard is never made worse), and `nutctl reset` always demands a
typed confirmation naming the scope being destroyed (spec user story 27). The
confirmation is not skippable by any flag — scripts pipe the phrase on stdin — and the
read-back counts what changed, so the claim that the reset landed is proven, not
assumed.
