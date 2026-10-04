---
status: accepted
---

# TUI with Bubble Tea instead of a Gio GUI

The configurator UI is a terminal UI built with
[charmbracelet/bubbletea](https://github.com/charmbracelet/bubbletea) (with `bubbles`
and `lipgloss`), shipped as part of `nutctl` (module `github.com/ht4w5/nutctl`).
The project owner switched from Gio to this for simplicity — ADR-0001 is superseded:
one static binary, no windowing/GL stack, and the CLI and TUI share the same device
layer without an event-loop-vs-goroutine split.

Considered options: Gio (rejected — widget/canvas work is heavy for a config tool),
a local web UI (the vendor's approach; exactly what fails us on Linux).

Consequences: no graphical keyboard canvas — key remapping is list/form driven (exact
interaction model still open); per-key RGB "preview" is text at best; UI tests assert
rendered terminal frames instead of screenshots; the terminal becomes a hard
requirement for interactive use.
