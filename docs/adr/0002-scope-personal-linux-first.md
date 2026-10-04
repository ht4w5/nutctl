# Scope: personal tool first, Linux-first

This is a personal driver that fills a real hole (the vendor's web configurator does
not work on Linux), published with public-repo hygiene — MIT license, real README, no
support commitments. Linux is the only supported platform; cross-platform packaging
(Windows, macOS signing/notarization), i18n and a support surface are explicitly out of
scope for now even though Gio and hidapi would allow them later.

Considered options: treating it as a full public project (roughly doubles UI/QA effort
for users we haven't met), and cross-platform day one (packaging costs dwarf the
benefit while the protocol is still being verified).

Consequences: Phase 6 is small (udev rule, CI on Linux, optional tagged releases);
anyone wanting Windows/macOS builds can build from source and report back.
