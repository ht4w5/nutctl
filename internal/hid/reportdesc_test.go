package hid

import "testing"

// item encodes one HID short item: prefix = [ tag:4 | type:2 | size:2 ].
func item(typ, tag byte, size int, data uint32) []byte {
	sizeCode := byte(0)
	switch size {
	case 1:
		sizeCode = 1
	case 2:
		sizeCode = 2
	case 4:
		sizeCode = 3
	}
	b := []byte{tag<<4 | typ<<2 | sizeCode}
	for i := 0; i < size; i++ {
		b = append(b, byte(data>>(8*i)))
	}
	return b
}

func desc(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

const (
	typMain   = 0
	typGlobal = 1
	typLocal  = 2
)

func TestParseReportDescriptor(t *testing.T) {
	tests := []struct {
		name       string
		raw        []byte
		wantPage   uint16
		wantLen    int
		wantNumber bool
	}{
		{
			name: "unnumbered vendor output report, 32 bytes",
			raw: desc(
				item(typGlobal, 0x0, 2, 0xFF68), // Usage Page (Vendor 0xFF68)
				item(typLocal, 0x0, 1, 0x01),    // Usage
				item(typMain, 0xA, 1, 0x01),     // Collection (Application)
				item(typGlobal, 0x7, 1, 8),      // Report Size 8
				item(typGlobal, 0x9, 1, 32),     // Report Count 32
				item(typLocal, 0x0, 1, 0x02),    // Usage
				item(typMain, 0x9, 1, 0x02),     // Output (Data,Var,Abs)
				item(typLocal, 0x0, 1, 0x03),    // Usage
				item(typMain, 0x8, 1, 0x02),     // Input (Data,Var,Abs)
				item(typMain, 0xC, 0, 0),        // End Collection
			),
			wantPage:   0xFF68,
			wantLen:    32,
			wantNumber: false,
		},
		{
			name: "64-byte output report variant",
			raw: desc(
				item(typGlobal, 0x0, 2, 0xFF00),
				item(typLocal, 0x0, 1, 0x01),
				item(typMain, 0xA, 1, 0x01),
				item(typGlobal, 0x7, 1, 8),  // Report Size 8
				item(typGlobal, 0x9, 1, 64), // Report Count 64
				item(typLocal, 0x0, 1, 0x02),
				item(typMain, 0x9, 1, 0x02), // Output
				item(typMain, 0xC, 0, 0),
			),
			wantPage:   0xFF00,
			wantLen:    64,
			wantNumber: false,
		},
		{
			name: "numbered reports, first output report is id 1",
			raw: desc(
				item(typGlobal, 0x0, 2, 0xFF80),
				item(typLocal, 0x0, 1, 0x01),
				item(typMain, 0xA, 1, 0x01),
				item(typGlobal, 0x8, 1, 1),  // Report ID 1
				item(typGlobal, 0x7, 1, 8),  // Report Size 8
				item(typGlobal, 0x9, 1, 32), // Report Count 32
				item(typLocal, 0x0, 1, 0x02),
				item(typMain, 0x9, 1, 0x02), // Output
				item(typGlobal, 0x8, 1, 2),  // Report ID 2
				item(typLocal, 0x0, 1, 0x03),
				item(typMain, 0x8, 1, 0x02), // Input
				item(typMain, 0xC, 0, 0),
			),
			wantPage:   0xFF80,
			wantLen:    32,
			wantNumber: true,
		},
		{
			name: "output split across two items sums up",
			raw: desc(
				item(typGlobal, 0x0, 2, 0xFF68),
				item(typLocal, 0x0, 1, 0x01),
				item(typMain, 0xA, 1, 0x01),
				item(typGlobal, 0x7, 1, 8),  // Report Size 8
				item(typGlobal, 0x9, 1, 16), // Report Count 16
				item(typLocal, 0x0, 1, 0x02),
				item(typMain, 0x9, 1, 0x02), // Output
				item(typGlobal, 0x9, 1, 16), // Report Count 16
				item(typLocal, 0x0, 1, 0x03),
				item(typMain, 0x9, 1, 0x02), // Output
				item(typMain, 0xC, 0, 0),
			),
			wantPage:   0xFF68,
			wantLen:    32,
			wantNumber: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseReportDescriptor(tt.raw)
			if err != nil {
				t.Fatalf("ParseReportDescriptor: %v", err)
			}
			if got.UsagePage != tt.wantPage {
				t.Errorf("UsagePage = %#x, want %#x", got.UsagePage, tt.wantPage)
			}
			if got.OutputLength != tt.wantLen {
				t.Errorf("OutputLength = %d, want %d", got.OutputLength, tt.wantLen)
			}
			if got.Numbered != tt.wantNumber {
				t.Errorf("Numbered = %v, want %v", got.Numbered, tt.wantNumber)
			}
		})
	}
}

func TestParseReportDescriptorNoOutputReport(t *testing.T) {
	raw := desc(
		item(typGlobal, 0x0, 2, 0x0001),
		item(typLocal, 0x0, 1, 0x06),
		item(typMain, 0xA, 1, 0x01),
		item(typGlobal, 0x7, 1, 8),
		item(typGlobal, 0x9, 1, 8),
		item(typMain, 0x8, 1, 0x02), // Input only
		item(typMain, 0xC, 0, 0),
	)
	got, err := ParseReportDescriptor(raw)
	if err != nil {
		t.Fatalf("ParseReportDescriptor: %v", err)
	}
	if got.OutputLength != 0 {
		t.Errorf("OutputLength = %d, want 0", got.OutputLength)
	}
}
