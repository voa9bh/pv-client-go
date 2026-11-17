package main

import (
	"PVClient/pvclient"
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
)

func run(ctx context.Context) error {
	address := flag.String("addr", "", "PVServer host:port")
	big := flag.Bool("big", false, "negotiate and use BigData packets")
	flag.Parse()
	if *address == "" || flag.NArg() != 0 {
		return fmt.Errorf("usage: go run ./cmd/pvbrowser -addr host:port [-big]")
	}
	logs := new(logBuffer)
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(logs, nil)))
	defer slog.SetDefault(previous)
	client, err := pvclient.NewPVClient(*address, pvclient.Options{BigData: *big})
	if err != nil {
		return err
	}
	defer client.Close()
	return newBrowser(ctx, client, *address, logs).run(nil)
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
