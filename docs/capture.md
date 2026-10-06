# Capturing NUT87 traffic without the browser

The official web configurator is unusable under Linux, so the original capture idea
(wrapping `HIDDevice.sendReport` in a userscript) is off the table. It is also
unnecessary: the protocol is already extracted from the vendor bundle
(`docs/protocol.md`), so capture is for **verification and the remaining §6 unknowns**,
not for discovery. Three methods, in order of preference:

## Method A — active probing with our own tool (primary)

Implement `internal/protocol` from `docs/protocol.md` and talk to the device directly
with `nutctl`. Every response is self-validating:

- `GET_DEVICE_INFO` must report the same vid/pid as `lsusb`, a plausible version,
  `firmwareStatus == 0`.
- `GET_KEY`/`GET_FN_KEY` must decode to the NUT87 layout (87 physical keys + Knob
  slots 13/14/15 + the 22 firmware-matrix Key Slots observed on firmware 1.20 —
  default bindings for keys the Model has no physical key for). The layout check
  is structural: every non-DEFAULT Key Action sits in a Key Slot the Model's
  layout table knows (keys ∪ Knob ∪ firmware-matrix slots), and each keymap block
  ends with the block-tail marker `00 00 AA 55` (bytes 510..511). Either symptom
  instantly reveals a framing/off-by-one bug.
- The check code `0xAA 0x55` sits at the block TAIL (`00 00 AA 55` at bytes
  508..511 of the keymap blocks; the Per-Key RGB block has the same tail) — an
  accidental endianness/offset detector built into the protocol. The Lighting
  Effect's check code at offsets 14..15 tolerates the unwritten factory state
  (`00 00`, the observed firmware-1.20 reading) as well as `0xAA 0x55` (written
  by the vendor app's SET path); any other value is corruption.
- Read-modify-write one harmless setting (e.g. brightness) and read back.

This method needs **no vendor software at all** and is sufficient to ship v1
(keymap, Fn layer, lighting, macros, settings). `nutctl fixtures record` dumps
every request/response pair it sees into `testdata/captures/`, so probing and
fixture generation are the same activity:

```sh
nutctl fixtures record                         # record the default read session
                                               # (probe + one full checked read pass)
nutctl fixtures record load state.json --i-know-what-im-doing
                                               # record THAT session — whatever the
                                               # command does on the wire (writes,
                                               # reset, watch traffic included)
nutctl fixtures record --out /tmp/corpus --case fw121 --device /dev/hidraw10
                                               # pick the corpus dir and case name
```

The recorder is a passive observer: it never touches the wire itself, and the
recorded session runs its own gates (ADR-0003) unchanged. One fixture per
transfer lands in `<out>/<cmd>/<case>.{req,res}.hex` + `meta.json` (firmware
version, connection type, capture method — the provenance a fixture needs to
be evidence); a command seen twice in one session gets `<case>-2`, `<case>-3`
…, and re-recording a case replaces its files. Metadata and wire bytes are the
corpus format `internal/fixture` reads back (`nutctl get` tests replay the
recordings through the fake Device), so a recording can go straight into the
repo as a test fixture.

Two habits keep the corpus honest:

- **Scrub, never drop.** The `source` line quotes the command that was
  recorded — if a dump ever names something personal, scrub `meta.json`
  rather than deleting the fixture (spec: "anything sensitive found in a dump
  is scrubbed from metadata rather than dropping the fixture").
- **One `--case` per session.** Re-recording a case replaces its files, but a
  later session with fewer exchanges of the same command leaves the previous
  session's numbered `<case>-2` files behind — record a fresh case name
  instead of reusing one.

## Method B — kernel USB capture (verification / unknowns)

For cross-checking our implementation against the real vendor app, capture at the USB
layer where the browser and OS can't hide anything:

```sh
sudo modprobe usbmon
lsusb | grep -i 0c45          # note the bus number, e.g. Bus 002
tshark -D | grep usbmon       # map bus -> usbmonN
sudo tshark -i usbmon2 -w nut87.pcapng        # capture everything on that bus
# ... operate the vendor app ...
# filter later:  tshark -Y 'usb.idVendor == 0x0c45' ...
```

Run the vendor app where it works — **a Windows VM with USB passthrough**, using the
same web configurator in Chrome (WebHID works on Windows) or the vendor's desktop app:

- QEMU/KVM: `virt-manager` → Add Hardware → USB Host Device (0c45:880c), or
  `-device usb-host,vendorid=0x0c45,productid=0x880c`
- VirtualBox: enable USB 3.0 controller + a device filter

The hypervisor drives the device through usbfs/libusb on the host, so **host `usbmon`
still sees every URB** even though the guest owns the device. What you'll see:

- Interrupt OUT / `SET_REPORT` control transfers (`0x21 0x09`) carrying our `0xAA`
  request frames (32-byte output reports, report id 0)
- Interrupt IN transfers carrying `0x55` response frames
- Any traffic we didn't expect (e.g. `COMMUNICATION_START/END`, feature reports)

Then turn the pcap into fixtures:

```sh
nutctl fixtures import-pcap nut87.pcapng --model NUT87
# → testdata/captures/<cmd>/<case>.{req,res}.hex + meta.json
nutctl fixtures import-pcap nut87.pcapng --model NUT87 --case fw121 --usb 2.14 --out /tmp/corpus
```

The importer reads the capture's URBs (pcap or pcapng of Linux usbmon,
`LINKTYPE_USB_LINUX`/`..._MMAPPED` — what tshark and Wireshark write; anything
else is refused by name), keeps the HID reports that carry the protocol —
interrupt transfers plus `SET_REPORT`/`GET_REPORT` control transfers, one
report per URB, 32- or 64-byte reports (§1/§6.5), with the HID report-id
prefix (output report id 0) stripped and counted — and groups them into
transfers with the same logic `internal/protocol` uses
(`protocol.SplitTransfers`: chunk reassembly, response matching by cmd/addr).
One transfer is one fixture, in the Method A corpus format.

- **`--model` is required**: a kernel capture carries no USB product strings
  to identify the Device by (CONTEXT.md: the name fields are decisive), and
  the capture cannot disambiguate shared product ids on its own — the
  firmware's name fields never reach usbmon, so `--model` is the operator's
  claim. The capture's own `GET_DEVICE_INFO` is cross-checked against the
  Model's USB ids and supplies the firmware version for the metadata; a
  capture of another Device (other USB ids) is refused — a fixture whose
  Device is wrong is not evidence.
- **`--usb <bus>.<dev>`** picks the Device when the capture holds several
  that speak the protocol (never a best guess); without it, exactly one must.
- **Imported fixtures validate against the protocol framing**
  (`protocol.ValidateTransfer`, the same contract `corpus_test.go` keeps the
  committed corpus to). An exchange that does not validate is not imported —
  its failure is reported instead.
- **Unparseable traffic is reported loudly instead of silently dropped.** A
  protocol-sized report on the Device's protocol streams that does not parse
  is a parse failure — a bug report: keep the bytes and file it. Everything
  else the capture holds (other Devices, key reports on the keyboard's own
  endpoint, non-report URBs, the documented 2.4G wake report) is counted and
  sampled in the same report. The exit code is 1 when a parse failure or a
  framing failure was left behind (bug-report material); expected noise is
  reported but does not fail the import. The exchanges that did parse are
  imported regardless.
- **Metadata records the capture provenance** — pcap source, Device (Model,
  USB ids, bus/device address), streams, report length, capture date and the
  firmware version where the capture knows one (`captureMethod:
  "usbmon-pcap"`).
- Device notify traffic (`55 FA`/`55 FC`) imports as response-only fixtures
  (`get_device_notify`, `get_24g_disconnect_notify`), which is how the
  notify corpus of ticket 08's follow-up gets filled from captures.

## Method C — five-minute check before giving up on the browser (optional)

If the web app's Linux failure is only *device access*, not a site-side block, this is
worth one attempt — capture in-browser is by far the least work:

1. udev rule so Chrome can open the hidraw device:
   `SUBSYSTEM=="hidraw", ATTRS{idVendor}=="0c45", MODE="0666"` (then re-plug).
2. Try Chrome (not Firefox — no WebHID), `chrome://device-log` for WebHID errors.
3. If the site refuses to load/scan on Linux, spoof a Windows User-Agent.

If it still doesn't work, drop it — Methods A+B fully replace it.

## Safety rules for any capture session

**One reader at a time.** `nutctl` takes an exclusive lock on the hidraw node
and refuses to open a Device another process already holds (a second `nutctl`
session, the vendor app, a capture tool) with a `device busy` error. This is
enforced because the kernel hands each hidraw input report to exactly one
reader: concurrent readers steal each other's chunks and multi-chunk reads
reassemble into corrupted blocks — measured on real hardware (2026-10-05):
4/4 concurrent `nutctl get keymap` runs corrupted, 0/8 sequential runs
failed.

- Only reversible operations: lighting, one key remap, one setting bit.
- Never touch OTA/flash commands (`0x50` `SET_FLASH_DOWNLOAD`, `0x80+` OTA_*), never
  run `SET_FACTORY_RESET` unless the reset itself is the thing under test.
- If the board is wireless, do long sessions over USB (2.4G adds sleep/wake noise and
  `..._24G` command variants); capture the 2.4G dongle separately later.
