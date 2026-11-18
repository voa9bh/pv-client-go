package main

import (
	"errors"
	"flag"
	"io"
	"testing"
)

func TestParseArgs(t *testing.T) {
	for _, test := range []struct {
		args    []string
		address string
		big     bool
	}{
		{[]string{"localhost", "5002"}, "localhost:5002", false},
		{[]string{"127.0.0.1", "1"}, "127.0.0.1:1", false},
		{[]string{"-big", "::1", "65535"}, "[::1]:65535", true},
	} {
		address, big, err := parseArgs(test.args, io.Discard)
		if err != nil || address != test.address || big != test.big {
			t.Fatalf("%v: %s %t %v", test.args, address, big, err)
		}
	}
	for _, args := range [][]string{
		nil, {"localhost"}, {"localhost", "5002", "extra"}, {"", "5002"}, {" host", "5002"},
		{"localhost", ""}, {"localhost", "0"}, {"localhost", "-1"}, {"localhost", "+5002"},
		{"localhost", "65536"}, {"localhost", "port"}, {"localhost", "999999999999999999999999"},
		{"-addr", "localhost:5002"}, {"localhost", "5002", "-big"},
	} {
		if _, _, err := parseArgs(args, io.Discard); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
	if _, _, err := parseArgs([]string{"-h"}, io.Discard); !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("help: %v", err)
	}
}
