# nutctl

Open-source driver and configurator for the WEIKAV NUT87, an 87-key TKL
keyboard with a knob (USB and 2.4G). The vendor's web configurator does not
work on Linux; `nutctl` talks to the keyboard over USB directly, in pure Go
(no cgo), and keeps its state where you can see it.

- **The Device is the source of truth.** `nutctl` reads the keyboard's state,
  and writes only when you explicitly ask. Complete state snapshots are plain
  files at paths you choose (State Files) — there is no preset library or
  background store.
- **Never guess on the wire.** The protocol is extracted from the vendor's own
  web app and documented in [`docs/protocol.md`](docs/protocol.md); every
  encoder/decoder is validated against recorded exchanges before it is trusted.
- **Everything works offline in tests.** A fake Device replays committed
  fixtures, so `go test ./...` is meaningful with no hardware attached.

## Status: v0, unstable

`nutctl` is **v0 while unstable**: versions are `v0.x.y`, and the tags are
milestones, not releases. Semver v0 is a promise of no promise — the CLI
output, the State File schema and the wire layer may change in any `v0.x`
release without a compatibility guarantee. What does hold inside v0:

- State Files carry a `schema` field: a file's format version travels with
  it, and a file whose schema the tool does not know is refused by name —
  never silently misread.
- `--json` output of `list`, `info` and `get` is treated as a scripting
  contract within v0.
- Every write ends in read-back verification of the affected state; where the
  Device cannot answer, the tool reports the write as **unverified** — it
  never reports success it did not see.

The full v0 scope lives in [`.scratch/nutctl-v0/spec.md`](.scratch/nutctl-v0/spec.md);
Macros (v0.1), advanced keys (v0.2) and 2.4G dongle support are deferred by
design. Linux only (ADR-0002); source-only distribution, no prebuilt binaries.

## Install

Requires Linux and Go 1.24.2 or newer (the module's declared version). From
the module path — nothing else to clone:

```sh
go install github.com/ht4w5/nutctl/cmd/nutctl@latest   # newest state
go install github.com/ht4w5/nutctl/cmd/nutctl@v0.0.1   # a milestone tag
```

Or from a source checkout:

```sh
git clone https://github.com/ht4w5/nutctl
cd nutctl
go build ./cmd/nutctl        # or: go install ./cmd/nutctl → $(go env GOBIN)
```

Either way the result is a single static binary (ADR-0006: pure-Go hidraw, no
cgo anywhere). Put it on your `PATH` and check it sees the keyboard:

```sh
nutctl list
```

## Let an unprivileged user open the Device

The hidraw interface is root-only by default. Install the shipped udev rule:

```sh
sudo cp udev/60-nut87.rules /etc/udev/rules.d/
sudo udevadm control --reload && sudo udevadm trigger
```

Then re-plug the keyboard and check:

```sh
nutctl list
```

Permission problems are an actionable error, not a crash: `nutctl` points at
this rule when it cannot open the Device.

## CLI

`nutctl` with no arguments opens the TUI. The commands below are its
scriptable side; there is deliberately **no `set` verb** — interactive writes
belong to the TUI, and bulk writes belong to `load` (ADR-0005).

```sh
nutctl list [--json]                   enumerate connected Devices and identify their Model
nutctl info [--json] [--device P]      Model, firmware version, Report Rate, Device facts
nutctl get keymap [--layer base|fn] [--json] [--device P]
nutctl get lighting [--json] [--device P]
nutctl get settings [--json] [--device P]
nutctl save <file> [--device P]        Device state → State File
nutctl load <file> [--device P] [--i-know-what-im-doing]
                                       State File → Device (write-gated, read-back verified)
nutctl reset --keys|--lighting|--macros|--all [--device P] [--i-know-what-im-doing]
                                       factory reset one scope (typed confirmation)
nutctl watch [--device P]              stream device notify traffic live (read-only)
nutctl fixtures record [--out DIR] [--case NAME] [--device P] [<command> …]
nutctl fixtures import-pcap <file> --model M [--out DIR] [--case NAME] [--usb B.D]
```

- `--device /dev/hidrawN` picks one Device when several are connected;
  without it `nutctl` selects the single supported Device or refuses
  ambiguity. A Device whose firmware-reported Device Identity is not an NUT87
  is refused by name — sibling Models share the USB product id, so the Model
  is never a best guess.
- `load` refuses a State File saved from another Model and warns (but
  proceeds) when its firmware version differs from the Device's.
- `reset` is CLI-only recovery: it asks you to type the scope back
  (`keys`, `lighting`, `macros`, `all`) before anything is destroyed. `--all`
  also destroys Macros — v0 State Files carry no Macros (deferred to v0.1),
  so those cannot be restored from a file. Save first.
- `fixtures` is the maintainer's corpus tooling (record live sessions, import
  `usbmon` captures); see [`docs/capture.md`](docs/capture.md).

## TUI

Bare `nutctl` opens the tabbed interface — **Device · Keys · Lighting ·
Settings** — rendering the state of one read pass plus one local edit buffer.
Edits stay local until you apply them.

```
1-4 / tab / shift+tab   switch screen        a   apply pending edits to the Device
s   save State File (path prompt)            r   revert edits from the Device's state
l   load State File onto the Device          q   quit
```

- **Device** — connection state, Device Identity, firmware version, Report
  Rate, battery status; the explicit Save/Load State File actions live here.
- **Keys** — a table of every Key Slot with its current Key Action beside a
  static ASCII map of the TKL layout. `space` toggles the Base/Fn Layer,
  ↑/↓/pgup/pgdn move, `enter` opens the rebind picker (←/→ kind: keyboard
  key, consumer key, mouse button, Function; type to filter; `enter` binds,
  `esc` cancels). The Knob's three gestures are table rows like any key.
  Key Slots the Model's layout table marks Fn-disabled cannot be rebound on
  the Fn Layer — the picker never offers a binding the firmware would reject.
- **Lighting** — the Lighting Effect form (mode, colors, brightness, speed,
  direction) and, on `space`, the Per-Key RGB grid; `enter` opens the color
  editor (↑/↓ channel, ←/→ ±1, pgup/pgdn ±16, digits type).
- **Settings** — Report Rate (the Model's offered set), key delay, sleep, Fn
  switch.

Every edit screen shows `*` on rows that differ from the Device and the
status bar lists the pending changes; `a` writes them and `r` throws them
away. An apply shows the diff it read back from the Device — the success
message is a verification of a write, not a hope about it.

## The write gate (ADR-0003)

Before anything else is written, `nutctl` opens the Device read-only and runs
protocol self-checks (identity match, keymap decodes to the expected layout,
lighting check code correct). Writes stay refused until they pass, and also
while the Device reports bootloader/firmware-recovery state. The one
exception is `nutctl reset` (ADR-0003): it is the recovery *for* a
configuration the self-checks reject, so its write goes out unchecked —
behind its typed confirmation and the golden-read prompt.

Before the **first write of a session** — an apply, a `load` or a factory
reset — the tool offers to save the current state first:

```
save current state to ./golden-<timestamp>.json? [Y/n]
```

`y` writes that golden State File and proceeds, `n` skips (the dismissible
escape hatch), `esc` cancels the write entirely. On the CLI,
`--i-know-what-im-doing` skips the prompt — loudly (the typed confirmation
of `reset` is never skipped). Every write ends in read-back verification of
the affected blocks; where the read-back cannot run, the tool reports the
write as unverified instead of claiming success (see the Report Rate quirk
below).

## State Files

A State File is pretty-printed JSON with a `model`, `firmware`, `schema`,
`state` envelope: one complete snapshot of the keyboard's configuration,
written only at a path you name (`save`, or the golden read above) and applied
only when you ask (`load`). There is no preset library, no history and no
background store (ADR-0005) — the Device is the source of truth, and the
golden read is just a State File.

### Known v0 quirk: Report Rate writes re-enumerate the keyboard

Changing the Report Rate resets the keyboard's USB interface mid-write: the
write lands, but the Device node disappears before the read-back can verify
it, and `nutctl` reports the write as unverified (it never claims success it
did not see). Re-run `nutctl` — the Device reappears on a new `/dev/hidrawN`
node — and read the state back to verify.

## Tests

```sh
go test ./...                # no hardware required
```

Tests run against a fake Device replaying the committed fixtures in
`testdata/captures/` (see [`docs/capture.md`](docs/capture.md) for how live
recordings are made). Hardware integration tests live behind the `device`
build tag (`go test -tags device ./internal/protocol/ -run Hardware`), run
manually against a real board.

## Documentation

- [`PLAN.md`](PLAN.md) — architecture, milestones, risks
- [`docs/protocol.md`](docs/protocol.md) — the wire protocol, extracted from the vendor bundle
- [`docs/capture.md`](docs/capture.md) — recording real device traffic
- [`CONTEXT.md`](CONTEXT.md) — domain vocabulary
- [`docs/adr/`](docs/adr/) — architecture decision records

## License

MIT, see [LICENSE](LICENSE).
