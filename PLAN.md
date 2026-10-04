# Plan: NUT87 open-source driver + configurator (Go + Bubble Tea)

Goal: talk to the WEIKAV NUT87 over USB (`0x0C45:0x880C`) directly from Go, and ship a
Linux TUI configurator built with [Bubble Tea](https://github.com/charmbracelet/bubbletea)
(module `github.com/ht4w5/nutctl`, ADR-0004).

**v1 scope (settled):** USB only, core features — key remap (Base + Fn layer), lighting
(effect + per-key RGB), settings (report rate 1K/4K/8K, key delay, sleep). Macros are
v1.1, advanced keys (SOCD/MT/TGL/CB) v1.2, 2.4G dongle support later (different PID,
`..._24G` command variants, sleep/wake — the device has no Bluetooth). See
`CONTEXT.md` for vocabulary (device state lives on the device; files are explicit
**State Files**, no preset management — ADR-0005) and
`docs/adr/` for the scope decisions.

Ground truth already extracted from the vendor bundle lives in [`docs/protocol.md`](docs/protocol.md).
That document is the contract; this plan is how we verify it and build on it.

---

## Guiding constraints

1. **Never guess on the wire.** Every encoder/decoder we write is validated against a
   real device exchange (a fixture recorded from probing or a `usbmon` capture) before
   it is trusted. Unknowns in `docs/protocol.md` §6 are closed by evidence, not by
   experiment-on-user-hardware.
2. **Everything above the transport seam runs without hardware.** The protocol logic
   must be exercisable by tests and by a CLI against a fake device.
3. **CLI first, TUI second.** A `nutctl` probe tool proves the protocol long before
   we invest in terminal UI polish, and it stays useful forever (debugging, scripting, CI).

---

## Architecture (modules and seams)

```
cmd/nutctl        CLI: probe, read, write, watch, dump-fixtures
internal/hid        Transport seam: report I/O, hotplug
  ├ hidraw adapter  (pure Go: /dev/hidraw + ioctl + udev)  ← no cgo anywhere
  └ fake adapter    (in-memory, scripted device)
internal/protocol   Framing + codecs  ← the deep module
internal/device     Device model: state, dirty tracking, writeback, presets
internal/tui        Bubble Tea screens over device state (ADR-0004)
testdata/           golden captures (hex + json), fixture scripts
```

**`protocol` is the deep module** — the one we spend design effort on. Its interface
should be roughly:

```go
type Transport interface {          // seam: two adapters = real seam (hidapi, fake)
    SendReport(id uint8, b []byte) error
    Reports() <-chan []byte         // input reports
    Close() error
}

type Device struct { /* ... */ }
func Open(t Transport, opts Options) (*Device, error)

// typed operations; each hides framing, chunking, retries, reassembly
func (d *Device) Info(ctx) (DeviceInfo, error)
func (d *Device) Keymap(ctx) (Keymap, error)
func (d *Device) SetKeymap(ctx, Keymap) error
func (d *Device) Lighting(ctx) (Lighting, error)
func (d *Device) SetLighting(ctx, Lighting) error
func (d *Device) Macros(ctx) ([]Macro, error)
func (d *Device) SetMacros(ctx, []Macro) error
func (d *Device) Settings(ctx) (Settings, error)
func (d *Device) SetSettings(ctx, Settings) error
func (d *Device) FactoryReset(ctx, Scope) error
```

Everything ugly — 24-byte chunking, `0xAA/0x55` framing, 500 ms/3-retry request-response
matching, 512-byte payload reassembly, single-slot reads via pre-built headers,
`frameVersion`-dependent timeouts — lives inside `protocol` and never leaks into
`device` or `ui`. Callers pass structs, not bytes. That is the depth we are paying for;
tests and the UI cross the same seam.

`device` adds session semantics on top: one goroutine owns the wire (no concurrent
transfers), models go dirty and get flushed on apply, reconnect/re-init after hotplug.

`ui` is intentionally shallow and dumb: it renders `device` state and emits intents.

---

## Phase 0 — Ground truth ✅ mostly done

- [x] Decode vendor bundle, extract framing, command table, payload layouts
      → `docs/protocol.md`
- [ ] Keep the decoded bundle in-repo as the reference corpus (`decoded/`, already
      extracted from `weikav.driveall.cn.har`), with a note on provenance and a script
      that re-extracts it from the HAR.
- **Exit criteria:** `docs/protocol.md` reviewed; open questions §6 listed as issues.

## Phase 1 — Verification corpus (no browser required)

Context: the official web configurator doesn't work under Linux, so there is no
in-browser capture path. It doesn't block us — the protocol is already extracted
statically (`docs/protocol.md`), including the advanced-key param semantics and the
custom-LED layout. Capture is for **verification and the §6 leftovers**, not discovery.
See [`docs/capture.md`](docs/capture.md) for full workflows.

- **Method A — active probing (primary):** implement `internal/protocol` from the docs,
  then read the real device with `nutctl`. Responses are self-validating
  (vid/pid vs `lsusb`, 87-key layout check, LED effect check-code `0xAA 0x55` at
  offsets 14..15). `nutctl fixtures record` turns every exchange into a fixture —
  probing and corpus building are the same work.
- **Method B — kernel USB capture (cross-check):** `usbmon` + Wireshark/tshark on the
  host while the vendor app runs in a **Windows VM with USB passthrough** (Chrome
  WebHID works there). The hypervisor uses usbfs/libusb, so host `usbmon` sees every
  URB. `nutctl fixtures import-pcap` reassembles the URBs into our framed
  request/response fixtures.
- **Method C — optional 5-minute browser retry** (udev rule, Chrome, UA spoof) —
  nice-to-have only, nothing depends on it.
- Corpus: one fixture per command in `testdata/captures/<cmd>/<case>.{req,res}.hex`
  plus `meta.json` (firmware version, connection type, capture method) — **committed to
  the repo** (protocol bytes, no personal data; scrub `meta.json` if anything sensitive
  ever appears in a dump).
- **Exit criteria:** fixtures for GET/SET of device-info, game-mode/settings, key,
  fn-key, led-effect, custom-led, macro, factory-reset; `docs/protocol.md` §6 items
  5–7 closed or explicitly deferred. Phase 2 starts in parallel — Method A *is* Phase 2
  code running against hardware.

## Phase 2 — Transport + protocol in Go (TDD against fixtures)

- `internal/hid`: `Transport` seam, **pure-Go hidraw adapter** (ADR-0006: `/dev/hidraw*`
  + `ioctl` for report descriptors/feature reports, udev/sysfs for enumeration),
  device enumeration
  (VID/PID + usage-page filter `0xFF68/0xFF80/0xFF60/0xFF00/0xFF01/0xFF1B`,
  product-name disambiguation `NUT87` vs `NUT75` on the shared PID).
- `internal/protocol`:
  1. framing: `marshalChunk` / `parseResponse` (pure functions, table tests);
  2. transfer: chunker + reassembler + retry/timeout matcher;
  3. codecs: structs ⇄ bytes for each payload (golden tests from Phase 1 fixtures);
  4. fuzz tests on `parseResponse` and every decoder.
- Fake transport: scripted request→response fixtures (replays the Phase 1 corpus),
  plus fault injection (dropped response → retry, garbage header → error).
- **Exit criteria:** `go test ./internal/...` passes with **no hardware attached**;
  `nutctl dump` on real hardware reproduces the vendor app's state byte-for-byte.

## Phase 3 — `nutctl` (probe + scripting)

```
nutctl list                 # enumerate, identify Model
nutctl info | dump all      # human + --json
nutctl get keymap|lighting|macros|settings [--layer fn]
nutctl save <file.json>     # device state → State File
nutctl load <file.json>     # State File → device
nutctl reset --keys|--lighting|--macros|--all   # typed confirmation
nutctl watch                # live input reports / notify cmds (250/252)
nutctl fixtures record ...  # feeds Phase 1 corpus automatically
```

- **Exit criteria:** a full read-modify-write cycle via CLI against real hardware,
  verified by read-back; `--json` output is stable enough for scripting.
- **Write gate (ADR-0003):** self-checks must pass, then the tool offers
  `save current state to ./golden-<ts>.json? [Y/n]` before the session's first write;
  a noisy `--i-know-what-im-doing` flag can skip it.
- v1 CLI surface is exactly the verbs above — **no granular `set`** until a real
  scripting need shows up (the TUI covers interactive writes).

## Phase 4 — Device model + State Files

- `internal/device`: typed state (`Keymap` with `KeyAction` sum type over the 16
  pageTypes, `Lighting`, `Macro`, `Settings`), dirty tracking, batched writeback
  (keymap = one 512-byte SET_KEY; macros = pointer table + action blob as the vendor
  does), and State File serialization: pretty-printed JSON with a
  `{"model","firmware","schema","state"}` envelope; `load` **refuses** a wrong-Model
  file and **warns** on firmware mismatch (explicit `save`/`load` only — no store,
  ADR-0005).
- The golden read from ADR-0003 is just a State File — one code path for save, load
  and restore-after-experiments.
- Port the NUT87 layout table (87 keys + knob slots 13/14/15, key ids 0..108 with the
  gaps the vendor config uses) as data, generated from the bundle config if possible.
- **Exit criteria:** round-trip property test (read → decode → encode → write → read =
  equal); State File save/load round-trips via CLI.

## Phase 5 — Bubble Tea TUI (ADR-0004)

v1 screens: **Device · Keys · Lighting · Settings** (the Device screen shows live
state plus explicit Save/Load-to-file actions).
Macros = v1.1, Advanced = v1.2 — independent slices, nothing gets harder by deferring.

- Stack: `bubbletea` + `bubbles` (table/viewport/textinput) + `lipgloss`; one program,
  one event loop; the device goroutine posts `tea.Msg` state updates.
- Screens are tabs: `1`–`4` jump, `tab` cycles — Device · Keys · Lighting · Settings.
- Edits are local until applied: status bar shows pending changes, `a` writes to the
  device, `r` reverts from device state.
- `Keys`: table-driven key remap (`bubbles` table: key → current binding → picker),
  with a static ASCII TKL map as a side pane; knob gestures are rows like any key
  (settled, round 3).
- `Lighting`: effect list + form (primary/secondary colors, brightness 1–6, speed 1–6,
  direction, per-key RGB as a value grid).
- `Macros` (v1.1): list (100 slots), action editor (press/release, keycode, delay).
- `Advanced` (v1.2): SOCD / MT / TGL / CB editors (param semantics known from the
  bundle, but each editor is its own design).
- `Settings`: report rate 1K/4K/8K, key delay, sleep, fn switch.
- The TUI is a view over `internal/device` state; all writes pass the write gate
  (ADR-0003); read-only mode until self-checks pass.
- **Exit criteria:** core operations doable against the fake transport; tests assert
  rendered frames (golden frame strings), no screenshots.

## Phase 6 — Hardening & release

- Linux delivery: udev rule for `0x0C45` access (ship the rule file), hotplug/reconnect,
  bootloader mode (`firmwareStatus == 1` → read-only "recover" UI, no accidental writes).
- Integration tests behind build tag `device` (skipped in CI, run locally with hardware).
- CI: Linux only — `go test ./...` + vet/staticcheck.
- Distribution: **source-only** — `go install github.com/ht4w5/nutctl/cmd/nutctl@latest`
  or `go build`; prebuilt binaries only if someone asks (goreleaser later).
- Versioning: **v0.x.y while unstable** (semver v0 = no stability promise); tags are
  milestones, not releases.
- **Exit criteria:** a tagged `v0.x` that someone else can build and use. Windows/macOS
  builds are a later checkbox (ADR-0002), not a v1 goal.

## Out of scope (later / never)

- **2.4G dongle support** (different PID `0xFEF9`, `..._24G` command variants,
  sleep/wake, `GET_24G_DISCONNECT_NOTIFY`) — later than v1.2. **Bluetooth: nothing to
  do — the device has none** (dead FUNC rows in the vendor app notwithstanding).
- **OTA firmware update** (feature-report protocol in `sn_isp_lib.wasm`, CRC16-CCITT):
  high brick risk, separate project.
- TFT/GIF animation editor, music/audio-reactive sync, AI lighting presets.
- Hall-effect features (DKS/RT/rapid-trigger/calibration) — NUT87 doesn't expose them,
  though the protocol supports them for sibling boards.

---

## Risks

| Risk | Mitigation |
|---|---|
| Vendor firmware drift changes payloads | fixtures record firmware version; `frameVersion`/`version` gates in codecs |
| Official web app unusable on Linux (no in-browser capture) | protocol extracted statically from the bundle; verify via active probing + host `usbmon` under a Windows VM (`docs/capture.md`) |
| NUT75/NUT87 share USB PID | match on firmware `product` string, not PID alone; refuse with a clear error |
| Report size differs per OS/variant (32 vs 64) | chunker takes report length from the descriptor (as the vendor code does), never hardcodes 32 |
| Writes during bootloader mode brick the device | `firmwareStatus == 1` → open read-only |
| hidraw quirks (permissions, ioctl/kernel differences) | ship the udev rule; the Transport seam keeps a hidapi adapter trivially addable (that would be the project's only cgo) |
| We mis-decode an unknown (esp. custom LED matrix) | no encoder ships until a capture fixture proves it; UI features gate on it |
| Concurrent access / other tools holding the device | single-owner goroutine + clear "device busy" error |

## Milestone order (summary)

`docs → captures → protocol lib → CLI → device model → TUI → release`

Each milestone ends in something a user can run; nothing is "90% done" for long.
