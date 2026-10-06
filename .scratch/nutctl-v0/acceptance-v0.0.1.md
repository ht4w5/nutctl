# Acceptance run — v0.0.1 release gate

The spec's end-to-end scenario, run by hand against real hardware as ticket
11's release gate. Every step below was driven through the real `nutctl` UI
(TUI frames captured from a terminal) and checked against the Device with
independent `nutctl get` read-backs.

- **Date:** 2026-10-06
- **Device:** WEIKAV NUT87 over USB (`0c45:880c`, firmware 1.20, Report Rate
  8K at start), Linux, hidraw via the shipped udev rule
- **Binary:** built from this tree (`go build ./cmd/nutctl`)

## Before anything: golden state

`nutctl save` wrote the pre-run State File (the same thing the write gate
offers as `./golden-<ts>.json`), and `nutctl get keymap`, `nutctl get
keymap --layer fn`, `nutctl get lighting` and `nutctl get settings` were
captured for comparison.

## Install verified

Before the scenario, both install paths of the README were run from clean
caches (fresh `GOPATH`, `GOMODCACHE`, `GOCACHE`; nothing pre-warmed):

- `go install github.com/ht4w5/nutctl/cmd/nutctl@latest` — resolves the
  module path over the module proxy, builds, and the resulting binary runs
  (`nutctl help`).
- `go install ./cmd/nutctl` from a checkout of this tree — the exact tree
  being tagged builds and installs from scratch (Go 1.27 toolchain building
  the module's declared `go 1.24.2`).

Caveat, stated plainly: the `@latest` form installs the newest **published**
state of the module; `@v0.0.1` resolves once the milestone tag is pushed to
GitHub. Both were verified on this (Linux) machine with cold caches — the
part of "clean Linux machine" an agent can actually stand in for.

## Scenario

### 1. Remap on both Layers plus the Knob (TUI, Keys screen)

| Key Slot | Before | After |
|---|---|---|
| base 15 — knob press | consumer key "Mute" | consumer key "Play /Pause" |
| base 48 — Caps | keyboard key "Caps" | keyboard key "L-Ctrl" |
| fn 38 — Y | keyboard key "Y" | keyboard key "L-Ctrl" |

Rebind picker per row (`enter` → filter → `enter`); `space` toggled the Layer
for the fn edit. All three stayed local until apply: the status bar listed
the three pending changes and the rows carried `*`.

### 2. Lighting Effect and Per-Key RGB (TUI, Lighting screen)

- Effect mode `11 Flowing with the Waves` → `10 Colorful Interchange`
- Per-Key RGB entry 0 (Esc): `#000000` → `#ff0000` (color editor, `255` on
  the red channel)

### 3. Report Rate (TUI, Settings screen)

`8K` → `4K` (`←` on the Report Rate row).

### 4. Apply — write gate and read-back

`a` raised the write gate (ADR-0003), the session's first write:

```
Write gate (ADR-0003) — the session's first write
  save current state to ./golden-20261006-193211.json? [Y/n]
```

`y` saved the golden read and applied. **Finding (below):** the keymap and
lighting blocks were written and verified, but the settings block's write
(Report Rate) re-enumerates the keyboard's USB interface — the in-session
read-back failed with `write /dev/hidraw10: no such device` and the tool
reported the apply as an **error, not success** (it never claims a write it
did not verify). Independent read-back after the Device reappeared on
`/dev/hidraw11`:

```
$ nutctl get keymap | grep -E '^15 |^48 '
15    Mute (knob press)                     consumer key "Play /Pause"
48    Caps                                  keyboard key "L-Ctrl"
$ nutctl get keymap --layer fn | grep -E '^38 '
38    Y                                     keyboard key "L-Ctrl"
$ nutctl get settings | head -3
Report rate:           4K
$ nutctl get lighting   # Mode: 10 Colorful Interchange · entry 0: #ff0000
```

Every write landed; the diff against the pre-run captures shows the six
intended changes and nothing else user-visible (finding 2 explains the
wire-marker deltas).

### 5. Save and restore a State File

Fresh session on the re-enumerated Device (TUI, Device screen):

1. `s` → `saved NUT87 state (firmware 1.20) to /tmp/accept/after.json`
2. `l` → `after.json`, write gate `y` →
   `loaded /tmp/accept/after.json onto the NUT87 … — read-back verified: the
   Device matches the State File` (save→restore round-trip verified).
3. `l` → the pre-run golden State File, restoring the original configuration.
   The Report Rate write (4K → 8K) again re-enumerated the Device (again
   reported as an error, again landed — the Device reappeared on
   `/dev/hidraw10`).

Final read-back against the pre-run captures: keymap (both Layers), settings
and every user-visible lighting value match the start state exactly.

## Findings

1. **Report Rate writes re-enumerate the keyboard's USB interface.** The
   write lands, but the Device node disappears before the same-session
   read-back can run, so `nutctl` reports an honest error ("no such device")
   instead of a verified success. Recovery is trivial: re-run `nutctl` (the
   Device reappears on a new node) and read back. Documented in the README as
   a known v0 quirk; recorded as a follow-up (re-verify writes across
   re-enumeration) in the issue's `## Comments`.
2. **The SET path's forced wire markers differ from the factory read.** After
   any vendor-style write the Device reports the Lighting block's `Driver
   setting` as 255 and the check code as `0xaa 0x55` (factory/unwritten state
   reads `0` / `0x00 0x00`), and the Per-Key RGB table's LED id column reads
   `0..127` instead of all-zero. These are wire-format markers, not
   user-visible state — the same behavior ticket 03's hardware write
   round-trip recorded ("only the SET wire format's forced markers differ").
   Colors and the effect settings round-trip unchanged.

## Verdict

The v0 end-to-end scenario passes on real hardware: remap on both Layers and
the Knob, Lighting Effect and Per-Key RGB changes, a Report Rate switch, and
State File save/restore — every write verified by read-back (in-session where
the Device stays connected, by a fresh read where the Report Rate switch
moved the node). The release gate is green with finding 1 documented.
