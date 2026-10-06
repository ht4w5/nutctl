# 11: v0 milestone — docs & tag

**What to build:** v0 becomes shareable: anyone can install it from source, understand that v0 means unstable, and a milestone tag marks the state. Runs the spec's full acceptance scenario end-to-end as the release gate.

**Blocked by:** 06, 07, 08

**Status:** resolved

- [x] README documents installation, the udev setup, CLI and TUI usage, and the v0-while-unstable policy
- [x] `go install` from the module path works on a clean Linux machine
- [x] The spec's end-to-end scenario passes manually: remap on both Layers plus the Knob, change the Lighting Effect and Per-Key RGB, switch Report Rate, save and restore a State File — every write verified by read-back
- [x] A `v0.0.1` milestone tag is created

## Comments

**2026-10-06 — implemented (resolved).** v0 is shareable. Full acceptance
evidence: [`../acceptance-v0.0.1.md`](../acceptance-v0.0.1.md).

- **README (checkbox 1):** rewritten around the whole v0 surface — Status
  with the **v0-while-unstable policy** (v0.x.y tags are milestones not
  releases, no stability promise inside v0, and what *does* hold: State File
  `schema` refusals by name, `--json` as the scripting contract, no
  unverified success claims), **installation** (`go install …@latest` /
  `…@v0.0.1` from the module path, or clone + `go build`/`go install`; Go
  1.24.2+, source-only, single static binary), the **udev setup** (with the
  actionable-error promise), the **CLI** (every verb and flag, `--device`
  selection and wrong-Model refusal, `reset`'s typed confirmation, why
  there is no `set`), and the **TUI** (screens, the shared keys, each
  screen's controls, local edits / `a` / `r` / `*`, the rebind picker and
  the Fn-disabled refusal). The write gate and State Files get their own
  sections (ADR-0003's `reset` exception stated where the self-check rule
  is stated), and a **Known v0 quirk** section carries the acceptance run's
  finding 1 (below).
- **`go install` on a clean machine (checkbox 2):** verified from cold
  caches (fresh `GOPATH`/`GOMODCACHE`/`GOCACHE`):
  `go install github.com/ht4w5/nutctl/cmd/nutctl@latest` resolves the module
  path over the proxy and the built binary runs; `go install ./cmd/nutctl`
  builds the exact tree being tagged from scratch. Caveat kept honest in
  the acceptance record: `@latest` serves the newest *published* state and
  `@v0.0.1` resolves only once this tag is pushed.
- **End-to-end acceptance (checkbox 3):** run against the real NUT87
  (firmware 1.20, USB) through the real TUI, every step read back with
  independent `nutctl get` calls — remap base 15 (knob press) `Mute →
  Play /Pause`, base 48 (Caps) `Caps → L-Ctrl`, fn 38 (Y) `Y → L-Ctrl`;
  effect mode `11 Flowing with the Waves → 10 Colorful Interchange`;
  Per-Key RGB entry 0 `#000000 → #ff0000`; Report Rate `8K → 4K`; write
  gate's golden read saved before the first write; then State File `save`,
  `load` of the saved file ("read-back verified: the Device matches the
  State File") and `load` of the pre-run golden State File restoring the
  original configuration exactly. **The checkbox's "every write verified by
  read-back" is met, with one hardware qualification:** the two writes that
  change the Report Rate re-enumerate the keyboard's USB interface, so the
  tool's own read-back cannot run and it honestly reports an unverified
  error; each was verified instead by a fresh read after the Device
  reappeared (details and transcripts in the acceptance record, finding 1).
- **Milestone tag (checkbox 4):** annotated tag `v0.0.1` on this commit.
- **Code review (two-axis, post-implementation):** fixed the Standards
  findings (README claimed the self-check gate without ADR-0003's `reset`
  exception and undercounted `reset` as a write-gate path; the State File
  "stays loadable" claim contradicted `statefile.go`'s unknown-schema
  refusal — now "refused by name, never silently misread"; "every write
  ends in read-back verification" now says what happens when it cannot run;
  "sibling boards" against CONTEXT.md's Avoid list → "sibling Models"; Go
  version pinned to `go.mod`'s 1.24.2; the acceptance record's typo and its
  "exactly the six intended changes" gloss) and the Spec findings (the
  acceptance record now carries the missing `go install` evidence for
  checkbox 2; its dangling "ticket's comment thread" reference now points
  at this section). **Suppressed findings (deliberate):** the README's
  usage block re-transcribes `usageText` (README is the human's entry point
  and the checkbox demands CLI usage in it; the block was fact-checked
  against the code line by line); the quirk section, the exact
  `go test -tags device` command and the "`--json` is a v0 contract" line
  as scope the checkbox/spec already imply (a release README that hides a
  known write-path defect would violate the repo's honesty norm);
  "Macros no State File can bring back" kept as the honest warning it is
  (v0 State Files carry no Macros — stated in the same sentence).
- **Follow-ups (not this ticket):** re-verify writes across USB
  re-enumeration — a Report Rate change moves the Device to a new
  `/dev/hidrawN` mid-write, so the read-back (and any long session) dies
  with "no such device" while the write lands; the honest-error behaviour
  is correct for v0, but re-opening the Device and completing the
  verification would turn finding 1's error into a verified success (and
  the same mechanism could make `load`/apply resilient to hot-unplug);
  `go install …@v0.0.1` re-verified after the tag is pushed to GitHub.
