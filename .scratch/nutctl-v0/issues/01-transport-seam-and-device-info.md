# 01: Scaffold + Transport seam + `nutctl list`/`info`

**What to build:** The first end-to-end path to the Device: `nutctl` finds the keyboard over USB, proves the protocol speaks correctly, and reports who the Device is. This is the enabler slice — everything above the `Transport` seam must be exercisable offline through a fake Device replaying fixtures (ADR-0006: pure-Go hidraw, no cgo anywhere).

**Blocked by:** None (can start immediately)

**Status:** resolved

- [x] `nutctl list` enumerates connected candidates and identifies each one's Model from the Device Identity
- [x] `nutctl info` prints Model, firmware version, Report Rate and related Device facts, with stable `--json` output
- [x] A sibling board sharing the USB product id (e.g. NUT75) is refused with a clear wrong-Model error, never configured
- [x] Requests are framed, chunked, and matched to responses per the protocol notes; retry/timeout behavior is observable under injected faults (dropped response, garbage header)
- [x] `go test ./...` passes with no hardware attached, via the fake Device and committed seed fixtures
- [x] A udev rule and README note let an unprivileged user open the Device on Linux

## Comments

**2026-10-05 — implemented (resolved).**

- Scaffold: module `github.com/ht4w5/nutctl`, `cmd/nutctl`, `internal/{hid,protocol,device,cli,fixture,hidfake}`.
- `internal/hid`: the `Transport` seam (`SendReport`/`Reports`/`ReportLength`/`Close`) plus the
  pure-Go hidraw adapter (ADR-0006: sysfs enumeration + report-descriptor parse + `/dev/hidraw`
  I/O, no cgo, no ioctls) and the scripted fake adapter in `internal/hidfake` (fixture replay +
  fault injection: dropped response, garbage header).
- `internal/protocol`: framing (`Request.Marshal`/`ParseResponse` mirror the bundle's
  `xn`/`iu` byte for byte), the transfer engine (chunking from the report length, per-chunk
  request/response matching, 500 ms / 3 retries, `frameVersion`-dependent timeouts), and the
  `DeviceInfo` (48-byte) + `Settings` (56-byte) codecs.
- `internal/device`: Model identification from the Device Identity (NUT87 supported, NUT75
  recognized but refused with a `wrong Model` error before it is ever opened) and the
  identity self-check.
- `internal/cli`: `list`/`info` with stable `--json`, actionable errors (udev hint on
  permission denied, `--device` for ambiguity).
- `udev/60-nut87.rules` + README section; MIT LICENSE.

**Hardware verification (docs/capture.md Method A):** the real NUT87 attached to the dev
machine was probed read-only through the shipping binary: `nutctl list` identifies it as
NUT87 and reports firmware 1.20; `nutctl info` reports Report Rate 8K etc. The exchanges
are committed as `testdata/captures/{get_device_info,get_game_mode}/nut87.{req,res}.hex`
(64-byte reports — the Device does **not** use the bundle's 32-byte reference size) and
drive the tests; `docs/protocol.md` §6 items 5 and 7 were closed with this evidence. The
synthesized `seed-32byte` cases remain as multi-chunk framing goldens. Response
`lenOrType`/`addr` semantics are now observed facts rather than placeholders.

**Identity matching note:** Model matching uses the USB product string — the field the
vendor bundle calls `productName` and matches its config names (`3141-34828-NUT87.ts`)
against; it is reported by the Device firmware over USB enumeration. `GET_DEVICE_INFO`
carries no name fields — its `manufacturer`/`product` are numeric u16s (32/2 on firmware
1.20), surfaced in `info` for reference but not matched on. `device.Verify` cross-checks
the firmware-reported vid/pid against the enumerated ones.

**Vocabulary note:** `Status: resolved` marks completion (the five triage roles are
about queue state; `resolved` is the tracker's completion word, as in the wayfinding
conventions).
