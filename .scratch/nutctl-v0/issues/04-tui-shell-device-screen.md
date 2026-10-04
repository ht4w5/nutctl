# 04: TUI shell + Device screen

**What to build:** The TUI becomes the front door (ADR-0004): bare `nutctl` opens a tabbed interface where the Device introduces itself and State Files are explicitly saved and loaded. Read-only states and errors are visible, never silent.

**Blocked by:** 03

**Status:** ready-for-agent

- [ ] Bare `nutctl` opens the TUI; the four screens are navigable with `1`–`4` and `tab`
- [ ] The Device screen shows connection state, Device Identity, firmware version, Report Rate, and charge/battery status
- [ ] The Device screen offers explicit Save and Load actions; no file is ever written without a user action
- [ ] The write-gate prompt and read-only banner follow ADR-0003; a bootloader Device shows a clear read-only state
- [ ] Permission failures show an actionable udev hint; device-busy and wrong-Model failures are clearly worded
- [ ] TUI behavior is tested through the fake Device with golden rendered frames (the second test seam)
