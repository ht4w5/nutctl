---
status: accepted
---

# No preset management: the Device is the source of truth

`nutctl` has no preset library, no named config store, no history, no auto-apply and
no background file writes. It reads configuration from the hardware, and touches files
only when the user explicitly saves or loads a **State File** at a path they chose.
Decided by the project owner, rejecting the "preset manager" found in every other
keyboard configurator (the vendor's own app keeps four browser-local profiles): such a
store is state we would have to name, sync, migrate and explain, for a workflow that
"save to a file I chose" already covers.

Consequences: the golden read of ADR-0003 is an explicit save (default filename
offered, never forced); switching between configurations is just loading a different
file; a preset library can always be layered on later as a wrapper around `save`/`load`
without touching the device layer.
