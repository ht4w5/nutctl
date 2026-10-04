# 11: v0 milestone — docs & tag

**What to build:** v0 becomes shareable: anyone can install it from source, understand that v0 means unstable, and a milestone tag marks the state. Runs the spec's full acceptance scenario end-to-end as the release gate.

**Blocked by:** 06, 07, 08

**Status:** ready-for-agent

- [ ] README documents installation, the udev setup, CLI and TUI usage, and the v0-while-unstable policy
- [ ] `go install` from the module path works on a clean Linux machine
- [ ] The spec's end-to-end scenario passes manually: remap on both Layers plus the Knob, change the Lighting Effect and Per-Key RGB, switch Report Rate, save and restore a State File — every write verified by read-back
- [ ] A `v0.0.1` milestone tag is created
