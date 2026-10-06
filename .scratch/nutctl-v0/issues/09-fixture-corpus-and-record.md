# 09: Fixture corpus + `nutctl fixtures record`

**What to build:** Recording becomes corpus-building: any live session against real hardware turns into committed fixtures that keep the offline tests honest and close the remaining protocol unknowns with evidence.

**Blocked by:** 03

**Status:** resolved

- [x] `nutctl fixtures record` captures request/response pairs from a live session with metadata (firmware version, connection type, capture method)
- [x] Recorded fixtures drive the fake Device in tests round-trip
- [x] The corpus covers every command used by the v0 read and write paths
- [x] The protocol notes are reconciled against recorded reality; any discrepancy is documented with the evidence

## Comments

**2026-10-06 — implemented (resolved).** On `main`, next to tickets 01–08 and 12.

- **`nutctl fixtures record` (docs/capture.md Method A, `internal/cli/fixtures.go`)**
  turns a live session into corpus fixtures: a **passive recorder** on the Transport
  seam (`recordEnumerator`/`recordTransport` decorate `hid.Enumerator` — the recorded
  session runs unchanged, its own write gate included) logs every report in order and
  writes one fixture per transfer to `<out>/<cmd>/<case>.{req,res}.hex` + `meta.json`
  with the ticket's metadata: **firmware version** (decoded from the session's own
  GET_DEVICE_INFO exchange), **connection type** (the Model's Connection), **capture
  method** (`active-probing`), plus model, report length and a source line naming the
  session. Default session = the v0 read path (probe + one full checked read pass,
  exit 1 on a failing self-check — the `save` precedent: the checks gate writes, not
  records); `nutctl fixtures record <command> …` records **any** invocation's session,
  so writes (`load`), reset and notify traffic are captured whenever a hardware run
  produces them. A command seen twice in one session gets `<case>-2`, `<case>-3` …
  (test 2's read-back is `get_key/load-2`).
- **The corpus format is now written, not just read** (`internal/fixture.Save`): the
  exact inverse of `Load`, pinned round-trip on a synthetic exchange **and over all 23
  committed fixtures**. `internal/protocol` gained the recording half of the wire:
  `WireReport`/`WireTransfer`/`SplitTransfers` (a wire log grouped into per-transfer
  exchanges — the transfer layer's own cmd/addr matching replayed backwards: chunked
  transfers split by the last-packet flag and address continuity, responses matched to
  the transfer awaiting their cmd, notify/unparseable input kept as unsolicited
  exchanges, never dropped; an unparseable REQUEST is a loud error — a parse failure is
  a bug report, ticket 10's rule), `ParseRequest` (the inverse of `Request.Marshal`),
  `CommandName` (the §3 name table) and `Reassemble` exported. `hidfake.Replay` replays
  response-only exchanges as injected notify traffic.
- **Round trip, as the ticket asks:** `TestFixturesRecordRecordsTheReadPath` requires
  the recorder's output to be the committed corpus **byte for byte** from the same
  session, and `TestRecordedFixturesDriveTheFakeDeviceRoundTrip` replays freshly
  recorded fixtures on a fresh fake Device and gets **the same `get settings --json`
  output and the same State File** as the committed fixtures produce — recorded
  fixtures drive the fake Device end to end. `TestFixturesRecordWrapsACommandAndRecordsTheWritePath`
  covers the write path (`load`'s five SET exchanges recorded and byte-identical to
  `testdata/captures/set_*`), `TestFixturesRecordRecordsResetExchanges` covers
  ticket 08's follow-up (a recorded `reset --keys` session yields a
  `set_factory_reset` fixture — request-only, no response).
- **Corpus coverage** (`internal/protocol/corpus_test.go`): the corpus carries fixtures
  for every command the v0 read and write paths put on the wire — the probe + five
  blocks (GET_DEVICE_INFO/KEY/FN_KEY/LED_EFFECT/CUSTOM_LED_DATA/GAME_MODE) and their
  five SET counterparts, 11 commands — each named by meta `cmd`, and every committed
  exchange follows the documented §2 framing (headers parse, response mirrors the
  request chunk's `lenOrType`/`addr`, one transfer = one complete §4 block, SET acks
  echo the chunk payload). **Boundary (deliberate):** SET_FACTORY_RESET and the notify
  commands are not in the corpus — per ticket 08's follow-up they get recorded "when
  hardware runs happen" (the recorder captures them now; a factory reset is
  destructive and is never run unattended, and the reset frame is pinned against the
  bundle's encoder in `reset_test.go`). The read/write-path command list is the test's
  explicit spec table (the strict fake already fails any path that starts using a
  command the corpus does not script).
- **Protocol notes reconciled** (docs/protocol.md §7, with the evidence): confirmed the
  §2 framing/reassembly, the §4 field maps (device info decodes to the lsusb identity,
  `reportRate` enum), the SET_LED_EFFECT forced markers and the raw keymap writes —
  and documented **three discrepancies with fixture evidence**: (1) §2's 32-byte/24-byte
  framing is the fallback, the NUT87 speaks 64-byte reports/56-byte chunks (§6.5
  pointer); (2) **the Per-Key block tail is not the keymap tail** — the stable marker is
  `AA 55` at 510..511 only; 508..509 are entry 127 data and the write path forces
  `7F 00` there (`get_custom_led_data/nut87` reads `00 00 AA 55`,
  `set_custom_led_data/nut87` writes `7F 00 AA 55`) — this refines §4 and §6.8 in
  place; (3) **the SET ack echoes the chunk payload and nothing more** — padding past
  the chunk length is the Device's own (`set_key/nut87`'s final ack continues
  `02 00 3D 00 …` where the request padded zeroes).
- **Key decisions:** (1) the recorder wraps sessions rather than owning them — probing
  and corpus building are the same activity for *every* command (ticket 08's
  follow-up), and the write gate is never the recorder's business; (2) grouping lives
  in `internal/protocol` (the framing is protocol domain, and ticket 10's importer
  groups URBs "the same way"); (3) the corpus coverage surface is an explicit table in
  the test, annotated with the code path each command comes from — deriving it from
  the fake's scripts would be circular, since those scripts ARE the corpus; (4) a
  fixture whose Device cannot be identified or whose probe does not decode is refused
  loudly (a recording that cannot be evidence is not committed), while a failing
  self-check is recorded anyway (the `get`/`save` precedent).
- **Testing (the agreed seams):** everything external — the recorder through the
  Transport seam (`internal/cli/fixtures_test.go`), the corpus and its framing at the
  protocol seam (`corpus_test.go`, `wire_test.go`), the format round trip in
  `internal/fixture`. Full suite green, `-race` clean.

**Code review (two-axis, post-implementation):** fixed the Standards findings (the
only non-test `device.Identify` error being discarded — refusals are loud now;
`ParseRequest`/`CommandName` moved beside `Marshal`/the command table in `frame.go`;
the `save` parameter clump → `recordOpts`; `wdeps` renamed) and the Spec findings
(the `lenOrType` mirror claim was documented but unpinned — the corpus test now pins
it per report; the ack-echo check could panic on an overloaded length byte — now
bounds-checked; `docs/capture.md` gained the spec's scrub rule and the stale-numbered-
case caveat). **Suppressed findings (deliberate):** the reset/notify corpus boundary
(ticket 08's own follow-up wording, and no hardware run may be manufactured);
`WireReport.Sent bool` (a direction flag, documented on the field); the test-side
`loadDir` helper duplication across two test packages; the recorder's summary writer
on `save`'s signature (stdout is the CLI's, not a recording option).

**Follow-ups (not this ticket):** record `set_factory_reset` and notify fixtures on
the next hardware run (`nutctl fixtures record … reset --keys` / `… watch`) and then
extend the corpus coverage table — ticket 10's `import-pcap` reuses
`protocol.SplitTransfers` for the URB grouping; README `fixtures` docs → ticket 11.
