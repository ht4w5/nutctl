package hid

import (
	"fmt"
)

// ReportDesc holds the few facts the transport needs from a HID report
// descriptor: the usage page of the first collection (used to recognize the
// Device's vendor interface), and the shape of the output report the protocol
// chunks over. Parsed per the HID 1.11 short-item encoding.
type ReportDesc struct {
	UsagePage      uint16 // usage page of the first top-level collection
	Numbered       bool   // descriptor uses numbered report ids
	OutputLength   int    // byte length of the first output report
	OutputReportID byte   // report id of the first output report
}

// Short-item prefix fields: [ tag:4 | type:2 | size:2 ].
const (
	itemTypeMain   = 0
	itemTypeGlobal = 1
	itemTypeLocal  = 2

	tagGlobalUsagePage   = 0x0
	tagGlobalReportSize  = 0x7
	tagGlobalReportID    = 0x8
	tagGlobalReportCount = 0x9

	tagMainInput      = 0x8
	tagMainOutput     = 0x9
	tagMainCollection = 0xA
	tagMainEndColl    = 0xC
)

// ParseReportDescriptor extracts ReportDesc from raw descriptor bytes.
func ParseReportDescriptor(raw []byte) (ReportDesc, error) {
	var (
		out             ReportDesc
		usagePage       uint16
		reportSize      uint32
		reportCount     uint32
		reportID        byte
		numbered        bool
		depth           int
		seenCollection  bool
		seenOutput      bool
		firstOutputID   byte
		firstOutputBits uint32
	)

	for i := 0; i < len(raw); {
		prefix := raw[i]
		i++
		if prefix == 0xFE { // long item: 0xFE, data-size, long-tag, data...
			if i+1 >= len(raw) {
				return out, fmt.Errorf("hid report descriptor: truncated long item")
			}
			i += 1 + int(raw[i+1])
			continue
		}
		size := 0
		switch prefix & 0x03 {
		case 1:
			size = 1
		case 2:
			size = 2
		case 3:
			size = 4
		}
		if i+size > len(raw) {
			return out, fmt.Errorf("hid report descriptor: truncated item at offset %d", i-1)
		}
		var data uint32
		for n := 0; n < size; n++ {
			data |= uint32(raw[i+n]) << (8 * n)
		}
		i += size

		typ := (prefix >> 2) & 0x03
		tag := prefix >> 4

		switch typ {
		case itemTypeGlobal:
			switch tag {
			case tagGlobalUsagePage:
				usagePage = uint16(data)
			case tagGlobalReportSize:
				reportSize = data
			case tagGlobalReportCount:
				reportCount = data
			case tagGlobalReportID:
				reportID = byte(data)
				numbered = true
			}
		case itemTypeMain:
			switch tag {
			case tagMainCollection:
				if depth == 0 && !seenCollection {
					out.UsagePage = usagePage
					seenCollection = true
				}
				depth++
			case tagMainEndColl:
				if depth > 0 {
					depth--
				}
			case tagMainInput:
			case tagMainOutput:
				if !seenOutput {
					seenOutput = true
					firstOutputID = reportID
				}
				if reportID == firstOutputID {
					firstOutputBits += reportSize * reportCount
				}
			}
		}
	}

	out.Numbered = numbered
	out.OutputLength = int((firstOutputBits + 7) / 8)
	out.OutputReportID = firstOutputID
	return out, nil
}
