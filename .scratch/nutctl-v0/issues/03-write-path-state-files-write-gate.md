# 03: Write path + State Files + write gate

**What to build:** Writes become possible and provable: put changes in a State File, `load` it, and get verified proof it landed — guarded by the write gate (ADR-0003) and with no file activity beyond what the user asked for (ADR-0005).

**Blocked by:** 02

**Status:** resolved

- [x] `nutctl save <file>` writes a State File with the `model`, `firmware`, `schema`, `state` envelope
- [x] `nutctl load <file>` applies a State File covering all four blocks (Base Layer, Fn Layer, Lighting Effect + Per-Key RGB, Settings) in batched writes, and prints the read-back verification diff
- [x] `load` refuses a State File saved from a different Model; on firmware mismatch it warns but proceeds
- [x] The session's first write requires self-checks passing and offers the golden-read prompt (save current state before writing); a noisy skip flag exists
- [x] Writes are refused while the Device reports bootloader/firmware-recovery state
- [x] Round-trip property holds: save → load → save yields identical State Files

## Comments

**2026-10-05 — implemented (resolved).** On `main`, next to tickets 01/02.

- `internal/protocol`: the write codecs and SET operations. `EncodeKeymap` (raw wire
  bytes per Key Slot are the truth — decode preserves them, encode reproduces them),
  `EncodeLightingEffect`/`EncodePerKeyRGB`/`EncodeSettings` byte-exact with the
  bundle's SET encoders: SET_LED_EFFECT forces byte 4 `0xFF` and the check code
  `0xAA 0x55`, SET_CUSTOM_LED_DATA writes `ledId = entry index`, SET_GAME_MODE zeroes
  offsets 0/10/12/13 (all quoted in docs/protocol.md §4). `SetKeymap`/`SetFnKeymap`/
  `SetLightingEffect`/`SetPerKeyRGB`/`SetSettings` write one complete block per
  batched transfer with the vendor engine's per-command timeouts (SET_KEY 1000 ms,
  SET_CUSTOM_LED_DATA 2000 ms, 2000 ms on frameVersion-1 firmware — the timeout table
  is now wired, was a TODO).
- `internal/device`: the **State File** (`statefile.go`) — pretty-printed JSON with the
  `model`/`firmware`/`schema`/`state` envelope, schema 1, strict presence validation
  (every envelope field and every block named in the error), `Check` refusing a
  wrong-Model file and warning on firmware mismatch. The state block carries only
  Device state: **the wire markers the SET format forces are deliberately not in the
  file** (Lighting Effect driverSetting/check code, Per-Key ledId byte — they are
  derived on write and never round-trip), which is what makes the round-trip property
  hold whatever the Device reports at those positions. `Apply` is the batched writeback
  (PLAN Phase 4's device-model half), shared by `load` now and the TUI's apply later.
  Key Action rows are `{"type","params","raw"}`: raw required and authoritative,
  type/params optional readability fields, validated against raw — a row that
  disagrees with itself is a loud error.
- `internal/cli`: `nutctl save <file>` (one code path with the golden read — the
  ADR-0003 golden is just a State File), `nutctl load <file>` with the write gate:
  wrong-Model refusal → bootloader/firmware-recovery refusal → self-checks must pass →
  golden-read prompt `save current state to ./golden-<ts>.json? [Y/n]` (offered, never
  forced — ADR-0005; **n** dismisses, EOF refuses the write with the flag named, and
  the noisy `--i-know-what-im-doing` skips the prompt with a loud warning) → four
  blocks in batched writes (Base Layer, Fn Layer, Lighting Effect + Per-Key RGB as one
  Lighting block — two transfers on the wire —, Settings) → read-back verification diff
  (State File → Device, Key Slots named from the layout table), exit 1 on any
  difference. Flags and the file argument parse in either order.

**Hardware verification pass (docs/capture.md Method A, real NUT87 `0c45:880c`,
firmware 1.20, 64-byte reports).** Golden read first (raw blocks to /tmp + `nutctl get`
snapshots), then each block written BACK to the Device and read back: **all five SET
blocks read back byte-for-byte identical to what the wire carried**, and the Device
acks every chunk (the ack echoes the block — recorded). This closed the two open
protocol questions with evidence: **§6.8** — after SET_LED_EFFECT the Device reads
`0xAA 0x55` at offsets 14..15 (and `0xFF` at offset 4): the check code IS a "written"
flag, and the keymap/Per-Key block tail `00 00 AA 55` survives writes byte-for-byte;
**§6.7** — SET commands land and verify without `COMMUNICATION_START/END` on firmware
1.20. Also observed: SET_CUSTOM_LED_DATA's forced `ledId = index` sticks (factory
blocks read `0` throughout). docs/protocol.md §2/§4/§6.7/§6.8 updated with all of it.
The write exchanges are committed as `testdata/captures/set_{key,fn_key,led_effect,
custom_led_data,game_mode}/nut87.*`; the protocol tests replay them byte-for-byte
against our encoder output (the read fixtures and the write fixtures describe the same
Device state). Full CLI end-to-end on hardware: `save → load (golden prompt, writes,
clean read-back verification) → save` yields **identical State Files**. A build-tagged
rerunnable pass lives in `internal/protocol/device_hw_test.go` (`go test -tags device`).

**Key decisions:** (1) the State File stores state, never wire markers — the three
forced positions are excluded by design, so the round-trip property is independent of
firmware normalization (and `load`'s verification compares exactly what is state);
(2) golden-prompt EOF refuses the write rather than defaulting either way — writes
unlock only after an explicit decision (answer, or the noisy flag), while a declined
offer (`n`) unlocks without touching the disk (ADR-0005's "offered, never forced");
(3) `save` writes the State File even when the self-checks fail (exit 1 + loud lines)
— the `get` precedent: the checks gate writes to the Device, and a failing check is
exactly when a snapshot is worth keeping.

**Code review (two-axis, post-implementation):** fixed both hard Standards findings
(`parseWithFile` naming the wrong argument on extra args; "all four blocks" comments
against a "5 blocks" print), the Spec finding that `load` claimed "loaded" before the
read-back proved it (the success line now prints only after verification), the
batched-writeback placement (moved to `device.Apply`), the triplicated check block
(`checkedRead`), the `KeyAction` derive-from-type/params input surface (removed — raw
is required), and the cli.go split (save/load/gate/diff → `internal/cli/statefile.go`).
**Suppressed findings (deliberate):** `save` exiting 1 on failed checks (the `get`
precedence above); fixture recording behind the build tag (`NUTCTL_FIXTURES_OUT`)
despite ticket 09 owning `fixtures record` — the recording is opt-in, manual and
build-tagged, and it is how this pass's evidence is reproduced; the test-side
`reassembledSize`/`blockDiff` dir-string switches (test glue over two fixture
families); device-package unit tests for the State File format (the spec's Testing
Decisions mandate CLI-seam external behavior for State Files — no new seam).

**Follow-ups (not this ticket):** the write gate's check half and the full-state apply
should move under `internal/device` alongside the TUI's dirty-tracked apply in tickets
05–07 (the prompt itself stays with whichever UI asks); README `save`/`load` docs →
ticket 11; `reset` (ticket 08) reuses this write gate verbatim.
