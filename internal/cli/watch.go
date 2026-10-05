package cli

import (
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/ht4w5/nutctl/internal/device"
	"github.com/ht4w5/nutctl/internal/protocol"
)

// --- `nutctl watch` ---
//
// Observability (ticket 08): stream the Device's notify traffic live — the
// 2.4G disconnect/sleep/wake events, device-side resets and the device state
// notifies (spec user story 30) — so wireless quirks are debuggable later.
// watch is read-only: it opens the Device, reads nothing but the probe and
// prints whatever the Device pushes.

// runWatch streams device notify traffic until the context is done
// (Ctrl-C) or the Device disappears. The exit code tells which: an
// interrupted watch is a clean stop, a vanished Device is not.
func runWatch(args []string, deps Deps) int {
	fs := flag.NewFlagSet("watch", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	selector := fs.String("device", "", "hidraw path of the Device to use")
	if err := fs.Parse(args); err != nil {
		return usageError(deps, err)
	}
	if fs.NArg() > 0 {
		return usageError(deps, fmt.Errorf("unexpected argument %q", fs.Arg(0)))
	}

	s, err := device.Open(deps.Devices, *selector)
	if err != nil {
		return fail(deps, err)
	}
	defer s.Close()

	fmt.Fprintf(deps.Stdout, "watching %s at %s (firmware %s) — device notify traffic, press Ctrl-C to stop\n",
		s.Model.Name, s.Info.Path, s.DeviceInfo.Version)

	for {
		select {
		case raw, ok := <-s.Dev.Notifications():
			if !ok {
				fmt.Fprintln(deps.Stderr, "device disconnected — watch stopped")
				return 1
			}
			printNotify(deps.Stdout, raw)
		case <-deps.ctx().Done():
			fmt.Fprintln(deps.Stderr, "watch stopped")
			return 0
		}
	}
}

// printNotify renders one watched input report: the decoded name of device
// notify traffic beside its raw bytes (a quirk chase needs the bytes), or
// the bytes alone with a bare label when the report is not notify traffic.
func printNotify(out io.Writer, raw []byte) {
	name := "input report"
	if n, ok := protocol.ParseNotify(raw); ok {
		name = protocol.NotifyText(n)
	}
	fmt.Fprintf(out, "%s  %-22s %s\n", time.Now().Format("15:04:05.000"), name, protocol.HexBytes(raw))
}
