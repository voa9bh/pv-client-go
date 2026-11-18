package main

import (
	"PVClient/pvclient"
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
)

func browserClientFactory(big bool) clientFactory {
	return func(address string) (ownedBrowserClient, error) {
		return pvclient.NewPVClient(address, pvclient.Options{BigData: big})
	}
}

func run(ctx context.Context) error {
	address, big, err := parseArgs(os.Args[1:], os.Stderr)
	if errors.Is(err, flag.ErrHelp) {
		return nil
	}
	if err != nil {
		return err
	}
	logs := new(logBuffer)
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(logs, nil)))
	defer slog.SetDefault(previous)
	factory := browserClientFactory(big)
	client, err := factory(address)
	if err != nil {
		return err
	}
	b := newBrowser(ctx, client, address, logs)
	b.newClient, b.ownedClient = factory, client
	return b.run(nil)
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
