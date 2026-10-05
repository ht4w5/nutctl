// Command nutctl is the CLI for configuring the WEIKAV NUT87 keyboard.
package main

import (
	"os"

	"github.com/ht4w5/nutctl/internal/cli"
	"github.com/ht4w5/nutctl/internal/hid"
)

func main() {
	os.Exit(cli.Run(os.Args[1:], cli.Deps{
		Devices: hid.NewEnumerator(),
		Stdin:   os.Stdin,
		Stdout:  os.Stdout,
		Stderr:  os.Stderr,
	}))
}
