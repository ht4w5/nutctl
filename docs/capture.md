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
- `GET_KEY`/`GET_FN_KEY` must decode to the physical NUT87 layout (87 keys + wheel
  slots 13/14/15) — a layout mismatch instantly reveals a framing/off-by-one bug.
- `GET_LED_EFFECT` check-code bytes must be `0xAA 0x55` at offsets 14..15 — an
  accidental endianness/offset detector built into the protocol.
- Read-modify-write one harmless setting (e.g. brightness) and read back.

This method needs **no vendor software at all** and is sufficient to ship v1
(keymap, Fn layer, lighting, macros, settings). `nutctl fixtures record` dumps
every request/response pair it sees into `testdata/captures/`, so probing and
fixture generation are the same activity.

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
```

The importer groups URBs into transfers, reassembles chunks (8-byte header, 24-byte
payloads), and pairs requests with responses by command id — the same logic
`internal/protocol` uses, so a parse failure is itself a bug report.

## Method C — five-minute check before giving up on the browser (optional)

If the web app's Linux failure is only *device access*, not a site-side block, this is
worth one attempt — capture in-browser is by far the least work:

1. udev rule so Chrome can open the hidraw device:
   `SUBSYSTEM=="hidraw", ATTRS{idVendor}=="0c45", MODE="0666"` (then re-plug).
2. Try Chrome (not Firefox — no WebHID), `chrome://device-log` for WebHID errors.
3. If the site refuses to load/scan on Linux, spoof a Windows User-Agent.

If it still doesn't work, drop it — Methods A+B fully replace it.

## Safety rules for any capture session

- Only reversible operations: lighting, one key remap, one setting bit.
- Never touch OTA/flash commands (`0x50` `SET_FLASH_DOWNLOAD`, `0x80+` OTA_*), never
  run `SET_FACTORY_RESET` unless the reset itself is the thing under test.
- If the board is wireless, do long sessions over USB (2.4G adds sleep/wake noise and
  `..._24G` command variants); capture the 2.4G dongle separately later.
