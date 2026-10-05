package protocol

import (
	"encoding/json"
	"fmt"
	"math"
)

// DeviceInfo is the decoded GET_DEVICE_INFO payload (docs/protocol.md §4,
// 48 bytes; fields run to offset 32).
type DeviceInfo struct {
	RomSize         uint8  `json:"romSize"`
	MacroSpaceSize  uint16 `json:"macroSpaceSize"`
	VID             uint16 `json:"vid"`
	PID             uint16 `json:"pid"`
	Version         string `json:"version"` // e.g. "1.06"
	Sensor          uint16 `json:"sensor"`
	Manufacturer    uint16 `json:"manufacturer"`
	Product         uint16 `json:"product"`
	WorkMode        uint8  `json:"workMode"`
	BatteryLevel    uint8  `json:"batteryLevel"`
	ChargeStatus    uint8  `json:"chargeStatus"`
	CurrentProfile  uint8  `json:"currentProfile"`
	AxisInfo        uint16 `json:"axisInfo"`
	TFTMaxFrames    uint16 `json:"tftMaxFrames"`
	GIFMaxFrames    uint16 `json:"gifMaxFrames"`
	LEDMaxFrames    uint16 `json:"ledMaxFrames"`
	TFTDirection    uint8  `json:"tftDirection"` // 0xFF = no TFT
	RTPrecision     uint8  `json:"rtPrecision"`
	FrameVersion    uint8  `json:"frameVersion"` // 1 → older timing + SN via OTA
	LightingVersion uint8  `json:"lightingVersion"`
	FirmwareStatus  uint8  `json:"firmwareStatus"` // 1 = bootloader / corrupted app
}

// FirmwareStatus values (docs/protocol.md §4).
const (
	FirmwareOK         = 0
	FirmwareBootloader = 1
)

// deviceInfoMinSize is the offset of the last decoded field plus one.
const deviceInfoMinSize = 33

// DecodeDeviceInfo decodes a reassembled GET_DEVICE_INFO payload.
func DecodeDeviceInfo(b []byte) (DeviceInfo, error) {
	if len(b) < deviceInfoMinSize {
		return DeviceInfo{}, fmt.Errorf("device info payload too short: %d bytes, want at least %d", len(b), deviceInfoMinSize)
	}
	// Version is BCD-ish: tenths and units in b8, hundreds in b9.
	v := int(b[8]&0x0F) + int((b[8]&0xF0)>>4)*10 + int(b[9])*100
	return DeviceInfo{
		RomSize:         b[0],
		MacroSpaceSize:  u16le(b[2:]),
		VID:             u16le(b[4:]),
		PID:             u16le(b[6:]),
		Version:         fmt.Sprintf("%d.%02d", v/100, v%100),
		Sensor:          u16le(b[10:]),
		Manufacturer:    u16le(b[12:]),
		Product:         u16le(b[14:]),
		WorkMode:        b[16],
		BatteryLevel:    b[17],
		ChargeStatus:    b[18],
		CurrentProfile:  b[19],
		AxisInfo:        u16le(b[20:]),
		TFTMaxFrames:    u16le(b[22:]),
		GIFMaxFrames:    u16le(b[24:]),
		LEDMaxFrames:    u16le(b[26:]),
		TFTDirection:    b[28],
		RTPrecision:     b[29],
		FrameVersion:    b[30],
		LightingVersion: b[31],
		FirmwareStatus:  b[32],
	}, nil
}

// ReportRate is the wire enum for the Settings block's polling rate
// (docs/protocol.md §4): 1K→3, 2K→4, 4K→5, 8K→6. The NUT87 offers 1K/4K/8K.
type ReportRate uint8

const (
	ReportRate1K ReportRate = 3
	ReportRate2K ReportRate = 4
	ReportRate4K ReportRate = 5
	ReportRate8K ReportRate = 6
)

// String renders the rate the way the docs and the UI do ("4K").
func (r ReportRate) String() string {
	switch r {
	case ReportRate1K:
		return "1K"
	case ReportRate2K:
		return "2K"
	case ReportRate4K:
		return "4K"
	case ReportRate8K:
		return "8K"
	default:
		return fmt.Sprintf("unknown(%d)", uint8(r))
	}
}

// MarshalJSON renders the rate as its label, so --json output is stable and
// readable.
func (r ReportRate) MarshalJSON() ([]byte, error) {
	return json.Marshal(r.String())
}

// UnmarshalJSON parses the rate from its label ("4K"), the inverse of
// MarshalJSON — State Files carry the label.
func (r *ReportRate) UnmarshalJSON(b []byte) error {
	var label string
	if err := json.Unmarshal(b, &label); err != nil {
		return err
	}
	switch label {
	case "1K":
		*r = ReportRate1K
	case "2K":
		*r = ReportRate2K
	case "4K":
		*r = ReportRate4K
	case "8K":
		*r = ReportRate8K
	default:
		return fmt.Errorf("unknown Report Rate %q (want 1K, 2K, 4K or 8K)", label)
	}
	return nil
}

// Settings is the decoded GET_GAME_MODE / SET_GAME_MODE payload
// (docs/protocol.md §4, 56 bytes; fields run to offset 20).
type Settings struct {
	GameMode           uint8      `json:"gameMode"`
	FnSwitch           uint8      `json:"fnSwitch"`
	SleepTime          uint8      `json:"sleepTime"`
	KeyDelay           uint8      `json:"keyDelay"`
	ReportRate         ReportRate `json:"reportRate"`
	SystemMode         uint8      `json:"systemMode"`
	TFTDisplayTime     uint8      `json:"tftDisplayTime"`
	TopDeadZone        float64    `json:"topDeadZone"`    // wire value/100
	BottomDeadZone     float64    `json:"bottomDeadZone"` // wire value/100
	StabilityMode      uint8      `json:"stabilityMode"`
	AutoCalibration    uint8      `json:"autoCalibration"`
	SingleKeyWakeup    uint8      `json:"singleKeyWakeup"`
	PushButtonMode     uint8      `json:"pushButtonMode"`
	NKROSwitch         uint8      `json:"nkroSwitch"`
	WirelessReportRate uint16     `json:"wirelessReportRate"` // raw Hz, 2.4G only
	PowerMode          uint8      `json:"powerMode"`
}

// settingsMinSize is the offset of the last decoded field plus one.
const settingsMinSize = 21

// DecodeSettings decodes a reassembled GET_GAME_MODE payload.
func DecodeSettings(b []byte) (Settings, error) {
	if len(b) < settingsMinSize {
		return Settings{}, fmt.Errorf("settings payload too short: %d bytes, want at least %d", len(b), settingsMinSize)
	}
	return Settings{
		GameMode:           b[1],
		FnSwitch:           b[2],
		SleepTime:          b[3],
		KeyDelay:           b[4],
		ReportRate:         ReportRate(b[5]),
		SystemMode:         b[6],
		TFTDisplayTime:     b[7],
		TopDeadZone:        float64(b[8]) / 100,
		BottomDeadZone:     float64(b[9]) / 100,
		StabilityMode:      b[11],
		AutoCalibration:    b[14],
		SingleKeyWakeup:    b[15],
		PushButtonMode:     b[16],
		NKROSwitch:         b[17],
		WirelessReportRate: u16le(b[18:]),
		PowerMode:          b[20],
	}, nil
}

// EncodeSettings encodes Settings into its 56-byte GET_GAME_MODE /
// SET_GAME_MODE payload (docs/protocol.md §4): zero at offsets 0, 10, 12 and
// 13, dead zones as value*100, wirelessReportRate u16 LE. Byte-exact with
// the vendor bundle's SET_GAME_MODE encoder.
func EncodeSettings(s Settings) []byte {
	out := make([]byte, SettingsSize)
	out[1] = s.GameMode
	out[2] = s.FnSwitch
	out[3] = s.SleepTime
	out[4] = s.KeyDelay
	out[5] = byte(s.ReportRate)
	out[6] = s.SystemMode
	out[7] = s.TFTDisplayTime
	out[8] = byte(math.Round(s.TopDeadZone * 100))
	out[9] = byte(math.Round(s.BottomDeadZone * 100))
	out[11] = s.StabilityMode
	out[14] = s.AutoCalibration
	out[15] = s.SingleKeyWakeup
	out[16] = s.PushButtonMode
	out[17] = s.NKROSwitch
	out[18] = byte(s.WirelessReportRate)
	out[19] = byte(s.WirelessReportRate >> 8)
	out[20] = s.PowerMode
	return out
}

func u16le(b []byte) uint16 {
	return uint16(b[0]) | uint16(b[1])<<8
}
