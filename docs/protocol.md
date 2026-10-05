# NUT87 wire protocol (extracted from `weikav.driveall.cn` bundle)

Source: `decoded/layout-classic-DSv6_q0d.js` (WebHID driver of the official web configurator).
Everything below is quoted from that bundle, not guessed. Where the bundle only implies
something, it is marked **(infer)** or listed under "Open questions".

## 1. Device identification

| Variant | VID | PID | USB product name |
|---|---|---|---|
| NUT87 (wired) | `0x0C45` (3141) | `0x880C` (34828) | `NUT87` |
| NUT87 (2.4G dongle) | `0x0C45` (3141) | `0xFEF9` (65273) | `NUT87 2.4G` |

- The configurator builds the identity string
  `vendorId:productId:productName:manufacturer:product` and matches it against
  config file names (`3141-34828-NUT87.ts`). **PID 34828 is shared with NUT75** and
  PID 65273 with several other boards — the firmware-reported `product`/`productName`
  string is what disambiguates.
- Accepted HID collections (usage pages) for the keyboard interface:
  `0xFF68, 0xFF80, 0xFF60, 0xFF00, 0xFF01, 0xFF1B`.
  2.4G dongle lookup additionally uses usage page `0xFF67`.
- USB transfer uses **output report id 0**, report length taken from the HID descriptor
  (`outputReports[0].items[0].reportCount`, **32 bytes** on the reference device).
  Some 2.4G commands come in `..._64_BYTE` variants, so 64-byte devices exist.
  **(observed 2026-10)** a real NUT87 (`0c45:880c`, firmware 1.20) exposes
  **64-byte** input/output reports on its `0xFF68` interface, so chunk payloads are
  56 bytes there; report length must come from the descriptor (recorded:
  `testdata/captures/get_device_info/nut87.*`).

## 2. Framing

All traffic is chunked request/response over report id 0.

### Request (host → device), one 32-byte report per chunk

```
byte 0      : 0xAA                magic
byte 1      : cmd                 command id
byte 2      : len                 payload bytes in THIS chunk
byte 3..4   : addr                uint16 LE, byte offset of this chunk in the transfer
byte 5..7   : otherHeader         3 free header bytes (used by some commands)
              byte 6 = 1          "last packet" flag on the final chunk
byte 8..31  : payload             up to reportLen-8 = 24 bytes
```

- Chunk payload size = `reportCount - headerCount` = 24 for 32-byte reports.
- A single-key read pre-builds the whole 8-byte header and passes it as `customHeader`
  (e.g. GET_KEY for one key: `AA 12 04 <idx*4 LE16> 00 00 00`).
- `isNeedLastPacketFlag` can be suppressed (macro table writes use `false` for the
  pointer-table write, `true` for the action-data write).

### Response (device → host), via `inputreport` event

```
byte 0      : 0x55                magic
byte 1      : cmd                 echoed command id
byte 2      : lenOrType
byte 3..4   : addr                uint16 LE
byte 8..    : data
```

- Response is matched by `cmd`, optionally by `addr`.
  **(observed 2026-10)** `lenOrType` is the payload length of the chunk and `addr`
  the transfer offset, mirroring the request header (real NUT87, firmware 1.20).
- Timeout 500 ms default (1000 ms SET_KEY, 2000 ms SET_CUSTOM_LED_DATA and on
  `frameVersion == 1` firmware), 3 retries. After final failure the app tears down the
  HID device and reconnects.
  **(observed 2026-10-05)** SET commands are answered per chunk like reads, and the
  NUT87's ack echoes the written block back (`55 <cmd> <len> <addr>` + the chunk
  payload; recorded as `testdata/captures/set_*` — a write-back experiment on real
  hardware, firmware 1.20).
- Command `0x1C` (GET_DEFAULT_FN_KEY_MATRIX) is optional: no response is tolerated.
- Multi-chunk transfers are reassembled by concatenating `response[8:]` of each chunk,
  then truncating to the requested `contentSize`.

## 3. Command table

```
COMMUNICATION_START 1     COMMUNICATION_END 2     SET_FACTORY_RESET 15
GET_DEVICE_INFO 16        GET_GAME_MODE 17        GET_KEY 18
GET_LED_EFFECT 19         GET_CUSTOM_LED_DATA 20  GET_MACRO 21
GET_FN_KEY 22             GET_MAGNETIC_AXIS_RT 23 GET_MAGNETIC_AXIS_DKS_DATA 24
GET_LIGHT_BOX 27          GET_DEFAULT_FN_KEY_MATRIX 28   GET_SIDE_LIGHT 29
GET_TRIGGER_KEY 30        GET_DEFAULT_KEY_MATRIX 31
SET_GAME_MODE 33          SET_KEY 34              SET_LED_EFFECT 35
SET_CUSTOM_LED_DATA 36    SET_MACRO 37            SET_FN_KEY 38
SET_MAGNETIC_AXIS_RT 39   SET_MAGNETIC_AXIS_DKS_DATA 40
SET_DOT_MATRIX_MODE 42    SET_LIGHT_BOX 43        SET_SIDE_LIGHT 45
SET_TRIGGER_KEY 46        SET_KEYBOARD_CUSTOM_FUNCTION_ON 48
SET_KEYBOARD_CUSTOM_FUNCTION_OFF 49
GET_LED_DATA 50           GET_ALL_LIGHTS_RGB 51   SET_TEMPORARY_COMMAND_DATA 52
SET_MUSIC_DATA 53         CLEAR_LED_DATA 54       GET_ALL_LIGHTS_RGB_24G 55
GET_ALL_LIGHTS_RGB_24G_64_BYTE 59   SET_MUSIC_DATA_24G_64_BYTE 60
GET_DOT_MATRIX_CONFIG 61  SET_LED_BOOT_ANIMATION 64  SET_LED_USER_ANIMATION 65
SET_LED_DATA 66           SET_FLASH_DOWNLOAD 79
SET_TFT_USER_ANIMATION 80 SET_TFT_BUILT_IN_INDEX 81
GET_MAGNETIC_AXIS_KEY_STATUS 96
SET_CALIBRATION_ON 100    SET_CALIBRATION_OFF 101
SET_SIMULATION_TEST_ON 102 SET_SIMULATION_TEST_OFF 103
GET_MAGNETIC_AXIS_STATUS 104
SET_CALIBRATION_ON_V2 105 SET_CALIBRATION_OFF_V2 106
OTA_GET_DEVICE_SYSTEM_INFO 128  OTA_VERIFY_FIRMWARE_INFO 129
OTA_DEVICE_ENTER_BOOT 130       OTA_SEND_FIRMWARE_INFO 131
OTA_GET_DEVICE_SN 132           OTA_SWITCH_APP_PARTITION 133
OTA_SET_DEVICE_SN 134
GET_DEVICE_NOTIFY 250     GET_MAGNETIC_AXIS_CALIBRATION_DATA 251
GET_24G_DISCONNECT_NOTIFY 252
```

Factory reset sub-commands: `KEY_RESET 1, LIGHTING_RESET 2, MACRO_RESET 4,
CLEAR_CALIBRATION 5, RESET_ALL 255`.

### Init sequence on connect (`deviceInit`)

```
GET_DEVICE_INFO(48)            → if firmwareStatus == 1: device is in bootloader, stop
GET_GAME_MODE(56)
GET_KEY(512)                   → 128 key slots
GET_DEFAULT_KEY_MATRIX         (dep. on routes)
GET_FN_KEY(512) + GET_DEFAULT_FN_KEY_MATRIX   (if customKeys.isShowFn)
GET_LED_EFFECT(16) + GET_CUSTOM_LED_DATA(512)  (if "lighting" route)
GET_LIGHT_BOX / GET_SIDE_LIGHT                (if configured)
GET_LED_DATA                   (if "led" route)
GET_MACRO                      (if "macro" route)
GET_MAGNETIC_AXIS_RT           (if "performance" route — HE boards only)
GET_TRIGGER_KEY                (if "trigger" route — HE boards only)
GET_MAGNETIC_AXIS_DKS_DATA     (if DKS supported)
OTA_GET_DEVICE_SN              (if frameVersion == 1)
```

For NUT87 the routes are `customKeys, lighting, macro, advancedKeys, settings` —
no TFT, no trigger/performance/HE features (config: `isShowAutoCalibration:false`,
`advancedKeysList:["SOCD","MT","TGL","CB"]`).

## 4. Payloads (byte-exact)

### GET_DEVICE_INFO — 48 bytes

| off | field | notes |
|---|---|---|
| 0 | romSize | u8 |
| 2..3 | macroSpaceSize | u16 LE (fallback 512) |
| 4..5 | vid | u16 LE |
| 6..7 | pid | u16 LE |
| 8..9 | version | BCD-ish: `((b8&0xF) + ((b8&0xF0)>>4)*10 + b9*100)/100` |
| 10..11 | sensor | u16 LE |
| 12..13 | manufacturer | u16 LE (matches identity string part) |
| 14..15 | product | u16 LE |
| 16 | workMode | |
| 17 | batteryLevel | |
| 18 | chargeStatus | |
| 19 | currentProfile | |
| 20..21 | axisInfo | u16 LE |
| 22..23 | tftMaxFrames | u16 LE |
| 24..25 | gifMaxFrames | u16 LE |
| 26..27 | ledMaxFrames | u16 LE |
| 28 | tftDirection | `0xFF` = no TFT |
| 29 | rtPrecision | |
| 30 | frameVersion | `1` → older timing + SN via OTA cmd |
| 31 | lightingVersion | |
| 32 | firmwareStatus | `1` = bootloader / corrupted app |

### GET_GAME_MODE / SET_GAME_MODE — 56 bytes

| off | field | | off | field |
|---|---|---|---|---|
| 1 | gameMode | | 11 | stabilityMode |
| 2 | fnSwitch | | 14 | autoCalibration |
| 3 | sleepTime | | 15 | singleKeyWakeup |
| 4 | keyDelay | | 16 | pushButtonMode |
| 5 | reportRate | wire enum `1K→3, 2K→4, 4K→5, 8K→6` (NUT87: 3/5/6) | 17 | nkroSwitch |
| 6 | systemMode | | 18..19 | wirelessReportRate u16 LE, raw Hz, 2.4G only |
| 7 | tftDisplayTime | | 20 | powerMode |
| 8 | topDeadZone | `value*100` on wire | | |
| 9 | bottomDeadZone | `value*100` on wire | | |

**(observed 2026-10-05, vendor bundle `decoded/layout-classic-DSv6_q0d.js`,
settings view + its `Dv()` composable — closes the value-domain half of §6.6):**

- `keyDelay` is a discrete level: the UI's `keyDelayList` offers only
  `[{value:1}..{value:5}]` and reads `keyDelay ?? 3`. No physical unit is
  exposed anywhere in the bundle.
- `sleepTime` is **minutes**: the UI's slider is `min:1 max:30 step:1` with
  marks `{1:"1min",10:"10min",20:"20min",30:"30min"}`; sleep off writes `0`
  (`handleSleepModeToggle`: off → 0, on → at least 1).
- `fnSwitch` has no UI at all — the app round-trips the byte
  (`l[2]=s.fnSwitch||0` on SET). Treat it as a plain 0/1 switch and claim no
  semantics beyond that.
- `reportRateList` is per-Model config: every NUT87-family entry carries
  `reportRateList:[1e3,4e3,8e3]` (Hz) → offers 1K/4K/8K = wire `3/5/6`,
  matching what the hardware reads (§6.6).

### GET_KEY / GET_FN_KEY — 512 bytes = 128 slots × 4 bytes

```
slot b0 : pageType     slot b1 : param1     slot b2 : param2     slot b3 : param3
```

Single-slot read: pre-built header with `addr = slotIndex*4`, `len = 4`.

**pageType table** (`Dt`):

```
0 DEFAULT   1 MOUSE       2 KEYBOARD    3 CONSUMER_KEY  4 SYSTEM_KEY
5 EXTRA_FUNCTION          6 MACRO       7 CB   8 DKS    9 MT
10 TGL      11 SOCD       12 RS         13 FUNC 14 END  15 MPT
>=128       FUNC_V2
```

Encoding per page (`lo()` writer / `Bn()` reader):

| page | b0 | b1 | b2 | b3 |
|---|---|---|---|---|
| MOUSE | 1 | button/param1 | value | — |
| KEYBOARD | 2 | — | HID keycode | — |
| CONSUMER_KEY | 3 | usage lo | usage hi | — |
| MACRO/MT/SOCD/RS/CB/MPT | Dt | param1 | param2 | param3 |
| DKS | 8 | value | — | — |
| TGL | 10 | value | — | — |
| FUNC | 13 | value>>16 | value>>8 | value |
| FUNC_V2 | `128 \| (r>>8)&0x7F` | `r&0xFF` | `l>>8` | `l` |

Key slots are laid out against the model's `keyList` (NUT87: 87 keys, ids 0..108 with
gaps; wheel keys occupy slots 13/14/15 = vol+/mute/vol−).

**(observed 2026-10-05)** a real NUT87 (firmware 1.20, recorded in
`testdata/captures/get_key/` + `get_fn_key/`) fills far more than the physical
layout: **22 out-of-layout Key Slots — 29, 30, 31, 45, 46, 47, 61, 62, 63,
77, 78, 79, 93, 94, 95, 96, 97, 98, 101, 109, 110, 111 (the layout's "gaps"
plus 109–111) — carry real default Key Actions on BOTH Layers** (e.g. slot 29
= `02 00 53 00`, 30 = `02 00 54 00`, 31 = `02 00 55 00` (keypad cluster
0x53–0x55), 101 = `02 00 e7 00`, 109 = `02 00 56 00`, 110 = `02 00 57 00`,
111 = `02 00 00 00`; slot 96 = `03 92 01 00` CONSUMER). This is a shared
firmware matrix with sibling Models — default bindings for keys the NUT87
does not physically have — so these Key Slots are normal state, not
corruption. PageType histograms of the recording: base `pt0:16 pt2:108 pt3:4`,
Fn `pt0:16 pt2:82 pt3:4 pt13:26`.

**(observed 2026-10-05)** each 512-byte keymap block ends with the marker
`00 00 AA 55` — bytes 510..511 are `0xAA 0x55`. See GET_LED_EFFECT below: the
`0xAA 0x55` marker sits at the block TAIL, not at the Lighting Effect's
offsets 14..15.

### GET_LED_EFFECT / SET_LED_EFFECT — 16 bytes

```
0 mode   1..3 rgb   4 driverSetting (0xFF on write)   5..7 secondary rgb
8 colorMode   9 brightness   10 speed   11 direction   12 effectModeType
13 -    14..15 check code: 0xAA 0x55 (SET path only — see observed reality below)
```

NUT87 ranges: brightness 1..6, speed 1..6, custom effects `[23,24,25]`.

**(observed 2026-10-05)** on a real NUT87 (firmware 1.20, recorded in
`testdata/captures/get_led_effect/`) offsets 14..15 read `00 00`, **not**
`0xAA 0x55` — the claim that GET_LED_EFFECT carries the check code there is
falsified by hardware. The `0xAA 0x55` marker was observed at the block TAIL
instead: bytes 510..511 of both keymap blocks read `AA 55` (whole tail
`00 00 AA 55`, slot 127 raw = `00 00 aa 55`), and the Per-Key RGB block has
the same tail. **Verified 2026-10-05 (§6.8, ticket 03 write path):** the SET
format's `0xAA 0x55` at offsets 14..15 is a "written" flag — after
SET_LED_EFFECT the Device reads `AA 55` there (and `0xFF` at offset 4)
forever after, factory/unwritten reads `00 00`.

### GET_CUSTOM_LED_DATA / SET_CUSTOM_LED_DATA — 512 bytes

128 entries × 4 bytes: `ledId, red, green, blue`. On SET the app writes
`b0 = index` as the ledId. (Index ↔ key mapping is the model's LED id order.)

**(observed 2026-10-05)** the recorded factory block (firmware 1.20,
`testdata/captures/get_custom_led_data/`) is all zeroes except the same tail
marker as the keymap blocks: bytes 508..511 = `00 00 AA 55` (so the last
entry's raw bytes carry `AA 55` at 510..511). **(observed 2026-10-05, ticket
03)** after SET_CUSTOM_LED_DATA the ledId bytes read back as the entry INDEX
(the write forces `l[f]=i`, §4) where a factory block reads `0` throughout.

### Key action semantics (`param1..3` per pageType)

| page | param1 | param2 | param3 |
|---|---|---|---|
| MT | tap keycode | hold keycode | threshold in 10 ms units (UI default 40 = 400 ms) |
| SOCD | mode 1..4 | keycode A | keycode B (both physical keys get the same entry) |
| RS | `0x00` | keycode A | keycode B |
| CB | hotkey 1 keycode | hotkey 2 keycode | regular-key keycode |
| TGL | (byte1) target keycode | — | — |
| END | key value 1 | key value 2 | — |
| MACRO | macroId (0..99) | pass-through | pass-through |
| DKS | slot index 0..63 (data in GET/SET_MAGNETIC_AXIS_DKS_DATA) | — | — |
| FUNC | 24-bit BE function id (`b1<<16\|b2<<8\|b3`) → `Ko` table (77 entries: factory reset, BT ch 1-5, wireless reconnect, F-row switch, battery, WIN/MAC/ANDROID/IOS, lighting effect/color/brightness/speed, lock Win, side light, light bar, ALT+TAB, WIN+E, CTRL_CAP swap, knob mode toggle, macro on/off, …) | | |
| FUNC_V2 | pageType ≥ 128: `keyA = (pageType&0x7F)<<8\|param1`, `keyB = param2<<8\|param3`; values < 4096 are keycodes, ≥ 4096 index the FUNC_V2 table | | |

### GET_MACRO / SET_MACRO

- Addr 0, 400 bytes = 100 macro pointers, `u32 LE` each, `0` = unused.
- At pointer `g`: `u16 LE` = total action bytes; then `n = len/2` actions × 4 bytes:
  `b0..1 delay u16 LE`, `b2 keyCode`, `b3 flags`:
  - bit7 = isPress
  - bits4..6 = actionType
  - actionType 1|2 → `b3 = 0x90` (press) / `0x10` (release), else `0xB0` / `0x30`
- SET_MACRO: first write pointer table (400 bytes, `isNeedLastPacketFlag:false`),
  then concatenated action data at `addrStart = 400` (`isNeedLastPacketFlag:true`).

### Others known only structurally

`GET/SET_LIGHT_BOX`, `GET/SET_SIDE_LIGHT`, `GET_LED_DATA`, `SET_LED_DATA`,
`SET_LED_BOOT_ANIMATION`, `SET_LED_USER_ANIMATION` (frame-based, sizes from
`tftMaxFrames/gifMaxFrames/ledMaxFrames`), `GET/SET_TRIGGER_KEY`,
`GET/SET_MAGNETIC_AXIS_*` (Hall-effect boards, not NUT87).

## 5. Misc

- CRC16-CCITT (poly `0x1021`, init `0xFFFF`) is implemented in the bundle — used by
  the OTA/flash path.
- OTA flashing runs through a Rust→WASM lib (`sn_isp_lib_bg.wasm`) that speaks HID
  **feature reports** (`sendFeatureReport`/`receiveFeatureReport`), separate from the
  input/output report protocol above.
- 2.4G specifics: `GET_ALL_LIGHTS_RGB_24G`, `SET_MUSIC_DATA_24G_64_BYTE`,
  `GET_24G_DISCONNECT_NOTIFY`, sleep/wake state handling.
- **No Bluetooth.** The only `connectType` values in the app are `"USB"` and `"2.4G"`;
  the UI states "All devices do not support Bluetooth mode connection driver". The
  `蓝牙通道1-5` / pairing rows in the FUNC table are static, ungated entries — treat
  them as dead codes (may still be accepted by firmware, unverified).
- **Profiles are app-side, not device-side.** The vendor app stores up to 4 config
  sets in browser storage and applies them via ordinary SET commands. Device-info
  byte 19 `currentProfile` is parsed but never used; there are no profile
  GET/SET/switch commands in the command table.

## 6. Open questions (to be closed by capture)

1. ~~Exact `GET_CUSTOM_LED_DATA` matrix layout~~ — **closed**: 128 × 4B `ledId,r,g,b`.
2. Payload layout of `SET_TRIGGER_KEY` / DKS / RT commands (not needed for NUT87 v1).
3. ~~Param semantics for `MT`, `SOCD`, `CB`, `RS`, `MPT`, `END`~~ — **closed** except
   `MPT` (no construction site in the bundle; editor lives in a lazy chunk we don't have)
   and `MACRO` param2/param3 (pass-through, believed unused).
4. `GET_LED_DATA` / animation frame binary format (only needed for the LED editor).
5. ~~Whether report size is 32 on all platforms, and behavior of the `..._64_BYTE` variants.~~
   — **closed for the wired NUT87**: it reports **64-byte** reports (interface `0xFF68`,
   firmware 1.20, recorded 2026-10 in `testdata/captures/`); the 32-byte figure is the
   vendor app's fallback default. The 2.4G `..._64_BYTE` variants remain untested
   (2.4G is out of scope for v0).
6. ~~Meaning of every `gameMode` byte~~ — **closed** for reportRate (`{1K:3,2K:4,4K:5,8K:6}`;
   `wirelessReportRate` = raw Hz u16 LE) and **closed 2026-10-05 for the v0 Settings
   editor** via the vendor bundle's own settings UI (see §4): `keyDelay` is a level
   1..5, `sleepTime` is minutes (0 = never, 1..30), `fnSwitch` is an opaque 0/1
   switch the app round-trips without ever showing it. Remaining field meanings
   (systemMode, powerMode) need one read-back experiment each.
7. ~~Whether `COMMUNICATION_START/END` (cmd 1/2) must be sent around sessions.~~
   — **closed for the v0 read path**: `GET_DEVICE_INFO`/`GET_GAME_MODE` succeed without
   them on firmware 1.20 (active probing 2026-10, `testdata/captures/`).
   **Closed for the v0 write path too (2026-10-05, ticket 03):**
   SET_KEY/SET_FN_KEY/SET_LED_EFFECT/SET_CUSTOM_LED_DATA/SET_GAME_MODE all land and
   verify read-back byte-for-byte without them (firmware 1.20 write-back experiment,
   `testdata/captures/set_*`).
8. ~~Check codes: the documented location was falsified~~ — **closed 2026-10-05
   (ticket 03 write path, real NUT87 firmware 1.20, `testdata/captures/set_*`).**
   The bundle puts `0xAA 0x55` at GET_LED_EFFECT offsets 14..15; hardware reads `00 00`
   there (factory/unwritten) and shows the `0xAA 0x55` marker at the block TAIL
   instead (`00 00 AA 55` at bytes 508..511 of GET_KEY, GET_FN_KEY **and**
   GET_CUSTOM_LED_DATA). The SET path's answer, by experiment: **SET_LED_EFFECT writes
   `0xAA 0x55` at offsets 14..15 and the Device reports it back on every later read —
   the pair doubles as a "written" flag** (offset 4 reads back `0xFF` the same way).
   The block-tail marker survives SET_KEY/SET_FN_KEY/SET_CUSTOM_LED_DATA byte for
   byte, and SET_CUSTOM_LED_DATA's forced `ledId = index` sticks (entry *i* reads back
   ledId *i*, factory blocks read `0` everywhere). Each of the five SET blocks was
   written back and read back **byte-for-byte identical**.

Everything needed for the v1 feature set (keymap, Fn layer, lighting effect + per-key
RGB, macros, settings, factory reset) is answered by §2–§4 above; items 2, 4–8 only
affect extras or cross-checking.
