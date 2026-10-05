package protocol

// Device notify traffic (docs/protocol.md §3): the Device pushes these
// unsolicited on the input stream — they are never answers to a request.
// The vendor bundle only ever listens for them (decoded/layout-classic-
// DSv6_q0d.js): startDeviceStateListener matches `55 FA <type>`,
// start24GDisconnectListener / startResetListener / start24GSleepListener
// match the GET_24G_DISCONNECT_NOTIFY subtypes 4 / 5 / 6, and
// start24GWakeListener matches the out-of-band `A6 FF 01` wake report.
const (
	CmdGetDeviceNotify        byte = 250
	CmdGet24GDisconnectNotify byte = 252
)

// GET_24G_DISCONNECT_NOTIFY subtypes (report byte 2) and the wake report's
// fixed magic prefix, quoted from the bundle's listeners above.
const (
	notify24GDisconnectType byte = 4
	notifyDeviceResetType   byte = 5
	notify24GSleepType      byte = 6
	notifyWakeMagic         byte = 0xA6
	notifyWakeType          byte = 0x01
)

// NotifyKind classifies one notify report. The kinds are the listeners the
// bundle installs; anything else the notify commands carry is kept whole as
// Notify24GOther rather than guessed at.
type NotifyKind int

const (
	// NotifyDeviceNotify is a GET_DEVICE_NOTIFY report (cmd 250); its type
	// and byte 3 (a state flag the bundle's type-6 listener reads) are the
	// observable facts.
	NotifyDeviceNotify NotifyKind = iota
	Notify24GDisconnect
	NotifyDeviceReset
	Notify24GSleep
	Notify24GWake
	Notify24GOther
)

// Notify is one decoded device notify report. Raw keeps the whole input
// report: a quirk chase needs the bytes, not just their name.
type Notify struct {
	Kind NotifyKind
	Type byte   // notify type (report byte 2); 0 for the wake report
	Raw  []byte // the whole input report
}

// ParseNotify decodes one input report as device notify traffic. Reports that
// are not notify traffic (ordinary responses, garbage, near misses) are
// refused — the transfer layer owns those.
func ParseNotify(report []byte) (Notify, bool) {
	if len(report) < 3 {
		return Notify{}, false
	}
	switch {
	case report[0] == ResponseMagic && report[1] == CmdGetDeviceNotify:
		return Notify{Kind: NotifyDeviceNotify, Type: report[2], Raw: report}, true
	case report[0] == ResponseMagic && report[1] == CmdGet24GDisconnectNotify:
		n := Notify{Type: report[2], Raw: report}
		switch report[2] {
		case notify24GDisconnectType:
			n.Kind = Notify24GDisconnect
		case notifyDeviceResetType:
			n.Kind = NotifyDeviceReset
		case notify24GSleepType:
			n.Kind = Notify24GSleep
		default:
			n.Kind = Notify24GOther
		}
		return n, true
	case report[0] == notifyWakeMagic && report[1] == 0xFF && report[2] == notifyWakeType:
		return Notify{Kind: Notify24GWake, Raw: report}, true
	}
	return Notify{}, false
}
