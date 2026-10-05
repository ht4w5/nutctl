# 02: Read full state — layout table, `nutctl get`, self-checks

**What to build:** The full readable state of the Device becomes visible — every Key Slot on both Layers, the Lighting Effect, Per-Key RGB, and Settings — together with the self-checks that make later writes safe (the ADR-0003 precondition).

**Blocked by:** 01

**Status:** resolved

- [x] `nutctl get keymap [--layer fn]` shows each Key Slot with its current Key Action, including Knob gestures
- [x] `nutctl get lighting` shows the Lighting Effect and Per-Key RGB; `nutctl get settings` shows Settings including Report Rate
- [x] All `get` commands provide stable `--json` output
- [x] The Model layout table (keys, Knob slots, Fn-disabled Key Slots) is data, not code — another Model's table could be added without code changes
- [x] The three self-checks (Device Identity match, keymap decodes to the physical layout, Lighting Effect check code) run as one reusable verification step and fail loudly when wrong
- [x] Unknown Key Action page types decode to an explicit unknown marker rather than crashing or silently dropping

## Comments

**2026-10-05 — implemented (resolved).** Branch `feat/02-read-state-and-self-checks`
(subtasks 02a–02d + one review-fix pass, merged).

- `internal/protocol`: `Keymap`/`FnKeymap`/`LightingEffect`/`PerKeyRGB` typed reads
  (GET_KEY 18 / GET_FN_KEY 22 / GET_LED_EFFECT 19 / GET_CUSTOM_LED_DATA 20) and codecs:
  128×4 Key Slots → `KeyAction` with the page-type table; page types outside the table
  (16..127) decode to an explicit `UNKNOWN` marker with raw bytes preserved;
  pageType ≥ 128 is `FUNC_V2`. Synthesized wire fixtures under
  `testdata/captures/{get_key,get_fn_key,get_led_effect,get_custom_led_data}/`
  (64-byte `nut87` + 32-byte multi-chunk `seed-32byte`) until ticket 09 can record real ones.
- `internal/device`: `layouts/nut87.json` (87 keys, Knob gestures 13 clockwise/15 press/
  14 counter-clockwise, Fn-disabled 1..12) loaded by `LayoutFor` via `go:embed` — adding
  another Model's table is adding a JSON file (proven by a test with a second table).
  `RunChecks` runs the three self-checks (ADR-0003) as one step, reports every failure
  as one `self-check failed: …` line each, and is the precondition ticket 03's write gate
  will reuse.
- `internal/cli`: `nutctl get keymap [--layer base|fn]` (every Key Slot 0..127 listed,
  Knob gestures as rows, Fn-disabled slots marked), `get lighting` (Effect + Per-Key RGB),
  `get settings` (Report Rate as `8K`), all with stable `--json` (fixed keys, camelCase,
  2-space indent, string enums, full-string golden tests). Each `get` does one full read
  pass and runs `RunChecks` once; on failure nothing reaches stdout (exit 1).
  `get macros` answers with an explicit "deferred to v0.1" (spec feature slicing).

**Key decisions:** (1) check 2 is *structural*, not value-equality — any non-DEFAULT Key
Action (including `UNKNOWN` markers) must sit in a layout slot, so remapped Devices pass
but framing/offset bugs fail loud; (2) `--json` key presence is part of the stability
contract — `name`/`knob` are null rather than omitted; (3) one full read pass per `get`
so the three checks genuinely run as one step on the read path.

**Code review — suppressed findings (deliberate):** CLI view structs duplicating
`protocol` json tags (spec: `--json` shape is a contract and must not drift with wire
types; `infoJSON` precedent); byte-identical typed reads and read-pass error wraps
(follow the established `Info()`/`Settings()` pattern); json tags on protocol types
(`DeviceInfo`/`Settings` precedent; ticket 03 State Files will marshal them); `layer`
string flag and `KnobEntry.Gesture` string (CLI-flag-sized concepts); the `get macros`
Named deferral.

**Follow-ups (not this ticket):** docs/protocol.md §4's "wheel keys occupy slots 13/14/15
= vol+/mute/vol−" reads positionally as 14=mute, but the bundle's `wheelKeys` (authoritative)
says 13=Vol+ (CW), 15=Mute (press), 14=Vol− (CCW) — the layout table follows the bundle;
reconcile the protocol note with recorded evidence in ticket 09. README's `get` docs →
ticket 11. The read-only/write-gate half of ADR-0003 → ticket 03 (this ticket delivers
its precondition).

**2026-10-05 — hardware verification pass (post-review).** A live read-only pass against
the real NUT87 (USB 0c45:880c, firmware 1.20, 64-byte reports) verified the decode
pipeline end to end (Esc/F1/F2/F12 slots 0/1/2/12 = HID 0x29/0x3a/0x3b/0x45; Knob
slots 13/14/15 = Consumer Vol+/Vol−/Mute) and falsified two synthesized assumptions:

- **22 out-of-layout Key Slots carry real Key Actions on both Layers** (29–31, 45–47,
  61–63, 77–79, 93–98, 101, 109–111 — the keyList gaps plus 109–111; e.g. 29–31 =
  keypad 0x53–0x55). This is the shared firmware matrix with sibling Models, not
  corruption. Fix: the layout table gained data-only `extraSlots`; check 2 now allows
  bound slots ∈ physical keys ∪ Knob ∪ extraSlots and adds a misalignment detector:
  both keymap blocks end `00 00 AA 55` (bytes 510..511).
- **The check code is at the block tail, and the LED Effect's is `00 00` on hardware**
  (`GET_LED_EFFECT` 14..15 = `00 00`; both keymap blocks and GET_CUSTOM_LED_DATA end
  `00 00 AA 55`). Fix: check 3 accepts `AA 55` (vendor SET path) or `00 00` (observed
  factory/unwritten state) and names the bytes otherwise. The closing evidence — that
  SET_LED_EFFECT's read-back shows `AA 55` — is assigned to ticket 03's write path.
  docs/protocol.md §6.8 and docs/capture.md Method A now carry this reconciliation.

The four `nut87` fixture cases (get_key/get_fn_key/get_led_effect/get_custom_led_data)
were **replaced with real recordings** (captureMethod: active-probing) — the synthesized
payloads had encoded the falsified assumption. `get` now prints the requested view to
stdout even when the checks fail (ADR-0003 gates *writes* on the checks) with the
`self-check failed:` lines on stderr and exit 1.

**Concurrent-reader hazard (diagnosed + fixed).** One diagnostic detour: two `nutctl`
processes reading the same hidraw node at once interleave their chunk responses (the
kernel gives each input report to exactly one reader), corrupting multi-chunk reads
(measured: 4/4 concurrent `get keymap` runs corrupted vs 0/8 sequential; the block-tail
detector caught it every time). There is **no** transfer-engine bug — sequential reads
are reliable. Fix: `internal/hid` takes an exclusive `flock` at `Open` and returns
`ErrBusy` otherwise (spec user story 29's device-busy error), with an actionable CLI
hint. Verified live: a racing second process is refused cleanly instead of silently
corrupting reads.
