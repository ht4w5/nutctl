// Command nutctl is the CLI for configuring the WEIKAV NUT87 keyboard.
package main

import (
	"context"
	"os"
	"os/signal"

	"github.com/ht4w5/nutctl/internal/cli"
	"github.com/ht4w5/nutctl/internal/hid"
)

func main() {
	// Ctrl-C cancels the running command cleanly (nutctl watch streams until
	// its context is done).
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	os.Exit(cli.Run(os.Args[1:], cli.Deps{
		Devices: hid.NewEnumerator(),
		Stdin:   os.Stdin,
		Stdout:  os.Stdout,
		Stderr:  os.Stderr,
		Context: ctx,
	}))
}
