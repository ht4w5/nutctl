package protocol

import "fmt"

// LightingEffect is the decoded GET_LED_EFFECT payload (16 bytes,
// docs/protocol.md §4). The check code (bytes 14..15) is decoded, never
// enforced here — the self-check on it lives in internal/device (ADR-0003).
type LightingEffect struct {
	Mode           uint8   `json:"mode"`
	RGB            [3]byte `json:"rgb"`
	SecondaryRGB   [3]byte `json:"secondaryRgb"`
	ColorMode      uint8   `json:"colorMode"`
	Brightness     uint8   `json:"brightness"`
	Speed          uint8   `json:"speed"`
	Direction      uint8   `json:"direction"`
	EffectModeType uint8   `json:"effectModeType"`
	// DriverSetting is the raw byte at offset 4. The SET wire format forces
	// it to 0xFF (docs/protocol.md §4), so it is a wire marker, not Device
	// state: never marshalled into a State File.
	DriverSetting uint8 `json:"-"`
	// CheckCode is the raw byte pair at offsets 14..15. The SET wire format
	// forces it to 0xAA 0x55 and a write makes the Device report it back
	// (docs/protocol.md §6.8, observed on firmware 1.20) — a wire marker of
	// the write path, not Device state: never marshalled into a State File.
	CheckCode [2]byte `json:"-"`
	// CheckCodeOK says the check code is a state the protocol recognizes:
	// written (0xAA 0x55 — the vendor app's SET_LED_EFFECT path puts it
	// there) or factory/unwritten (0x00 0x00 — the state observed on a
	// firmware 1.20 Device out of the factory). Any other pair is
	// corruption/misalignment and fails self-check 3 in internal/device.
	// Derived from CheckCode: never marshalled into a State File.
	CheckCodeOK bool `json:"-"`
}

// CheckCode is the magic pair at offsets 14..15 of the Lighting Effect block
// (docs/protocol.md §4): an accidental endianness/offset detector built into
// the protocol. Observed reality (2026-10, firmware 1.20): 0xAA 0x55 is only
// written by the vendor app's SET path, and a Device that has never been
// written reads 0x00 0x00 here. The same 0xAA 0x55 marker was observed at
// the TAIL (bytes 510..511) of the GET_KEY / GET_FN_KEY blocks.
const (
	CheckCodeByte0 byte = 0xAA
	CheckCodeByte1 byte = 0x55
)

// DecodeLightingEffect decodes a reassembled GET_LED_EFFECT payload. The
// check code lands in CheckCode (raw) and CheckCodeOK (recognized state),
// never enforced here.
func DecodeLightingEffect(b []byte) (LightingEffect, error) {
	if len(b) < LEDEffectSize {
		return LightingEffect{}, fmt.Errorf("lighting effect payload too short: %d bytes, want at least %d", len(b), LEDEffectSize)
	}
	checkCode := [2]byte{b[14], b[15]}
	return LightingEffect{
		Mode:           b[0],
		RGB:            [3]byte{b[1], b[2], b[3]},
		DriverSetting:  b[4],
		SecondaryRGB:   [3]byte{b[5], b[6], b[7]},
		ColorMode:      b[8],
		Brightness:     b[9],
		Speed:          b[10],
		Direction:      b[11],
		EffectModeType: b[12],
		CheckCode:      checkCode,
		CheckCodeOK:    checkCode == [2]byte{CheckCodeByte0, CheckCodeByte1} || checkCode == [2]byte{0, 0},
	}, nil
}

// EncodeLightingEffect encodes a Lighting Effect into its 16-byte
// GET_LED_EFFECT / SET_LED_EFFECT payload (docs/protocol.md §4). The SET wire
// format forces two positions quoted from the vendor bundle: byte 4
// (driverSetting) is 0xFF and the check code at 14..15 is 0xAA 0x55 on every
// write. Those positions are wire markers of the SET format, not Device
// state — which is why the State File does not carry them (internal/device).
func EncodeLightingEffect(e LightingEffect) []byte {
	out := make([]byte, LEDEffectSize)
	out[0] = e.Mode
	copy(out[1:4], e.RGB[:])
	out[4] = 0xFF // driverSetting is forced to 0xFF on write
	copy(out[5:8], e.SecondaryRGB[:])
	out[8] = e.ColorMode
	out[9] = e.Brightness
	out[10] = e.Speed
	out[11] = e.Direction
	out[12] = e.EffectModeType
	// byte 13 stays zero; the check code is forced to 0xAA 0x55 on write.
	out[14], out[15] = CheckCodeByte0, CheckCodeByte1
	return out
}

// PerKeyLED is one Per-Key RGB entry (docs/protocol.md §4: ledId, red, green,
// blue).
type PerKeyLED struct {
	// LEDID is the raw ledId byte. The SET wire format writes the entry
	// index there (docs/protocol.md §4: `l[f]=i`), so it is derived on
	// write: never marshalled into a State File.
	LEDID uint8 `json:"-"`
	R     uint8 `json:"r"`
	G     uint8 `json:"g"`
	B     uint8 `json:"b"`
}

// PerKeyRGB is a decoded GET_CUSTOM_LED_DATA payload: 128 entries, index =
// entry id.
type PerKeyRGB [128]PerKeyLED

// EncodePerKeyRGB encodes the Per-Key RGB table into its 512-byte
// GET_CUSTOM_LED_DATA / SET_CUSTOM_LED_DATA payload (docs/protocol.md §4:
// 128 entries × 4 bytes). The SET wire format writes the entry INDEX as the
// ledId (quoted from the bundle: `l[f]=i`) — the ledId byte is derived on
// write, never taken from the block, which is why the State File carries
// colors only (internal/device).
func EncodePerKeyRGB(rgb PerKeyRGB) []byte {
	out := make([]byte, PerKeyRGBSize)
	for i, e := range rgb {
		copy(out[i*4:], []byte{uint8(i), e.R, e.G, e.B})
	}
	return out
}

// DecodePerKeyRGB decodes a reassembled GET_CUSTOM_LED_DATA payload
// (docs/protocol.md §4: 128 entries × 4 bytes).
func DecodePerKeyRGB(b []byte) (PerKeyRGB, error) {
	if len(b) < PerKeyRGBSize {
		return PerKeyRGB{}, fmt.Errorf("per-key RGB payload too short: %d bytes, want at least %d", len(b), PerKeyRGBSize)
	}
	var out PerKeyRGB
	for i := range out {
		raw := b[i*4 : i*4+4]
		out[i] = PerKeyLED{LEDID: raw[0], R: raw[1], G: raw[2], B: raw[3]}
	}
	return out, nil
}
