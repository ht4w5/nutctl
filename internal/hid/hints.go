package hid

import "errors"

// Actionable hints for the errors this seam returns. Every UI (CLI and TUI)
// surfaces them the same way, so a broken environment is fixable instead of
// guessable (spec user story 29).

// UdevHint is the fix for a Device that cannot be opened for lack of
// permissions (ErrPermission), and for "no device found" on a machine where
// the Device is plugged in but invisible.
const UdevHint = `hint: install the udev rule so an unprivileged user can open the Device:
  sudo cp udev/60-nut87.rules /etc/udev/rules.d/
  sudo udevadm control --reload && sudo udevadm trigger
then re-plug the keyboard`

// BusyHint is the fix for a Device another process already holds (ErrBusy).
const BusyHint = `hint: another nutctl session (or another tool) is reading this Device — close it and retry (also quit the vendor app if it is running)`

// Hint returns the actionable hint for an error from this seam, or "" when
// the error carries none.
func Hint(err error) string {
	switch {
	case errors.Is(err, ErrPermission):
		return UdevHint
	case errors.Is(err, ErrBusy):
		return BusyHint
	}
	return ""
}
