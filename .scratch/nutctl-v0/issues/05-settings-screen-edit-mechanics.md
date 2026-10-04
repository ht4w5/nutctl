# 05: Settings screen + edit mechanics

**What to build:** Settings become editable in the TUI with a trustworthy apply loop: edit locally, see what is pending, apply with verification, or revert from the Device. These edit mechanics are shared by every later edit screen.

**Blocked by:** 04

**Status:** ready-for-agent

- [ ] The Settings screen edits Report Rate (1K/4K/8K), key delay, sleep, and the Fn switch
- [ ] The status bar shows pending changes whenever local state differs from the Device
- [ ] `a` applies the pending changes with read-back verification; `r` reverts from the Device's actual state
- [ ] An applied Report Rate uses the correct wire encoding (1K/4K/8K) and reads back as set
- [ ] Unsaved changes are never silently written, and never silently lost when switching screens
