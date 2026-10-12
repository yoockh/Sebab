// Command sebab is the Sebab engine and CLI.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/yoockh/sebab/internal/cli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := cli.Run(ctx, os.Args[1:], cli.OSEnv())
	stop()
	os.Exit(code)
}
