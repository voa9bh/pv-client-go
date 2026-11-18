package main

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

func TestQuitFromNonEditingViews(t *testing.T) {
	for _, name := range []string{"tree", "members", "response", "command display", "details", "macro output", "button"} {
		t.Run(name, func(t *testing.T) {
			b := macroBrowser(t, nil)
			views := map[string]tview.Primitive{
				"tree": b.tree, "members": b.table, "response": b.response,
				"command display": b.commandDisplay, "details": b.details,
				"macro output": b.macroOutput, "button": b.bar.buttons[0],
			}
			b.app.SetFocus(views[name])
			lower := tcell.NewEventKey(tcell.KeyRune, 'q', tcell.ModNone)
			if b.input(lower) != lower || b.ctx.Err() != nil {
				t.Fatal("lowercase q must not quit")
			}
			if b.input(tcell.NewEventKey(tcell.KeyRune, 'Q', tcell.ModNone)) != nil || b.ctx.Err() == nil {
				t.Fatal("Q did not quit")
			}
		})
	}
}

func TestSharedLettersRemainTextInEditorsAndDialogs(t *testing.T) {
	b := macroBrowser(t, nil)
	for _, editor := range []tview.Primitive{b.command, b.macroEditor} {
		b.app.SetFocus(editor)
		for _, r := range "qQPRs" {
			event := tcell.NewEventKey(tcell.KeyRune, r, tcell.ModNone)
			if b.input(event) != event || b.ctx.Err() != nil || b.connecting || b.macroDialog {
				t.Fatalf("intercepted %q while editing", r)
			}
		}
	}
	b.app.SetFocus(b.details)
	b.promptMacroFile(false)
	for _, r := range "qQPRs" {
		event := tcell.NewEventKey(tcell.KeyRune, r, tcell.ModNone)
		if b.input(event) != event || b.ctx.Err() != nil || b.connecting {
			t.Fatalf("intercepted %q in a dialog", r)
		}
	}
	b.input(tcell.NewEventKey(tcell.KeyCtrlC, 0, tcell.ModNone))
	if b.ctx.Err() == nil {
		t.Fatal("Ctrl+C did not quit from a dialog")
	}
}

func TestActionButtonsUseSelectedMember(t *testing.T) {
	commands := make(chan string, 2)
	b := macroBrowser(t, func(_ context.Context, command string) (string, error) {
		commands <- command
		return "value", nil
	})
	b.inspectTarget = b.root
	b.info = metadata{properties: []member{{handle: 1, name: "value"}}}
	b.renderInspector()
	b.table.Select(1, 0)
	b.selectMember(1)
	b.app.SetFocus(b.bar.buttons[1])
	dialogKey(b, tcell.KeyEnter)
	b.workers.Wait()
	b.drain()
	if got := <-commands; got != "/pvServer.value" {
		t.Fatalf("Get button: %q", got)
	}
	b.app.SetFocus(b.bar.buttons[2])
	dialogKey(b, tcell.KeyEnter)
	if !b.command.HasFocus() || b.edit == nil {
		t.Fatal("Edit button did not open the selected property")
	}
	b.command.SetText("qPR")
	dialogKey(b, tcell.KeyEnter)
	b.workers.Wait()
	b.drain()
	if got := <-commands; got != "/pvServer.value=qPR" {
		t.Fatalf("edit command: %q", got)
	}
	b.app.SetFocus(b.bar.buttons[3])
	dialogKey(b, tcell.KeyEnter)
	if b.command.HasFocus() || !strings.Contains(b.status.GetText(false), "Call requires a method") {
		t.Fatal("Call button accepted a property")
	}
	b.app.SetFocus(b.bar.buttons[4])
	dialogKey(b, tcell.KeyEnter)
	if !b.showGlobals {
		t.Fatal("Globals button did not toggle")
	}
}

func clickButton(t *testing.T, ui *runningBrowser, label string) {
	t.Helper()
	// Separate clicks from earlier buttons so tview does not classify them as
	// a double-click (which Buttons intentionally do not activate).
	time.Sleep(tview.DoubleClickInterval + 10*time.Millisecond)
	done := make(chan bool, 1)
	ui.b.app.QueueUpdateDraw(func() {
		for _, button := range ui.b.bar.buttons {
			if button.GetLabel() != label {
				continue
			}
			x, y, width, _ := button.GetRect()
			x += width / 2
			down := tcell.NewEventMouse(x, y, tcell.Button1, tcell.ModNone)
			up := tcell.NewEventMouse(x, y, tcell.ButtonNone, tcell.ModNone)
			ui.b.app.QueueEvent(down)
			ui.b.app.QueueEvent(up)
			done <- true
			return
		}
		done <- false
	})
	if !<-done {
		t.Fatalf("button %q not found", label)
	}
}

func TestActionBarFocusClicksAndResize(t *testing.T) {
	for _, size := range [][2]int{{80, 24}, {140, 40}} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			ui := startBrowserSize(t, editorClient(func(context.Context, string) (string, error) { return "", nil }), size[0], size[1])
			ui.wait(t, func(s snapshot) bool { return s.metadataLoaded })
			ui.b.app.QueueUpdateDraw(func() {
				ui.b.app.SetFocus(ui.b.details)
			})
			for i := 0; i < 9; i++ {
				ui.key(tcell.KeyTab, 0)
				want := i
				ui.wait(t, func(s snapshot) bool { return s.actionFocus == want })
			}
			ui.key(tcell.KeyTab, 0)
			ui.wait(t, func(s snapshot) bool { return s.treeFocus })
			ui.key(tcell.KeyBacktab, 0)
			ui.wait(t, func(s snapshot) bool { return s.actionFocus == 8 })
			clickButton(t, ui, "F2 Switch")
			ui.wait(t, func(s snapshot) bool { return s.macros && s.macroEditorFocus })
			clickButton(t, ui, "^O Load")
			ui.wait(t, func(s snapshot) bool { return s.macroDialog && strings.Contains(s.rendered, "Load macro") })
			ui.key(tcell.KeyEscape, 0)
			ui.wait(t, func(s snapshot) bool { return !s.macroDialog })
			ui.b.app.QueueUpdateDraw(func() {
				ui.screen.SetSize(60, 24)
			})
			ui.b.app.QueueEvent(tcell.NewEventResize(60, 24))
			ui.wait(t, func(s snapshot) bool {
				return strings.Contains(s.rendered, "Q Quit") && strings.Contains(s.rendered, "F5 Run")
			})
			clickButton(t, ui, "F2 Switch")
			ui.wait(t, func(s snapshot) bool { return !s.macros })
			clickButton(t, ui, "G Globals")
			ui.wait(t, func(s snapshot) bool { return strings.Contains(s.rendered, "including globals") })
		})
	}
}

func TestActionBarMouseQuit(t *testing.T) {
	ui := startBrowser(t, editorClient(func(context.Context, string) (string, error) { return "", nil }))
	ui.wait(t, func(s snapshot) bool { return s.metadataLoaded })
	clickButton(t, ui, "Q Quit")
	select {
	case <-ui.b.ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("click did not quit")
	}
}
