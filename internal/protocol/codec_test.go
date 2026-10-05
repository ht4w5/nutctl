package protocol

import (
	"bytes"
	"testing"
)

// The nut87 fixtures are recorded from real hardware (docs/capture.md Method
// A); these decoders must reproduce exactly what the Device reported.
func TestDecodeDeviceInfoFromFixture(t *testing.T) {
	x := loadFixture(t, "get_device_info", "nut87")
	info, err := DecodeDeviceInfo(reassemble(x.Responses, DeviceInfoSize))
	if err != nil {
		t.Fatalf("DecodeDeviceInfo: %v", err)
	}

	want := DeviceInfo{
		RomSize:         64,
		MacroSpaceSize:  3072,
		VID:             0x0C45,
		PID:             0x880C,
		Version:         "1.20",
		Manufacturer:    32,
		Product:         2,
		ChargeStatus:    2,
		GIFMaxFrames:    200,
		LightingVersion: 1,
		FirmwareStatus:  FirmwareOK,
	}
	if info != want {
		t.Errorf("decode:\n got  %+v\n want %+v", info, want)
	}
}

func TestDecodeSettingsFromFixture(t *testing.T) {
	x := loadFixture(t, "get_game_mode", "nut87")
	settings, err := DecodeSettings(reassemble(x.Responses, SettingsSize))
	if err != nil {
		t.Fatalf("DecodeSettings: %v", err)
	}

	want := Settings{
		ReportRate: ReportRate8K,
		SleepTime:  5,
		KeyDelay:   3,
	}
	if settings != want {
		t.Errorf("decode:\n got  %+v\n want %+v", settings, want)
	}
}

func TestDecodeSeedFixtures(t *testing.T) {
	x := loadFixture(t, "get_device_info", "seed-32byte")
	info, err := DecodeDeviceInfo(reassemble(x.Responses, DeviceInfoSize))
	if err != nil {
		t.Fatalf("DecodeDeviceInfo: %v", err)
	}
	if info.VID != 0x0C45 || info.PID != 0x880C || info.Version != "1.06" {
		t.Errorf("decode = %+v, want vid:pid 0c45:880c version 1.06", info)
	}
}

func TestDecodeRejectsShortPayloads(t *testing.T) {
	if _, err := DecodeDeviceInfo(make([]byte, 32)); err == nil {
		t.Error("DecodeDeviceInfo accepted 32 bytes, want error")
	}
	if _, err := DecodeSettings(make([]byte, 20)); err == nil {
		t.Error("DecodeSettings accepted 20 bytes, want error")
	}
}

func TestReportRateLabels(t *testing.T) {
	for _, tt := range []struct {
		wire ReportRate
		want string
	}{
		{ReportRate1K, "1K"},
		{ReportRate2K, "2K"},
		{ReportRate4K, "4K"},
		{ReportRate8K, "8K"},
		{ReportRate(7), "unknown(7)"},
	} {
		if got := tt.wire.String(); got != tt.want {
			t.Errorf("ReportRate(%d).String() = %q, want %q", uint8(tt.wire), got, tt.want)
		}
	}
}

// The write path (ticket 03): SET_GAME_MODE's wire format is documented in
// docs/protocol.md §4 and quoted from the bundle — a 56-byte block, zero at
// offsets 0, 10, 12 and 13, dead zones as value*100, wirelessReportRate
// u16 LE.
func TestEncodeSettingsUsesSetWireFormat(t *testing.T) {
	s := Settings{
		GameMode:           1,
		FnSwitch:           1,
		SleepTime:          5,
		KeyDelay:           3,
		ReportRate:         ReportRate4K,
		SystemMode:         2,
		TFTDisplayTime:     7,
		TopDeadZone:        0.35,
		BottomDeadZone:     0.10,
		StabilityMode:      1,
		AutoCalibration:    1,
		SingleKeyWakeup:    1,
		PushButtonMode:     2,
		NKROSwitch:         1,
		WirelessReportRate: 1000,
		PowerMode:          3,
	}
	want := make([]byte, SettingsSize)
	copy(want, []byte{
		0, 1, 1, 5, 3, 5, 2, 7, // 0=pad, 1=gameMode .. 7=tftDisplayTime
		35, 10, // 8..9 dead zones (value*100)
		0, 1, // 10=pad, 11=stabilityMode
		0, 0, // 12..13 pad
		1, 1, 2, 1, // 14..17 autoCalibration, singleKeyWakeup, pushButtonMode, nkroSwitch
		0xE8, 0x03, // 18..19 wirelessReportRate u16 LE = 1000
		3, // 20 powerMode
	})
	if got := EncodeSettings(s); !bytes.Equal(got, want) {
		t.Errorf("EncodeSettings = %X, want %X", got, want)
	}
}

// encode(decode(x)) is identity at the typed level: what a read decodes, a
// write reproduces — the round-trip property the State File round-trip rests
// on.
func TestEncodeSettingsRoundTrip(t *testing.T) {
	x := loadFixture(t, "get_game_mode", "nut87")
	payload := reassemble(x.Responses, SettingsSize)
	s, err := DecodeSettings(payload)
	if err != nil {
		t.Fatalf("DecodeSettings: %v", err)
	}
	back, err := DecodeSettings(EncodeSettings(s))
	if err != nil {
		t.Fatalf("DecodeSettings(Encode): %v", err)
	}
	if back != s {
		t.Errorf("typed round trip:\n got  %+v\n want %+v", back, s)
	}
}
