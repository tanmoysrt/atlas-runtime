package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: atlas-runtime <config.toml>")
		os.Exit(1)
	}

	configPath := os.Args[1]
	config, err := LoadConfig(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "load config: %v\n", err)
		os.Exit(1)
	}
	if err := config.Validate(); err != nil {
		fmt.Fprintf(os.Stderr, "validate config: %v\n", err)
		os.Exit(1)
	}

	runtime, err := NewRuntime(configPath, config)
	if err != nil {
		fmt.Fprintf(os.Stderr, "create runtime: %v\n", err)
		os.Exit(1)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	signalChannel := make(chan os.Signal, 1)
	signal.Notify(signalChannel, syscall.SIGTERM, syscall.SIGINT, syscall.SIGUSR1)
	go func() {
		for sig := range signalChannel {
			if sig == syscall.SIGUSR1 {
				runtime.Reload()
			} else {
				cancel()
				return
			}
		}
	}()

	if err := runtime.Run(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "run: %v\n", err)
		os.Exit(1)
	}
}
