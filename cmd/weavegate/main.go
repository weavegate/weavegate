// Command weavegate reaches a verdict on a configured concurrent workflow
// and saves the run evidence to disk.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(ExecuteContext(ctx, os.Args[1:], os.Stdout, os.Stderr))
}
