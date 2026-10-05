# 12: Bug — self-check 2 rejects a freshly factory-reset Device

**What it is:** Defect in ticket 02's self-check 2, surfaced by ticket 08's
`nutctl reset` on real hardware: after a factory reset, every read (`get`,
`save`, `load`, the TUI) exits 1 with

```
self-check failed: keymap block misalignment: GET_KEY bytes 510..511 are 0x00 0x00;
GET_FN_KEY bytes 510..511 are 0x00 0x00, want 0xAA 0x55 (the block-tail marker of a
well-aligned read) — the read is misaligned or the block is corrupt
```

**Blocked by:** —

**Status:** resolved

- [x] A freshly factory-reset Device passes the self-checks
- [x] The misalignment detector still catches real corruption
- [x] The post-reset wire state is recorded as fixtures and in docs/protocol.md

## Comments

**2026-10-06 — diagnosed and fixed (resolved).** On `main`, on top of ticket 08.

**Diagnosis (the real Device — read-only probes — after `nutctl reset` was run
several times on it):** the factory reset (SET_FACTORY_RESET) **clears the
block-tail bytes**. GET_KEY / GET_FN_KEY / GET_CUSTOM_LED_DATA all read
`00 00 00 00` at 508..511 where the pre-reset recording has `00 00 AA 55`,
and the LED Effect returns to its factory/unwritten markers (check code
`00 00`, driverSetting `00`, Per-Key ledId 0 everywhere). The keymap
contents are exactly the documented factory default matrix: 127 of 128
slots byte-identical to the committed pre-reset fixtures at the same
offsets (only slot 127's tail bytes differ), the pageType histograms match
§6.8's, and the reads are stable across repeats. The reset itself worked
perfectly — self-check 2's claim "`0xAA 0x55` = well-aligned read" was what
was wrong.

**Ranked hypotheses and how they were discriminated (diagnosis loop, live
read-only probes + fixture diff):**

1. **Factory reset clears the tail bytes; `0x00 0x00` is legitimate state**
   (confirmed — predicted only bytes 510..511 differ and everything else is
   byte-identical; observed exactly that, plus the LED markers returning to
   their documented factory/unwritten state).
2. **Read misalignment** (falsified — predicted shifted/garbage bytes and
   unstable reads; 127/128 slots byte-identical at the same offsets, stable
   ×3, slot 0 = `02 00 29 00` exactly where it belongs).
3. **Storage corruption from the repeated resets** (falsified — predicted
   scrambled contents; contents are the clean default matrix).
4. **Wrong block offsets for this firmware** (falsified — only the tail
   region differs).
5. **Flapping reads racing the reset's 100 ms settle** (falsified — three
   reads identical).

**Fix (`internal/device/selfcheck.go`, check 2):** the tail pair is a
**two-state detector, exactly like the LED check code** (self-check 3, which
already accepts both): `0xAA 0x55` (flashed/written) or `0x00 0x00`
(factory/reset) pass; anything else still fails loudly as misalignment. The
`00 00` = corruption claim was falsified by hardware and is gone from the
message and docs.

**Evidence committed:** the real post-reset read path is recorded as
`testdata/captures/*/nut87_post_reset` (read-only capture, 2026-10-06,
firmware 1.20 — meta carries provenance) and `docs/protocol.md` §4/§6.8
record the observation. The capture harness was throwaway (ticket 09's
`fixtures record` supersedes it) and is deleted.

**Regression tests (both agreed seams, red before the fix):**
`internal/cli/reset_test.go` `TestReadsWorkOnAFreshlyResetDevice` replays the
real post-reset fixtures through the Transport seam and requires `get` to
succeed (the user's exact symptom was its red output, byte for byte);
`internal/device/selfcheck_test.go` `TestRunChecksAcceptsThePostResetState`
pins the same at the domain seam. The unit cases that encoded the old rule
(zero tail = corruption) now use genuinely corrupt pairs (`0x12 0x34`,
`0x56 0x78`, `0x00 0x55` — still caught), plus a new case that the
factory/reset tail passes.

**Follow-ups (not this ticket):** what the tail bytes *mean* (written flag
vs Key Slot 127 factory data) stays unproven — the fixture meta and
docs/protocol.md say so explicitly; `load` restores the `AA 55` tail from a
pre-reset State File (SET_KEY reproduces the block bytes), so the marker is
self-healing for anyone who kept a golden read.
