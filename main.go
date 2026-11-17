package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
)

func run(ctx context.Context) error {
	address := flag.String("addr", "", "PVServer host:port")
	big := flag.Bool("big", false, "negotiate and use BigData packets")
	resend := flag.Bool("resend", false, "resend interrupted commands (may execute twice)")
	flag.Parse()
	if *address == "" || flag.NArg() == 0 {
		return errors.New("usage: go run . -addr host:port [-big] [-resend] 'command' ['command' ...]")
	}
	client, err := NewPVClient(*address, Options{BigData: *big, ResendAfterReconnect: *resend})
	if err != nil {
		return err
	}
	defer client.Close()
	if err := client.Connect(ctx); err != nil {
		return fmt.Errorf("connect: %w", err)
	}

	reply, err := client.AskTxtContext(ctx, flag.Arg(0))
	if err != nil {
		return err
	}
	fmt.Println(reply)

	var futures []*Future
	for _, command := range flag.Args()[1:] {
		future, err := client.AskTxtAsync(ctx, command)
		if err != nil {
			return err
		}
		futures = append(futures, future)
	}
	for _, future := range futures {
		reply, err := future.Wait()
		if err != nil {
			return fmt.Errorf("futureID %d: %w", future.FutureID(), err)
		}
		fmt.Printf("futureID %d: %s\n", future.FutureID(), reply)
	}
	return nil
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := run(ctx); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
