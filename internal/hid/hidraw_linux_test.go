//go:build linux

package hid

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestParseUeventAndFillIdentity(t *testing.T) {
	uevent := parseUevent([]byte(
		"HID_ID=0003:00000C45:0000880C\nHID_NAME=NUT87\nHID_UNIQ=\n"))
	var info Info
	if err := fillIdentity(&info, uevent); err != nil {
		t.Fatalf("fillIdentity: %v", err)
	}
	if info.VendorID != 0x0C45 || info.ProductID != 0x880C {
		t.Errorf("identity = %#x:%#x, want 0c45:880c", info.VendorID, info.ProductID)
	}

	var broken Info
	if err := fillIdentity(&broken, map[string]string{}); err == nil {
		t.Error("fillIdentity accepted a missing HID_ID, want error")
	}
}

// fakeSysfs builds a minimal sysfs-shaped tree: USB device dir, interface dir
// and HID device dir, plus a /sys/class/hidraw-style symlink pointing at the
// HID device dir — the layout usbStrings actually sees in /sys.
func fakeSysfs(t *testing.T) (hidrawLink, hidDir string) {
	t.Helper()
	root := t.TempDir()
	usbDir := filepath.Join(root, "devices", "usb3", "3-3")
	hidDir = filepath.Join(usbDir, "3-3:1.3", "0003:0C45:880C.0003")
	if err := os.MkdirAll(hidDir, 0o755); err != nil {
		t.Fatal(err)
	}
	classDir := filepath.Join(root, "class", "hidraw", "hidraw2")
	if err := os.MkdirAll(classDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(hidDir, filepath.Join(classDir, "device")); err != nil {
		t.Fatal(err)
	}
	write := func(path, content string) {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(usbDir, "idVendor"), "0c45\n")
	write(filepath.Join(usbDir, "idProduct"), "880c\n")
	write(filepath.Join(usbDir, "manufacturer"), "hfdic\n")
	write(filepath.Join(usbDir, "product"), "NUT87\n")
	write(filepath.Join(hidDir, "uevent"), "HID_ID=0003:00000C45:0000880C\nHID_NAME=hfdic NUT87\n")
	return filepath.Join(classDir, "device"), hidDir
}

func TestUSBStringsWalksUpToTheUSBDevice(t *testing.T) {
	hidrawLink, _ := fakeSysfs(t)
	manufacturer, product := usbStrings(hidrawLink)
	if manufacturer != "hfdic" || product != "NUT87" {
		t.Errorf("usbStrings = %q, %q, want hfdic, NUT87", manufacturer, product)
	}
}

func TestUSBStringsUnknownWithoutUSBDevice(t *testing.T) {
	manufacturer, product := usbStrings(t.TempDir())
	if manufacturer != "" || product != "" {
		t.Errorf("usbStrings = %q, %q, want empty", manufacturer, product)
	}
}

func TestMapOpenErrorHints(t *testing.T) {
	if err := mapOpenError(&os.PathError{Op: "open", Path: "/dev/hidraw3", Err: syscall.EACCES}); !errors.Is(err, ErrPermission) {
		t.Errorf("EACCES mapped to %v, want ErrPermission", err)
	}
	if err := mapOpenError(&os.PathError{Op: "open", Path: "/dev/hidraw3", Err: syscall.EBUSY}); !errors.Is(err, ErrBusy) {
		t.Errorf("EBUSY mapped to %v, want ErrBusy", err)
	}
	if err := mapOpenError(&os.PathError{Op: "open", Path: "/dev/hidraw3", Err: syscall.ENOENT}); !errors.Is(err, ErrNotFound) {
		t.Errorf("ENOENT mapped to %v, want ErrNotFound", err)
	}
}

// TestLockExclusiveRefusesSecondHolder races two opens of one file for the
// exclusive lock: the second must fail with ErrBusy. Closing the first
// releases the lock implicitly, so the second can then take it.
func TestLockExclusiveRefusesSecondHolder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hidraw3")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	open := func() *os.File {
		t.Helper()
		f, err := os.OpenFile(path, os.O_RDWR, 0)
		if err != nil {
			t.Fatal(err)
		}
		return f
	}
	first := open()
	second := open()
	t.Cleanup(func() { first.Close(); second.Close() })

	if err := lockExclusive(first); err != nil {
		t.Fatalf("first lock = %v, want nil", err)
	}
	if err := lockExclusive(second); !errors.Is(err, ErrBusy) {
		t.Errorf("second lock = %v, want ErrBusy", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close first: %v", err)
	}
	if err := lockExclusive(second); err != nil {
		t.Errorf("lock after close = %v, want nil", err)
	}
}

// TestOpenRefusesBusyDeviceAndCloseIsIdempotent runs the real Open path
// against a plain temp file standing in for a hidraw node: while another
// handle holds the lock, Open must fail with ErrBusy; once released, Open
// succeeds and Close may be called repeatedly.
func TestOpenRefusesBusyDeviceAndCloseIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hidraw3")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	holder, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := lockExclusive(holder); err != nil {
		t.Fatalf("lock holder: %v", err)
	}

	e := hidrawEnumerator{}
	if _, err := e.Open(Info{Path: path, ReportLength: 32}); !errors.Is(err, ErrBusy) {
		t.Errorf("Open while held = %v, want ErrBusy", err)
	}
	if err := holder.Close(); err != nil {
		t.Fatalf("close holder: %v", err)
	}

	tr, err := e.Open(Info{Path: path, ReportLength: 32})
	if err != nil {
		t.Fatalf("Open after release: %v", err)
	}
	if err := tr.Close(); err != nil {
		t.Errorf("first Close = %v, want nil", err)
	}
	if err := tr.Close(); err != nil {
		t.Errorf("second Close = %v, want nil", err)
	}
}
