# 09: Fixture corpus + `nutctl fixtures record`

**What to build:** Recording becomes corpus-building: any live session against real hardware turns into committed fixtures that keep the offline tests honest and close the remaining protocol unknowns with evidence.

**Blocked by:** 03

**Status:** ready-for-agent

- [ ] `nutctl fixtures record` captures request/response pairs from a live session with metadata (firmware version, connection type, capture method)
- [ ] Recorded fixtures drive the fake Device in tests round-trip
- [ ] The corpus covers every command used by the v0 read and write paths
- [ ] The protocol notes are reconciled against recorded reality; any discrepancy is documented with the evidence
