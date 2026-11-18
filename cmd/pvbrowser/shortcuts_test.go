package main

import (
	"context"
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"
)

func TestMemberShortcutScoping(t *testing.T) {
	b := newBrowser(context.Background(), &fakeClient{}, "test:5002", new(logBuffer))
	defer b.cancel()
	b.ready = true
	b.inspectTarget = b.root
	b.info = metadata{properties: []member{{handle: 1, name: "value"}}, methods: []member{{handle: 2, name: "call"}}}
	b.renderInspector()
	b.app.SetFocus(b.table)
	b.table.Select(1, 0)
	b.selectMember(1)
	b.table.Select(3, 0)
	b.selectMember(3)
	b.input(tcell.NewEventKey(tcell.KeyRune, 'g', tcell.ModNone))
	if b.edit != nil || b.sending || !strings.Contains(b.status.GetText(false), "g reads properties only") {
		t.Fatal("g accepted a method")
	}
	for _, event := range []*tcell.EventKey{
		tcell.NewEventKey(tcell.KeyF5, 0, tcell.ModNone),
		tcell.NewEventKey(tcell.KeyF6, 0, tcell.ModNone),
		tcell.NewEventKey(tcell.KeyRune, 'c', tcell.ModAlt),
		tcell.NewEventKey(tcell.KeyRune, 'c', tcell.ModCtrl),
		tcell.NewEventKey(tcell.KeyRune, 'C', tcell.ModNone),
		tcell.NewEventKey(tcell.KeyRune, 'c', tcell.ModNone),
	} {
		if got := b.input(event); got != event || b.edit != nil || b.sending {
			t.Fatalf("unexpected member action for %v", event)
		}
	}
	b.app.SetFocus(b.response)
	for _, key := range "gGsc" {
		event := tcell.NewEventKey(tcell.KeyRune, key, tcell.ModNone)
		if b.input(event) != event || b.edit != nil || b.sending {
			t.Fatal("member shortcut intercepted outside the list")
		}
	}
	b.app.SetFocus(b.command)
	for _, key := range "gGsc" {
		event := tcell.NewEventKey(tcell.KeyRune, key, tcell.ModNone)
		if b.input(event) != event {
			t.Fatal("input shortcut consumed typed text")
		}
	}
}
