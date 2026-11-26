package main

import (
	"context"
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

func TestLetterShortcutModifiers(t *testing.T) {
	for _, modifiers := range []tcell.ModMask{
		tcell.ModNone, tcell.ModShift, tcell.ModCtrl, tcell.ModAlt,
		tcell.ModShift | tcell.ModCtrl, tcell.ModShift | tcell.ModAlt,
	} {
		for _, key := range "QPRGgsr" {
			t.Run(tcell.NewEventKey(tcell.KeyRune, key, modifiers).Name(), func(t *testing.T) {
				b := newBrowser(context.Background(), &fakeClient{
					ask: func(context.Context, string) (string, error) { return "0", nil },
				}, "localhost:5002", new(logBuffer))
				defer func() {
					b.cancel()
					b.workers.Wait()
				}()
				b.ready = true
				b.inspectTarget = b.root
				b.info = metadata{properties: []member{{handle: 1, name: "value"}}}
				b.renderInspector()
				b.table.Select(1, 0)
				b.app.SetFocus(b.table)
				if key == 'r' {
					b.app.SetFocus(b.tree)
				}
				b.newClient = func(string) (ownedBrowserClient, error) { return emptyOwnedClient(), nil }
				event := tcell.NewEventKey(tcell.KeyRune, key, modifiers)
				got := b.input(event)
				allowed := modifiers == tcell.ModNone || modifiers == tcell.ModShift
				if !allowed {
					if got != event || b.ctx.Err() != nil || b.connecting || b.macroDialog || b.sending || b.edit != nil || b.showGlobals {
						t.Fatal("Ctrl/Alt shortcut was accepted")
					}
					return
				}
				if got != nil {
					t.Fatal("letter shortcut was not handled")
				}
				switch key {
				case 'Q':
					if b.ctx.Err() == nil {
						t.Fatal("Q did not quit")
					}
				case 'P':
					if !b.macroDialog {
						t.Fatal("P did not open port prompt")
					}
				case 'R':
					if !b.connecting {
						t.Fatal("R did not reconnect")
					}
				case 'G':
					if !b.showGlobals {
						t.Fatal("G did not toggle globals")
					}
				case 'g':
					if !b.sending {
						t.Fatal("g did not get property")
					}
				case 's':
					if b.edit == nil || !b.command.HasFocus() {
						t.Fatal("s did not open property editor")
					}
				case 'r':
					if b.root.state != loading {
						t.Fatal("r did not refresh")
					}
				}
			})
		}
	}
}

func TestShiftLetterShortcutsPreserveEditingAndCase(t *testing.T) {
	b := newBrowser(context.Background(), &fakeClient{}, "localhost:5002", new(logBuffer))
	defer b.cancel()
	for _, editor := range []tview.Primitive{b.command, b.macroEditor} {
		b.app.SetFocus(editor)
		for _, key := range "QPRGgs" {
			event := tcell.NewEventKey(tcell.KeyRune, key, tcell.ModShift)
			if b.input(event) != event {
				t.Fatalf("intercepted %q in editor", key)
			}
		}
	}
	b.app.SetFocus(b.details)
	for _, key := range "qpgs" {
		event := tcell.NewEventKey(tcell.KeyRune, key, tcell.ModShift)
		if b.input(event) != event || b.ctx.Err() != nil {
			t.Fatalf("changed case or focus scoping for %q", key)
		}
	}
	b.promptMacroFile(false)
	for _, key := range "QPRGgs" {
		event := tcell.NewEventKey(tcell.KeyRune, key, tcell.ModShift)
		if b.input(event) != event {
			t.Fatalf("intercepted %q in dialog", key)
		}
	}
}

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
