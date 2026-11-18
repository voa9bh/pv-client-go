package main

import (
	"flag"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
)

func parseArgs(args []string, output io.Writer) (string, bool, error) {
	flags := flag.NewFlagSet("pvbrowser", flag.ContinueOnError)
	flags.SetOutput(output)
	big := flags.Bool("big", false, "negotiate and use BigData packets")
	flags.Usage = func() { fmt.Fprintln(output, "usage: pvbrowser [-big] host port") }
	if err := flags.Parse(args); err != nil {
		return "", false, err
	}
	if flags.NArg() != 2 {
		return "", false, fmt.Errorf("usage: pvbrowser [-big] host port")
	}
	host, port := flags.Arg(0), flags.Arg(1)
	if strings.TrimSpace(host) == "" || strings.TrimSpace(host) != host {
		return "", false, fmt.Errorf("host must not be empty or padded with whitespace")
	}
	for _, digit := range port {
		if digit < '0' || digit > '9' {
			return "", false, fmt.Errorf("port must be a decimal number in 1..65535")
		}
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return "", false, fmt.Errorf("port must be a decimal number in 1..65535")
	}
	return net.JoinHostPort(host, strconv.Itoa(n)), *big, nil
}
