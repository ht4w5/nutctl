package protocol

import "fmt"

// LightingEffect is the decoded GET_LED_EFFECT payload (16 bytes,
// docs/protocol.md §4). The check code (bytes 14..15) is decoded, never
// enforced here — the self-check on it lives in internal/device (ADR-0003).
type LightingEffect struct {
	Mode           uint8   `json:"mode"`
	RGB            [3]byte `json:"rgb"`
	DriverSetting  uint8   `json:"driverSetting"`
	SecondaryRGB   [3]byte `json:"secondaryRgb"`
	ColorMode      uint8   `json:"colorMode"`
	Brightness     uint8   `json:"brightness"`
	Speed          uint8   `json:"speed"`
	Direction      uint8   `json:"direction"`
	EffectModeType uint8   `json:"effectModeType"`
	// CheckCode is the raw byte pair at offsets 14..15.
	CheckCode [2]byte `json:"checkCode"`
	// CheckCodeOK says the check code is a state the protocol recognizes:
	// written (0xAA 0x55 — the vendor app's SET_LED_EFFECT path puts it
	// there) or factory/unwritten (0x00 0x00 — the state observed on a
	// firmware 1.20 Device out of the factory). Any other pair is
	// corruption/misalignment and fails self-check 3 in internal/device.
	CheckCodeOK bool `json:"checkCodeOk"`
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

// PerKeyLED is one Per-Key RGB entry (docs/protocol.md §4: ledId, red, green,
// blue).
type PerKeyLED struct {
	LEDID uint8 `json:"ledId"`
	R     uint8 `json:"r"`
	G     uint8 `json:"g"`
	B     uint8 `json:"b"`
}

// PerKeyRGB is a decoded GET_CUSTOM_LED_DATA payload: 128 entries, index =
// entry id.
type PerKeyRGB [128]PerKeyLED

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
