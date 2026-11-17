package main

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

type fakeClient struct {
	ask       func(context.Context, string) (string, error)
	connect   func(context.Context) error
	reconnect func(context.Context) error
}

func (c *fakeClient) AskTxtContext(ctx context.Context, command string) (string, error) {
	return c.ask(ctx, command)
}

func (c *fakeClient) Connect(ctx context.Context) error {
	if c.connect != nil {
		return c.connect(ctx)
	}
	return nil
}

func (c *fakeClient) Reconnect(ctx context.Context) error {
	if c.reconnect != nil {
		return c.reconnect(ctx)
	}
	return nil
}

type exchange struct {
	command string
	reply   string
	err     error
}

func TestChildrenCommands(t *testing.T) {
	tests := []struct {
		name   string
		parent object
		script []exchange
		want   []object
	}{
		{
			name: "root", parent: rootObject(),
			script: []exchange{
				{command: "/pvServer.getNextChildObject(0)", reply: " -1\n"},
				{command: "#-1.name", reply: "procs"},
				{command: "/pvServer.getNextChildObject(-1)", reply: "-2"},
				{command: "#-2.name", reply: "with [red] markup/and space"},
				{command: "/pvServer.getNextChildObject(-2)", reply: "0"},
			},
			want: []object{
				{id: -1, name: "procs", path: "/pvServer/procs"},
				{id: -2, name: "with [red] markup/and space", path: "/pvServer/with [red] markup/and space"},
			},
		},
		{
			name: "nested", parent: object{id: -1, name: "procs", path: "/pvServer/procs"},
			script: []exchange{
				{command: "#-1.getNextChildObject(0)", reply: "12"},
				{command: "#12.name", reply: "  child  "},
				{command: "#-1.getNextChildObject(12)", reply: "0"},
			},
			want: []object{{id: 12, name: "  child  ", path: "/pvServer/procs/  child  "}},
		},
		{
			name: "empty", parent: rootObject(),
			script: []exchange{{command: "/pvServer.getNextChildObject(0)", reply: "0"}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			index := 0
			client := &fakeClient{ask: func(_ context.Context, command string) (string, error) {
				if index >= len(test.script) {
					t.Fatalf("unexpected extra request %q", command)
				}
				step := test.script[index]
				index++
				if command != step.command {
					t.Fatalf("command %q, want %q", command, step.command)
				}
				return step.reply, step.err
			}}
			got, err := children(context.Background(), client, test.parent, nil)
			if err != nil || !reflect.DeepEqual(got, test.want) || index != len(test.script) {
				t.Fatalf("children %+v, %v (%d requests); want %+v", got, err, index, test.want)
			}
		})
	}
}

func TestChildrenErrors(t *testing.T) {
	remote := errors.New("remote failure")
	for _, test := range []struct {
		name      string
		parent    object
		ancestors map[int64]bool
		script    []exchange
		errorText string
	}{
		{name: "malformed", script: []exchange{{reply: "not-an-ID"}}, errorText: "invalid object ID"},
		{name: "overflow", script: []exchange{{reply: "9223372036854775808"}}, errorText: "invalid object ID"},
		{name: "enumeration failure", script: []exchange{{err: remote}}, errorText: "remote failure"},
		{name: "name failure", script: []exchange{{reply: "-1"}, {err: remote}}, errorText: "name of #-1"},
		{name: "repeated", script: []exchange{{reply: "-1"}, {reply: "child"}, {reply: "-1"}}, errorText: "cyclic"},
		{name: "ancestor", ancestors: map[int64]bool{-1: true}, script: []exchange{{reply: "-1"}}, errorText: "cyclic"},
		{name: "self", parent: object{id: -1}, script: []exchange{{reply: "-1"}}, errorText: "cyclic"},
		{name: "partial failure", script: []exchange{{reply: "-1"}, {reply: "child"}, {err: remote}}, errorText: "remote failure"},
	} {
		t.Run(test.name, func(t *testing.T) {
			parent := test.parent
			if parent.id == 0 {
				parent = rootObject()
			}
			index := 0
			client := &fakeClient{ask: func(_ context.Context, command string) (string, error) {
				if index >= len(test.script) {
					t.Fatalf("unexpected request %q", command)
				}
				step := test.script[index]
				index++
				return step.reply, step.err
			}}
			list, err := children(context.Background(), client, parent, test.ancestors)
			if err == nil || !strings.Contains(err.Error(), test.errorText) || list != nil {
				t.Fatalf("expected atomic failure %q, got %+v, %v", test.errorText, list, err)
			}
		})
	}
}

func TestChildrenCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	client := &fakeClient{ask: func(context.Context, string) (string, error) {
		t.Fatal("queried after cancellation")
		return "", nil
	}}
	if _, err := children(ctx, client, rootObject(), nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
}
