// Command aits runs coding agents from the command line.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/agent-in-the-shell/aits/internal/cli"
)

func main() {
	// SIGTERM and SIGINT cancel the context; running commands stop their
	// children and still report a result (P10).
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	code := cli.Run(ctx, os.Args[1:], cli.Env{
		Stdin:   os.Stdin,
		Stdout:  os.Stdout,
		Stderr:  os.Stderr,
		Environ: os.Environ(),
	})
	stop()
	os.Exit(code)
}
