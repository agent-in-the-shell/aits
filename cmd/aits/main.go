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
	// their children and still report a result (P10). A second one closes
	// kill, which ends a stopping agent's group at once. Later ones are
	// ignored: aits still reaps the group and reports. The agent runs in its
	// own process group, so a closed terminal reaches it only through aits.
	ctx, cancel := context.WithCancel(context.Background())
	kill := make(chan struct{})
	sigs := make(chan os.Signal, 2)
	signal.Notify(sigs, syscall.SIGTERM, os.Interrupt, syscall.SIGHUP)
	go func() {
		<-sigs
		cancel()
		<-sigs
		close(kill)
	}()
	os.Exit(cli.Run(ctx, os.Args[1:], cli.Env{
		Stdin:   os.Stdin,
		Stdout:  os.Stdout,
		Stderr:  os.Stderr,
		Environ: os.Environ(),
		Kill:    kill,
	}))
}
