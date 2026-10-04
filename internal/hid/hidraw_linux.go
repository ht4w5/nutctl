//go:build linux

package hid

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
)

const sysfsHidraw = "/sys/class/hidraw"

// NewEnumerator returns the pure-Go hidraw enumerator (ADR-0006): sysfs for
// enumeration and the report descriptor, /dev/hidraw for report I/O. No
// ioctls are needed — the kernel exposes the report descriptor in sysfs, and
// feature reports (OTA) are out of scope.
func NewEnumerator() Enumerator { return hidrawEnumerator{} }

type hidrawEnumerator struct{}

// Enumerate lists every /dev/hidraw interface with its identity and output
// report length.
func (hidrawEnumerator) Enumerate() ([]Info, error) {
	entries, err := os.ReadDir(sysfsHidraw)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", sysfsHidraw, err)
	}
	var infos []Info
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, "hidraw") {
			continue
		}
		devDir := filepath.Join(sysfsHidraw, name, "device")
		uevent, err := os.ReadFile(filepath.Join(devDir, "uevent"))
		if err != nil {
			continue // interface vanished or unreadable: skip, don't abort
		}
		var info Info
		info.Path = filepath.Join("/dev", name)
		if err := fillIdentity(&info, parseUevent(uevent)); err != nil {
			continue
		}
		info.Manufacturer, info.ProductName = usbStrings(devDir)
		raw, err := os.ReadFile(filepath.Join(devDir, "report_descriptor"))
		if err == nil {
			desc, err := ParseReportDescriptor(raw)
			if err == nil {
				info.UsagePage = desc.UsagePage
				info.ReportLength = desc.OutputLength
				info.Numbered = desc.Numbered
			}
		}
		infos = append(infos, info)
	}
	return infos, nil
}

// Open opens the hidraw device behind info and starts delivering input
// reports.
func (hidrawEnumerator) Open(info Info) (Transport, error) {
	f, err := os.OpenFile(info.Path, os.O_RDWR, 0)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", info.Path, mapOpenError(err))
	}
	t := &hidrawTransport{
		file:      f,
		reportLen: info.ReportLength,
		numbered:  info.Numbered,
		reports:   make(chan []byte, 64),
		done:      make(chan struct{}),
	}
	go t.readLoop()
	return t, nil
}

func mapOpenError(err error) error {
	switch {
	case errors.Is(err, syscall.EACCES), errors.Is(err, syscall.EPERM):
		return fmt.Errorf("%w (is the udev rule installed?)", ErrPermission)
	case errors.Is(err, syscall.EBUSY):
		return ErrBusy
	case errors.Is(err, syscall.ENOENT), errors.Is(err, syscall.ENODEV):
		return ErrNotFound
	default:
		return err
	}
}

type hidrawTransport struct {
	file      *os.File
	reportLen int
	numbered  bool
	reports   chan []byte
	done      chan struct{}
	closeOnce sync.Once
}

func (t *hidrawTransport) ReportLength() int { return t.reportLen }

func (t *hidrawTransport) SendReport(id uint8, report []byte) error {
	// hidraw writes take the report id as the first byte.
	buf := make([]byte, 1+len(report))
	buf[0] = id
	copy(buf[1:], report)
	_, err := t.file.Write(buf)
	return err
}

func (t *hidrawTransport) Reports() <-chan []byte { return t.reports }

func (t *hidrawTransport) readLoop() {
	defer close(t.reports)
	buf := make([]byte, 4096)
	for {
		n, err := t.file.Read(buf)
		if err != nil {
			return // closed or device gone
		}
		raw := make([]byte, n)
		copy(raw, buf[:n])
		if t.numbered && len(raw) > 1 {
			raw = raw[1:] // drop the report id prefix
		}
		select {
		case t.reports <- raw:
		case <-t.done:
			return
		}
	}
}

func (t *hidrawTransport) Close() error {
	t.closeOnce.Do(func() { close(t.done) })
	return t.file.Close()
}

// parseUevent parses a kernel uevent file into its KEY=VALUE pairs.
func parseUevent(b []byte) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(string(b), "\n") {
		if k, v, ok := strings.Cut(line, "="); ok {
			out[k] = v
		}
	}
	return out
}

// fillIdentity reads the HID_ID line: "0003:00000C45:0000880C" =
// bus:vendor:product in hex.
func fillIdentity(info *Info, uevent map[string]string) error {
	id := uevent["HID_ID"]
	parts := strings.Split(id, ":")
	if len(parts) != 3 {
		return fmt.Errorf("bad HID_ID %q", id)
	}
	vid, err := strconv.ParseUint(parts[1], 16, 16)
	if err != nil {
		return fmt.Errorf("bad HID_ID vendor %q: %w", parts[1], err)
	}
	pid, err := strconv.ParseUint(parts[2], 16, 16)
	if err != nil {
		return fmt.Errorf("bad HID_ID product %q: %w", parts[2], err)
	}
	info.VendorID = uint16(vid)
	info.ProductID = uint16(pid)
	return nil
}

// usbStrings walks up from the HID interface directory to the USB device
// directory (the one carrying idVendor) and reads the manufacturer/product
// string attributes. The hidraw device path is a symlink into
// /sys/devices/..., so it must be resolved before walking up.
func usbStrings(devDir string) (manufacturer, product string) {
	dir, err := filepath.EvalSymlinks(devDir)
	if err != nil {
		dir = devDir
	}
	for i := 0; i < 8; i++ {
		if _, err := os.Stat(filepath.Join(dir, "idVendor")); err == nil {
			return readTrim(filepath.Join(dir, "manufacturer")),
				readTrim(filepath.Join(dir, "product"))
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", ""
}

func readTrim(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}
