package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	code := RunCLI(ctx, os.Args[1:], os.Stdout, os.Stderr, os.LookupEnv)
	os.Exit(code)
}
