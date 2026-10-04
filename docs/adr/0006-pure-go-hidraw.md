---
status: accepted
---

# Pure-Go hidraw transport, no cgo

The HID transport adapter talks to `/dev/hidraw*` directly — read/write for reports,
`ioctl` for report descriptors and feature reports, udev/sysfs for enumeration — in
pure Go. Decided with the project owner once scope settled on Linux-first (ADR-0002):
hidapi is itself just a wrapper over hidraw on Linux, so cgo would buy nothing today
while costing static builds and build simplicity.

Considered options: `sstallion/go-hid` (hidapi/cgo — the plan's original assumption),
`karalabe/hid` (unmaintained), `gousb`/libusb (too low-level for HID reports).

Consequences: `CGO_ENABLED=0` single static binary; we own enumeration and permissions
(the udev rule ships in-repo); the Transport seam keeps a hidapi adapter trivially
addable — and that adapter would be the project's only cgo — if Windows/macOS support
ever happens.

Implementation note (2026-10): the report descriptor is read from sysfs
(`.../device/report_descriptor`) instead of `HIDIOCGRDESC`, which is simpler and keeps
the same properties (pure Go, no cgo); no ioctls are needed for the v0 surface.
Feature reports (only the OTA path needs them — out of scope) would add
`HIDIOCGFEATURE`/`HIDIOCSFEATURE`.
