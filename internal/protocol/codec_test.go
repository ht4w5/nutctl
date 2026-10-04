package protocol

import (
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
