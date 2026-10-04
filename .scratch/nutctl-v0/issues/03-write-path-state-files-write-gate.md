# 03: Write path + State Files + write gate

**What to build:** Writes become possible and provable: put changes in a State File, `load` it, and get verified proof it landed — guarded by the write gate (ADR-0003) and with no file activity beyond what the user asked for (ADR-0005).

**Blocked by:** 02

**Status:** ready-for-agent

- [ ] `nutctl save <file>` writes a State File with the `model`, `firmware`, `schema`, `state` envelope
- [ ] `nutctl load <file>` applies a State File covering all four blocks (Base Layer, Fn Layer, Lighting Effect + Per-Key RGB, Settings) in batched writes, and prints the read-back verification diff
- [ ] `load` refuses a State File saved from a different Model; on firmware mismatch it warns but proceeds
- [ ] The session's first write requires self-checks passing and offers the golden-read prompt (save current state before writing); a noisy skip flag exists
- [ ] Writes are refused while the Device reports bootloader/firmware-recovery state
- [ ] Round-trip property holds: save → load → save yields identical State Files
