# 08: Recovery — `nutctl reset` + `nutctl watch`

**What to build:** Recovery and observability from the CLI: factory reset when the configuration is garbage, and live device notify traffic when chasing quirks.

**Blocked by:** 03

**Status:** ready-for-agent

- [ ] `nutctl reset --keys|--lighting|--macros|--all` maps onto the firmware's reset scopes
- [ ] Reset requires typed confirmation naming the scope being destroyed
- [ ] `nutctl watch` streams device notify commands live
- [ ] Reset is refused on a bootloader/firmware-recovery Device
