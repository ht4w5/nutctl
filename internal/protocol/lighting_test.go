package protocol

import "testing"

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
