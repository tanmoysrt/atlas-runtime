package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	var enableDashboard bool
	flag.BoolVar(&enableDashboard, "enable-dashboard", false, "enable web dashboard at /")
	flag.Parse()

	if flag.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "usage: atlas-runtime [options] <config.toml>")
		flag.PrintDefaults()
		os.Exit(1)
	}

	configPath := flag.Arg(0)
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

	api := NewAPI(runtime, enableDashboard)

	if err := runtime.Run(ctx, api); err != nil {
		fmt.Fprintf(os.Stderr, "run: %v\n", err)
		os.Exit(1)
	}
}
