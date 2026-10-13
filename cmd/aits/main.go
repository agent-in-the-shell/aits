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
	// SIGTERM, SIGINT and SIGHUP cancel the context; running commands stop
	// their children and still report a result (P10). The agent runs in its own
	// process group, so a closed terminal reaches it only through aits.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt, syscall.SIGHUP)
	code := cli.Run(ctx, os.Args[1:], cli.Env{
		Stdin:   os.Stdin,
		Stdout:  os.Stdout,
		Stderr:  os.Stderr,
		Environ: os.Environ(),
	})
	stop()
	os.Exit(code)
}
