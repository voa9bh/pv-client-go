package main

import (
	"context"
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"
)

func TestGlobalFilterSelectionAndPersistence(t *testing.T) {
	b := newBrowser(context.Background(), &fakeClient{}, "test:5002", new(logBuffer))
	defer b.cancel()
	b.inspectTarget = b.root
	b.info = metadata{properties: []member{{handle: -1, name: "global"}, {handle: 1, name: "local"}, {handle: 0, name: "zero"}},
		methods: []member{{handle: 2, name: "localMethod"}, {handle: -2, name: "globalMethod"}}}
	b.renderInspector()
	b.app.SetFocus(b.table)
	b.table.Select(1, 0)
	b.selectMember(1)
	if b.selection.name != "local" || b.table.GetCell(3, 0).Text != "localMethod()" {
		t.Fatal("default visibility incorrect")
	}
	b.toggleGlobals()
	if b.selection.name != "local" {
		t.Fatal("visible selection lost")
	}
	if b.table.GetCell(1, 0).Text != "global" || b.table.GetCell(2, 0).Text != "local" ||
		b.table.GetCell(4, 0).Text != "localMethod()" || b.table.GetCell(5, 0).Text != "globalMethod()" {
		t.Fatal("server order changed")
	}
	for i := 0; i < b.table.GetRowCount(); i++ {
		if strings.Contains(b.table.GetCell(i, 0).Text, "zero") {
			t.Fatal("zero handle visible")
		}
	}
	b.table.Select(1, 0)
	b.selectMember(1)
	sequence := b.commandSequence
	b.toggleGlobals()
	if b.selection == nil || b.selection.name != "local" || b.commandSequence <= sequence {
		t.Fatal("hidden target not invalidated")
	}
	b.apply(result{kind: commandResult, generation: b.generation, sequence: sequence, text: "stale"})
	if strings.Contains(b.response.GetText(false), "stale") {
		t.Fatal("hidden target accepted late reply")
	}
	b.toggleGlobals()
	b.reset()
	if !b.showGlobals {
		t.Fatal("filter preference lost on reset")
	}
}

func TestGlobalOnlyAndErrors(t *testing.T) {
	b := newBrowser(context.Background(), &fakeClient{}, "test:5002", new(logBuffer))
	defer b.cancel()
	b.inspectTarget = b.root
	b.info = metadata{properties: []member{{handle: -1, name: "global"}}}
	b.renderInspector()
	if !strings.Contains(b.table.GetCell(1, 0).Text, "no visible properties") {
		t.Fatal("missing filtered-empty state")
	}
	b.app.SetFocus(b.table)
	b.toggleGlobals()
	b.toggleGlobals()
	if b.selection != nil {
		t.Fatal("hidden global still selected")
	}
	b.memberAction('g')
	if b.sending {
		t.Fatal("hidden global sent request")
	}
}

func TestTwoLineResultAndHorizontalScroll(t *testing.T) {
	ui := startBrowser(t, editorClient(func(context.Context, string) (string, error) {
		return strings.Repeat("x", 150) + "END\nnext\r\ttail", nil
	}))
	ui.wait(t, func(s snapshot) bool { return s.metadataLoaded })
	ui.key(tcell.KeyTab, 0)
	ui.key(tcell.KeyRune, 'g')
	ui.wait(t, func(s snapshot) bool { return !s.sending && strings.Contains(s.response, `END\nnext\r\ttail`) })
	ui.key(tcell.KeyTab, 0) // Result.
	for i := 0; i < 160; i++ {
		ui.key(tcell.KeyRight, 0)
	}
	ui.wait(t, func(s snapshot) bool { return strings.Contains(s.rendered, `END\nnext`) })
}
