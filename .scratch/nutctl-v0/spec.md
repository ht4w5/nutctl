# Spec: `nutctl` v0 — NUT87 configurator for Linux (CLI + TUI)

Status: ready-for-agent

## Problem Statement

I own a WEIKAV NUT87 keyboard, and the only official configurator is a web app that
does not work on Linux. There is no way for me to remap keys, adjust lighting, or
change Settings (Report Rate, key delay, sleep) without borrowing a working
Windows/browser environment. The vendor tool also keeps its saved configurations in
browser storage, which is fragile and invisible. I want to own my keyboard's
configuration from my own machine, with proof that each change actually landed.

## Solution

`nutctl` (module `github.com/ht4w5/nutctl`): a single Linux binary that talks to the
NUT87 over USB directly. The Device is the source of truth: `nutctl` reads its state,
renders it in a tabbed TUI (Device · Keys · Lighting · Settings), lets me edit locally
and explicitly apply changes with read-back verification, and — only when I ask —
saves or loads complete device state to a State File at a path I choose. A CLI covers
the same ground for scripting and recovery. No preset library, no background files,
no cloud.

## User Stories

1. As an NUT87 owner, I want `nutctl` to detect my keyboard over USB and confirm its Model from the firmware-reported Device Identity, so that I know I am configuring a NUT87 and not a sibling board sharing the USB product id.
2. As an NUT87 owner, I want running `nutctl` with no arguments to open the TUI, so that interactive configuration is the default experience.
3. As an NUT87 owner, I want the tool to open my Device read-only until protocol self-checks pass (identity match, keymap decodes to the expected layout, lighting check code correct), so that a protocol misunderstanding cannot corrupt my configuration.
4. As an NUT87 owner, I want the tool to offer to save the current device state to a State File before the session's first write, so that I can always restore a known-good configuration.
5. As an NUT87 owner, I want a clear, dismissible escape hatch for skipping the golden save, so that I stay in control of files on my disk without the tool nagging.
6. As an NUT87 owner, I want the Device screen to show connection state, Device Identity, firmware version, Report Rate and charge/battery status, so that I can see what the keyboard reports about itself.
7. As an NUT87 owner, I want explicit Save/Load-to-file actions on the Device screen, so that state files are things I request, never things the tool decides to write.
8. As an NUT87 owner, I want the Keys screen as a table of keys with their current Key Actions, so that I can scan what every key does.
9. As an NUT87 owner, I want a static ASCII map of the TKL layout beside the table, so that I can orient myself physically without a graphical canvas.
10. As an NUT87 owner, I want to switch between the Base Layer and the Fn Layer in the Keys screen, so that I can view and edit both binding tables.
11. As an NUT87 owner, I want to rebind any Key Slot on the Base Layer to a keyboard key, so that the board matches my muscle memory (e.g. Caps → Ctrl).
12. As an NUT87 owner, I want to rebind Key Slots on the Fn Layer, so that I can put media and Function bindings where I want them.
13. As an NUT87 owner, I want the Knob's three gestures (clockwise, press, counter-clockwise) listed as rows like any other Key Slot, so that remapping the knob is the same workflow as remapping a key.
14. As an NUT87 owner, I want to bind keyboard keys, consumer/media keys, mouse buttons and Functions to a Key Slot, so that I cover the Key Action types the firmware offers.
15. As an NUT87 owner, I want the Model's layout table to mark Key Slots that cannot be bound on the Fn Layer, so that the picker never offers bindings the firmware will reject.
16. As an NUT87 owner, I want to edit the Lighting Effect (mode, primary and secondary colors, brightness 1–6, speed 1–6, direction), so that the ambient lighting matches my taste.
17. As an NUT87 owner, I want to edit Per-Key RGB for the supported slots, so that I can highlight specific keys.
18. As an NUT87 owner, I want to edit Settings — Report Rate (1K/4K/8K), key delay, sleep, Fn switch — so that the keyboard behaves the way I work.
19. As an NUT87 owner, I want edits to be local until I explicitly apply them, so that trying out a configuration is cheap and reversible.
20. As an NUT87 owner, I want a status bar showing pending changes, so that I always know the Device and my edits disagree.
21. As an NUT87 owner, I want to revert my pending edits from the Device's actual state, so that one key gets me back to truth.
22. As an NUT87 owner, I want every apply verified by reading the affected state back from the Device and showing me the diff, so that I can trust the tool's success message.
23. As an NUT87 owner, I want to navigate the four screens with `1`–`4` and `tab`, so that moving around costs no thought.
24. As an NUT87 owner, I want `nutctl save <file>` and `nutctl load <file>` outside the TUI, so that I can switch configurations from a script or a shell alias.
25. As an NUT87 owner, I want `load` to refuse a State File saved from a different Model, so that a file from a friend's board cannot scramble my key ids.
26. As an NUT87 owner, I want `load` to warn (but proceed) when the State File's firmware version differs from my Device, so that I am informed without being blocked.
27. As an NUT87 owner, I want factory reset (keys / lighting / macros / all) as a CLI-only action behind typed confirmation, so that recovery exists without a foot-gun in the TUI.
28. As an NUT87 owner, I want the tool to refuse writes while the Device reports bootloader/firmware-recovery state, so that a half-updated keyboard is never made worse.
29. As an NUT87 owner, I want actionable errors for permission problems (udev rule hint), device-busy, and wrong-Model devices, so that I can fix my environment instead of guessing.
30. As an NUT87 owner, I want `nutctl watch` to stream device notify traffic (e.g. 2.4G disconnect notifications) live, so that wireless quirks are debuggable later.
31. As a CLI scripter, I want `nutctl get keymap|lighting|macros|settings` with stable `--json` output, so that I can inspect state from scripts.
32. As a CLI scripter, I want `nutctl list` to enumerate connected candidates and identify the Model of each, so that I can script around multiple keyboards.
33. As a future maintainer, I want a committed corpus of recorded request/response fixtures, so that `go test ./...` is meaningful without hardware.
34. As a future maintainer, I want `nutctl fixtures record` to turn any live session into fixtures, so that probing the protocol and building the test corpus are the same activity.
35. As a future maintainer, I want a fake Device adapter that replays fixtures and injects faults, so that the CLI and TUI are testable end-to-end offline.
36. As a future maintainer, I want golden-frame tests of the TUI screens, so that layout regressions in the Keys table, ASCII map and status bar are caught.
37. As a future maintainer, I want State File round-trip and validation tested as external behavior, so that the format's contract (schema, Model refusal, firmware warning) stays stable.
38. As a future maintainer, I want the State File format versioned via a schema field, so that files remain loadable as the tool evolves.

## Implementation Decisions

- **Modules**: a transport module owning the `Transport` seam (report I/O, hotplug); a protocol module (the deep module: framing, chunked transfers, request/response matching, codecs); a device model (typed state, dirty tracking, batched writeback); a TUI module (Bubble Tea); a CLI entry point (`nutctl` binary, bare invocation opens the TUI).
- **Transport adapter is pure-Go hidraw, no cgo anywhere** (ADR-0006): raw report read/write plus ioctl for report descriptors and feature reports; enumeration via udev/sysfs; a udev rule ships with the project. A hidapi adapter can be added later behind the same seam (it would then be the project's only cgo).
- **Scope**: Linux-only, personal tool with public-repo hygiene, MIT (ADR-0002). Versioning is `v0.x.y` while unstable; tags are milestones, not releases. Distribution is source-only (`go install …@latest` or `go build`).
- **Protocol contract** (extracted from the vendor bundle, documented in the protocol notes): output report id 0, 32-byte reports, 8-byte request header (`0xAA`, cmd, len, addr u16 LE, 3 header bytes with last-packet flag), 24-byte chunk payloads, `0x55` responses matched by cmd/addr, 3 retries at 500 ms (longer on frame-version-1 firmware). Chunking must take report length from the HID descriptor, never hardcode 32.
- **v0 command surface**: device info (48-byte field map), game-mode/Settings block (56 bytes; Report Rate wire enum `1K→3, 2K→4, 4K→5, 8K→6`, NUT87 offers 3/5/6), keymap and Fn Layer (128 Key Slots × 4 bytes with the 16-entry Key Action page-type table), Lighting Effect (16 bytes, `0xAA 0x55` check code at offsets 14–15 — also used as a self-check), Per-Key RGB (128 entries × 4 bytes `ledId,r,g,b`), factory reset scopes.
- **Device Identity disambiguation**: Models share USB product ids (NUT87 vs NUT75 on `0x880C`); matching is on the firmware-reported product name fields, and a mismatch is a clear refusal, never a best guess.
- **Write gate** (ADR-0003): self-checks must pass and a golden-read prompt (`save current state to ./golden-<ts>.json? [Y/n]`) precedes the session's first write; a noisy skip flag exists. OTA/flash commands are never implemented.
- **No preset management** (ADR-0005): the Device is the source of truth; State Files are written/read only at user-chosen paths via explicit `save`/`load`; no auto-apply, no library, no history.
- **State File format**: pretty-printed JSON with a `model`, `firmware`, `schema`, `state` envelope; `load` refuses wrong-Model files and warns on firmware mismatch.
- **CLI surface in v0**: `list`, `info`, `get`, `save`, `load`, `reset`, `watch`, `fixtures` — deliberately **no granular `set`**; the TUI covers interactive writes.
- **TUI** (ADR-0004): Bubble Tea with bubbles/lipgloss; four tabbed screens (`1`–`4`, `tab` cycles): Device, Keys, Lighting, Settings. Keys is table-driven with a static ASCII TKL map pane; Knob gestures are table rows. Edits are local until `a` applies and `r` reverts; the status bar shows pending changes.
- **Session model**: one goroutine owns the wire (no concurrent transfers); TUI state updates arrive as program messages; the TUI is a view over the device model, not a second source of truth.
- **Feature slicing**: Macros are deferred to v0.1, advanced keys (SOCD/MT/TGL/CB) to v0.2, 2.4G dongle support beyond that. Deferral is by design: nothing gets harder by waiting.

## Testing Decisions

- A good test asserts **external behavior only**: bytes on the wire, files on disk, rendered frames, exit codes, stdout. Never internal function call order, internal struct shapes, or UI view-model internals.
- **Primary seam — `Transport`**: the fake Device adapter replays committed fixtures and injects faults (dropped responses, garbage headers). Through this single seam we test: codecs against golden wire bytes; transfer retry/timeout behavior; device-model dirty tracking, apply and read-back verification; CLI commands end-to-end (including State File validation and the write gate); TUI sessions driven with key messages (apply/revert, pending-changes state, gate prompts).
- **Secondary seam — rendered TUI frames**: golden strings for the Keys table + ASCII TKL map, the Lighting form, and the status bar. A TUI's rendered frame is external behavior; layout regressions are invisible through the Transport seam. (This is the one deliberate second seam; everything else collapses into the Transport seam.)
- State File round-trips and refusal behavior are tested as external behavior through the CLI seam with temporary files — no new seam.
- All tests run without hardware. Hardware integration tests live behind a build tag, run manually.
- **No prior art exists in the repo** (greenfield); these seams establish the testing shape for every later feature (v0.1 macros will test through the same two seams).

## Out of Scope

- Macros (deferred to v0.1), advanced keys SOCD/MT/TGL/CB (v0.2), Knob-adjacent extras beyond its three gestures.
- 2.4G dongle support (different product id, `24G`-variant commands, sleep/wake) — later than v0.2. Bluetooth: nothing to do; the Device has none.
- OTA firmware update (feature-report protocol, CRC16) — never in these phases; too much brick risk.
- TFT/GIF animation editors, music/audio-reactive lighting, AI lighting features.
- Hall-effect features (DKS, rapid trigger, calibration) — the NUT87 doesn't expose them.
- Preset management of any kind (ADR-0005): no library, no named store, no auto-apply.
- Granular CLI `set` verbs; Windows/macOS builds (ADR-0002); prebuilt binaries; i18n; support commitments.

## Further Notes

- Two protocol facts remain open and are resolved by evidence during implementation,
  never by guessing: the semantics of remaining Settings bytes (key delay units, sleep
  encoding, system mode, power mode) and whether `COMMUNICATION_START/END` must wrap
  sessions. Fallbacks are documented in the protocol notes.
- Fixtures recorded from live hardware are committed to the repo (protocol bytes, no
  personal data); anything sensitive found in a dump is scrubbed from metadata rather
  than dropping the fixture.
- Vocabulary follows `CONTEXT.md`: Device, Model, Device Identity, Key Slot, Key
  Action, Layer, Knob, Macro, Advanced Key, Function, Lighting Effect, Per-Key RGB,
  Settings, Report Rate, State File. Decisions recorded in ADR-0002 through ADR-0006
  are binding for this spec (ADR-0001, the Gio choice, is superseded).
