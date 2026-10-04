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
