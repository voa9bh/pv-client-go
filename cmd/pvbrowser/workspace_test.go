package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"
)

func TestLateResultDoesNotLeaveMacros(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	ui := startBrowser(t, editorClient(func(ctx context.Context, command string) (string, error) {
		close(started)
		select {
		case <-release:
		case <-ctx.Done():
			return "", ctx.Err()
		}
		return "finished in background", nil
	}))
	ui.wait(t, func(s snapshot) bool { return s.metadataLoaded })
	ui.key(tcell.KeyTab, 0)
	ui.key(tcell.KeyRune, 'g')
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("request did not start")
	}
	ui.key(tcell.KeyF2, 0)
	ui.wait(t, func(s snapshot) bool { return s.macros })
	release <- struct{}{}
	ui.wait(t, func(s snapshot) bool {
		return s.macros && !s.sending && strings.Contains(s.response, "finished in background")
	})
	ui.key(tcell.KeyF2, 0)
	ui.wait(t, func(s snapshot) bool { return !s.macros && strings.Contains(s.response, "finished in background") })
}

func TestSeparatorCannotTargetPreviousMember(t *testing.T) {
	b := newBrowser(context.Background(), editorClient(func(context.Context, string) (string, error) {
		t.Error("separator executed a command")
		return "", nil
	}), "test:5002", new(logBuffer))
	defer b.cancel()
	b.ready = true
	b.inspectTarget = b.root
	b.info = metadata{properties: []member{{name: "value"}}, methods: []member{{name: "get"}}}
	b.renderInspector()
	b.app.SetFocus(b.table)
	b.selectMember(1)
	b.table.Select(2, 0)
	b.memberAction('g')
	if b.sending || !strings.Contains(b.status.GetText(false), "No member selected") {
		t.Fatal("separator used a previous target")
	}
}
