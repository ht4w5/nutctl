package protocol

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// The check code (bytes 14..15) is decoded into CheckCode (raw) and
// CheckCodeOK (a recognized state), never enforced: a wrong check code is a
// self-check failure in internal/device, not a decode failure here.
// Recognized states, calibrated to hardware evidence (firmware 1.20):
// 0xAA 0x55 (written by the vendor app's SET_LED_EFFECT path) and 0x00 0x00
// (factory/unwritten — the observed state).
func TestDecodeLightingEffectCheckCode(t *testing.T) {
	for _, ok := range [][2]byte{{0xAA, 0x55}, {0x00, 0x00}} {
		payload := make([]byte, LEDEffectSize)
		payload[14], payload[15] = ok[0], ok[1]
		effect, err := DecodeLightingEffect(payload)
		if err != nil {
			t.Fatalf("DecodeLightingEffect(%X %X): %v", ok[0], ok[1], err)
		}
		if !effect.CheckCodeOK {
			t.Errorf("CheckCodeOK = false with recognized state %X %X at 14..15, want true", ok[0], ok[1])
		}
		if effect.CheckCode != ok {
			t.Errorf("CheckCode = %X, want raw bytes %X preserved", effect.CheckCode, ok)
		}
	}

	for _, bad := range [][2]byte{{0x55, 0xAA}, {0xAA, 0x54}, {0xAB, 0x55}, {0x12, 0x34}} {
		payload := make([]byte, LEDEffectSize)
		payload[14], payload[15] = bad[0], bad[1]
		effect, err := DecodeLightingEffect(payload)
		if err != nil {
			t.Fatalf("DecodeLightingEffect(%X %X): %v (wrong check code must not fail the decode)", bad[0], bad[1], err)
		}
		if effect.CheckCodeOK {
			t.Errorf("CheckCodeOK = true with %X %X at 14..15, want false", bad[0], bad[1])
		}
		if effect.CheckCode != bad {
			t.Errorf("CheckCode = %X, want raw bytes %X preserved", effect.CheckCode, bad)
		}
	}
}

func TestDecodeLightingEffectRejectsShortPayloads(t *testing.T) {
	if _, err := DecodeLightingEffect(make([]byte, LEDEffectSize-1)); err == nil {
		t.Error("DecodeLightingEffect accepted 15 bytes, want error")
	}
}

func TestDecodePerKeyRGBRejectsShortPayloads(t *testing.T) {
	if _, err := DecodePerKeyRGB(make([]byte, PerKeyRGBSize-1)); err == nil {
		t.Error("DecodePerKeyRGB accepted 511 bytes, want error")
	}
}

func TestDecodePerKeyRGBFields(t *testing.T) {
	payload := make([]byte, PerKeyRGBSize)
	// entry 3: ledId 3, red/green/blue 0x10/0x20/0x30
	copy(payload[3*4:], []byte{3, 0x10, 0x20, 0x30})

	rgb, err := DecodePerKeyRGB(payload)
	if err != nil {
		t.Fatalf("DecodePerKeyRGB: %v", err)
	}
	if want := (PerKeyLED{LEDID: 3, R: 0x10, G: 0x20, B: 0x30}); rgb[3] != want {
		t.Errorf("entry 3 = %+v, want %+v", rgb[3], want)
	}
	if rgb[2] != (PerKeyLED{}) {
		t.Errorf("entry 2 = %+v, want the zero entry", rgb[2])
	}
	if rgb[127] != (PerKeyLED{}) {
		t.Errorf("entry 127 = %+v, want the zero entry", rgb[127])
	}
}

// The write path (ticket 03): SET_LED_EFFECT's wire format is documented in
// docs/protocol.md §4 and quoted from the bundle — byte 4 (driverSetting) is
// forced to 0xFF and the check code at 14..15 to 0xAA 0x55 on every write.
// Those two positions are wire markers of the SET format, not state.
func TestEncodeLightingEffectUsesSetWireFormat(t *testing.T) {
	effect := LightingEffect{
		Mode:           5,
		RGB:            [3]byte{1, 2, 3},
		DriverSetting:  0, // whatever was read; the SET format forces 0xFF
		SecondaryRGB:   [3]byte{4, 5, 6},
		ColorMode:      7,
		Brightness:     4,
		Speed:          2,
		Direction:      1,
		EffectModeType: 3,
		CheckCode:      [2]byte{0, 0}, // read state; the SET format forces 0xAA 0x55
	}
	want := []byte{5, 1, 2, 3, 0xFF, 4, 5, 6, 7, 4, 2, 1, 3, 0, 0xAA, 0x55}
	if got := EncodeLightingEffect(effect); !bytes.Equal(got, want) {
		t.Errorf("EncodeLightingEffect = %X, want %X", got, want)
	}
}

// The write path (ticket 03): SET_CUSTOM_LED_DATA writes the entry INDEX as
// the ledId (docs/protocol.md §4, quoted from the bundle: `l[f]=i`) — the
// ledId byte is derived on write, never taken from the block.
func TestEncodePerKeyRGBWritesIndexAsLEDID(t *testing.T) {
	var rgb PerKeyRGB
	rgb[0] = PerKeyLED{LEDID: 9, R: 0x10, G: 0x20, B: 0x30} // ledId ignored
	rgb[127] = PerKeyLED{LEDID: 127, R: 0xAA, G: 0xBB, B: 0xCC}

	got := EncodePerKeyRGB(rgb)
	if want := []byte{0, 0x10, 0x20, 0x30}; !bytes.Equal(got[0:4], want) {
		t.Errorf("entry 0 = %X, want %X (ledId must be the index)", got[0:4], want)
	}
	if want := []byte{127, 0xAA, 0xBB, 0xCC}; !bytes.Equal(got[127*4:128*4], want) {
		t.Errorf("entry 127 = %X, want %X", got[127*4:128*4], want)
	}
}

// State File JSON of the Lighting Effect (ticket 03): only the fields that
// are Device state. driverSetting and the check code are wire markers the
// SET format forces (0xFF, 0xAA 0x55 — docs/protocol.md §4), so a State File
// never carries them and save → load → save round-trips regardless of what
// the Device reports at those positions.
func TestLightingEffectStateJSONOmitsWireMarkers(t *testing.T) {
	e := LightingEffect{
		Mode: 5, RGB: [3]byte{1, 2, 3}, DriverSetting: 0xFF,
		SecondaryRGB: [3]byte{4, 5, 6}, ColorMode: 7, Brightness: 4,
		Speed: 2, Direction: 1, EffectModeType: 3, CheckCode: [2]byte{0xAA, 0x55}, CheckCodeOK: true,
	}
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	for _, marker := range []string{"driverSetting", "checkCode", "checkCodeOk"} {
		if strings.Contains(string(b), marker) {
			t.Errorf("Marshal = %s, want no %q (a wire marker of the SET format, not state)", b, marker)
		}
	}
	var got LightingEffect
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.Mode != 5 || got.RGB != [3]byte{1, 2, 3} || got.Brightness != 4 || got.EffectModeType != 3 {
		t.Errorf("round trip = %+v, want the state fields preserved", got)
	}
}

// State File JSON of a Per-Key RGB entry: the color only. The ledId byte is
// the entry index on the wire (the SET format writes `i` there), so it is
// derived data, never state.
func TestPerKeyLEDStateJSONOmitsLEDID(t *testing.T) {
	e := PerKeyLED{LEDID: 9, R: 0x10, G: 0x20, B: 0x30}
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if want := `{"r":16,"g":32,"b":48}`; string(b) != want {
		t.Errorf("Marshal = %s, want %s", b, want)
	}
	var got PerKeyLED
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got != (PerKeyLED{R: 0x10, G: 0x20, B: 0x30}) {
		t.Errorf("round trip = %+v, want the color preserved and ledId derived", got)
	}
}
