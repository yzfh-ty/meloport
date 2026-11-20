package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"meloport/internal/app"
)

var version = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := app.Run(ctx, os.Args[1:], os.Stdout, os.Stderr, version); err != nil {
		fmt.Fprintln(os.Stderr, "meloport:", err)
		os.Exit(1)
	}
}
