---
status: superseded by ADR-0004
---

# Go + Gio for the driver and configurator

We build the configurator as a Go desktop application with [Gio](https://gioui.org),
and keep everything except the HID transport adapter pure Go. Alternatives considered:
Fyne, GTK bindings, a local web UI behind a Go daemon (the vendor's own approach is a
web app — which is exactly what fails us on Linux). Gio was chosen by the project
owner; we accepted it because it keeps one language across protocol, CLI and UI, and
its immediate-mode drawing suits the custom keyboard canvas widget the UI needs.

Consequences: we write key-cap/knob widgets ourselves instead of using a widget
library; cgo appears only in `internal/hid` (hidapi), so the protocol core stays
testable and portable.
