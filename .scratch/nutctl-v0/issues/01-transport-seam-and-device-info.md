# 01: Scaffold + Transport seam + `nutctl list`/`info`

**What to build:** The first end-to-end path to the Device: `nutctl` finds the keyboard over USB, proves the protocol speaks correctly, and reports who the Device is. This is the enabler slice — everything above the `Transport` seam must be exercisable offline through a fake Device replaying fixtures (ADR-0006: pure-Go hidraw, no cgo anywhere).

**Blocked by:** None (can start immediately)

**Status:** ready-for-agent

- [ ] `nutctl list` enumerates connected candidates and identifies each one's Model from the Device Identity
- [ ] `nutctl info` prints Model, firmware version, Report Rate and related Device facts, with stable `--json` output
- [ ] A sibling board sharing the USB product id (e.g. NUT75) is refused with a clear wrong-Model error, never configured
- [ ] Requests are framed, chunked, and matched to responses per the protocol notes; retry/timeout behavior is observable under injected faults (dropped response, garbage header)
- [ ] `go test ./...` passes with no hardware attached, via the fake Device and committed seed fixtures
- [ ] A udev rule and README note let an unprivileged user open the Device on Linux
