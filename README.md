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

## Status

Early v0: the transport seam, the protocol core and the first CLI commands
(`list`, `info`) exist. The TUI and the read/write surface are landing per
[`PLAN.md`](PLAN.md); the full scope is in
[`.scratch/nutctl-v0/spec.md`](.scratch/nutctl-v0/spec.md).

## Build

```sh
go build ./cmd/nutctl        # or: go install github.com/ht4w5/nutctl/cmd/nutctl@latest
```

Linux only (ADR-0002); the binary is a single static executable (ADR-0006:
pure-Go hidraw, no cgo anywhere).

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

## Usage

```sh
nutctl list                  # enumerate connected Devices and identify their Model
nutctl list --json           # same, stable JSON for scripting
nutctl info                  # Model, firmware version, Report Rate, Device facts
nutctl info --json --device /dev/hidraw3
```

`nutctl info` refuses to touch a Device that is not an NUT87 — sibling boards
like the NUT75 share the USB product id, so the Model comes from the
firmware-reported Device Identity and a mismatch is a clear error, never a
best guess.

## Tests

```sh
go test ./...                # no hardware required
```

Tests run against a fake Device replaying the committed fixtures in
`testdata/captures/` (see [`docs/capture.md`](docs/capture.md) for how live
recordings are made). Hardware integration tests will live behind a build tag.

## Documentation

- [`PLAN.md`](PLAN.md) — architecture, milestones, risks
- [`docs/protocol.md`](docs/protocol.md) — the wire protocol, extracted from the vendor bundle
- [`docs/capture.md`](docs/capture.md) — recording real device traffic
- [`CONTEXT.md`](CONTEXT.md) — domain vocabulary
- [`docs/adr/`](docs/adr/) — architecture decision records

## License

MIT, see [LICENSE](LICENSE).
