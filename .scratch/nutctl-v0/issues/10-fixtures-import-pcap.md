# 10: `nutctl fixtures import-pcap`

**What to build:** Vendor-app traffic captured at the kernel level (usbmon while the official app runs in a Windows VM) becomes fixtures too, so our implementation can be cross-checked against the official one without any browser tooling.

**Blocked by:** 09

**Status:** ready-for-agent

- [ ] `nutctl fixtures import-pcap` reassembles USB captures into framed request/response fixtures
- [ ] Imported fixtures validate against the protocol framing; unparseable traffic is reported loudly instead of silently dropped
- [ ] Metadata records the capture provenance (pcap source, device, firmware where known)
