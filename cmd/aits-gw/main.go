// Command aits-gw records events for agents and scripts: the gateway that
// P15(4) allows as a second binary (#163). Stage 0 is shadow mode (#165).
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/agent-in-the-shell/aits/internal/cli"
	"github.com/agent-in-the-shell/aits/internal/gw"
)

func main() {
	// SIGTERM, SIGINT and SIGHUP cancel the context: serve stops after the
	// event in hand and still prints its result (P10).
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt, syscall.SIGHUP)
	code := cli.Run(ctx, gw.Root(), os.Args[1:], cli.Env{
		Stdin:   os.Stdin,
		Stdout:  os.Stdout,
		Stderr:  os.Stderr,
		Environ: os.Environ(),
		Self:    os.Args[0],
	})
	stop()
	os.Exit(code)
}
