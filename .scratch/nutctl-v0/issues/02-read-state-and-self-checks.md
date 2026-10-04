# 02: Read full state — layout table, `nutctl get`, self-checks

**What to build:** The full readable state of the Device becomes visible — every Key Slot on both Layers, the Lighting Effect, Per-Key RGB, and Settings — together with the self-checks that make later writes safe (the ADR-0003 precondition).

**Blocked by:** 01

**Status:** ready-for-agent

- [ ] `nutctl get keymap [--layer fn]` shows each Key Slot with its current Key Action, including Knob gestures
- [ ] `nutctl get lighting` shows the Lighting Effect and Per-Key RGB; `nutctl get settings` shows Settings including Report Rate
- [ ] All `get` commands provide stable `--json` output
- [ ] The Model layout table (keys, Knob slots, Fn-disabled Key Slots) is data, not code — another Model's table could be added without code changes
- [ ] The three self-checks (Device Identity match, keymap decodes to the physical layout, Lighting Effect check code) run as one reusable verification step and fail loudly when wrong
- [ ] Unknown Key Action page types decode to an explicit unknown marker rather than crashing or silently dropping
