package main

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"
)

func macroBrowser(t *testing.T, ask func(context.Context, string) (string, error)) *browser {
	t.Helper()
	b := newBrowser(context.Background(), &fakeClient{ask: ask}, "test:5002", new(logBuffer))
	b.ready = true
	t.Cleanup(func() {
		b.cancel()
		b.workers.Wait()
	})
	return b
}

func finishMacro(t *testing.T, b *browser) {
	t.Helper()
	for b.macroRunning {
		select {
		case r := <-b.results:
			b.apply(r)
		case <-time.After(2 * time.Second):
			t.Fatal("macro did not finish")
		}
	}
}

func TestMacroSequentialSnapshotAndResponses(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	commands := make(chan string, 8)
	var active atomic.Int32
	b := macroBrowser(t, func(ctx context.Context, command string) (string, error) {
		if active.Add(1) != 1 {
			t.Error("overlapping macro calls")
		}
		defer active.Add(-1)
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 30*time.Second || time.Until(deadline) < 29*time.Second {
			t.Error("missing 30-second per-call timeout")
		}
		commands <- command
		switch command {
		case " first() ":
			close(started)
			select {
			case <-release:
			case <-ctx.Done():
				return "", ctx.Err()
			}
			return "", nil
		case "fail()":
			return "", errors.New("test failure")
		default:
			return "line 1\n[red]line 2\x1b", nil
		}
	})
	b.macroEditor.SetText(" first() \r\n\n \t\r\nfail()\r\nlast()\n", false)
	b.runMacro()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("macro did not start")
	}
	b.runMacro()
	b.macroEditor.SetText("not part of this run", false)
	if len(commands) != 1 {
		t.Fatal("later commands submitted before the first returned")
	}
	close(release)
	finishMacro(t, b)
	var got []string
	for len(commands) > 0 {
		got = append(got, <-commands)
	}
	if want := []string{" first() ", "fail()", "last()"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("commands: %q, want %q", got, want)
	}
	output := b.macroOutput.GetText(false)
	for _, want := range []string{
		"Line 1 >  first() \n<ready>", "Line 4 > fail()\nERROR: test failure",
		"Line 5 > last()\nline 1\n[red]line 2\uFFFD", "Macro completed: 3 commands, 1 errors",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("missing %q in %q", want, output)
		}
	}
	previous := output
	b.apply(result{kind: macroResult, sequence: b.macroSequence - 1, macro: macroEvent{text: "stale"}})
	if b.macroOutput.GetText(false) != previous {
		t.Fatal("stale macro result applied")
	}
	b.macroEditor.SetText("last()", false)
	b.runMacro()
	finishMacro(t, b)
	if strings.Contains(b.macroOutput.GetText(false), "test failure") {
		t.Fatal("new run retained previous output")
	}
}

func TestMacroReadinessAndEmptyEditor(t *testing.T) {
	b := macroBrowser(t, func(context.Context, string) (string, error) {
		t.Error("unexpected request")
		return "", nil
	})
	b.ready = false
	b.macroEditor.SetText("command", false)
	b.runMacro()
	if b.macroRunning || !strings.Contains(b.status.GetText(false), "Not ready") {
		t.Fatal("macro ran before connection was ready")
	}
	b.ready = true
	b.macroEditor.SetText(" \r\n\t\n", false)
	b.runMacro()
	if b.macroRunning || !strings.Contains(b.status.GetText(false), "no commands") {
		t.Fatal("empty editor started a run")
	}
}

func TestMacroCancellation(t *testing.T) {
	for _, action := range []string{"stop", "reconnect", "quit"} {
		t.Run(action, func(t *testing.T) {
			started := make(chan struct{})
			var calls atomic.Int32
			b := macroBrowser(t, func(ctx context.Context, command string) (string, error) {
				calls.Add(1)
				close(started)
				<-ctx.Done()
				return "", ctx.Err()
			})
			b.client.(*fakeClient).reconnect = func(context.Context) error { return errors.New("offline") }
			b.macroEditor.SetText("first\nnever sent", false)
			b.runMacro()
			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatal("macro did not start")
			}
			switch action {
			case "stop":
				b.stopMacro("Stop requested")
				b.runMacro() // Must not overlap the still-returning worker.
			case "reconnect":
				b.connect(true)
			case "quit":
				b.input(tcell.NewEventKey(tcell.KeyCtrlC, 0, tcell.ModNone))
			}
			if action == "quit" {
				b.workers.Wait()
			} else {
				finishMacro(t, b)
				if b.macroRunning || !strings.Contains(b.macroOutput.GetText(false), "Macro stopped") {
					t.Fatal("macro state did not settle after cancellation")
				}
			}
			if calls.Load() != 1 {
				t.Fatal("remaining command sent or command replayed")
			}
			if !strings.Contains(b.macroOutput.GetText(false), "may already have executed") {
				t.Fatal("missing cancellation warning")
			}
		})
	}
}

func TestMacroWorkspaceEditingRunAndSwitch(t *testing.T) {
	for _, size := range [][2]int{{80, 24}, {140, 40}} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			started, release := make(chan struct{}), make(chan struct{})
			ui := startBrowserSize(t, editorClient(func(ctx context.Context, command string) (string, error) {
				if command != "qgrR()" {
					t.Errorf("typed command changed: %q", command)
				}
				close(started)
				select {
				case <-release:
				case <-ctx.Done():
					return "", ctx.Err()
				}
				return "", nil
			}), size[0], size[1])
			ui.wait(t, func(s snapshot) bool { return s.metadataLoaded })
			ui.key(tcell.KeyF2, 0)
			for _, r := range "qgrR()" {
				ui.key(tcell.KeyRune, r)
			}
			ui.key(tcell.KeyEnter, 0)
			s := ui.wait(t, func(s snapshot) bool { return s.macroText == "qgrR()\n" && s.macroEditorFocus })
			for _, label := range []string{"Macro editor", "Macro output", "F5 Run", "^O Load", "^S Save", "P Port", "Q Quit"} {
				if !strings.Contains(s.rendered, label) {
					t.Fatalf("missing %q:\n%s", label, s.rendered)
				}
			}
			ui.key(tcell.KeyF5, 0)
			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatal("run shortcut did not start macro")
			}
			ui.key(tcell.KeyF2, 0)
			ui.wait(t, func(s snapshot) bool { return !s.macros && s.macroRunning })
			close(release)
			ui.wait(t, func(s snapshot) bool {
				return !s.macros && !s.macroRunning && strings.Contains(s.macroOutput, "<ready>")
			})
			ui.key(tcell.KeyF2, 0)
			ui.wait(t, func(s snapshot) bool {
				return s.macros && s.macroEditorFocus && s.macroText == "qgrR()\n" && strings.Contains(s.details, "One nonblank")
			})
			ui.key(tcell.KeyTab, 0)
			ui.wait(t, func(s snapshot) bool { return s.macroOutputFocus })
			ui.key(tcell.KeyRune, 'x')
			ui.key(tcell.KeyBacktab, 0)
			ui.wait(t, func(s snapshot) bool {
				return s.macroEditorFocus && !strings.Contains(s.macroOutput, "x") && s.macroText == "qgrR()\n"
			})
		})
	}
}

func TestMacroPanesScroll(t *testing.T) {
	ui := startBrowser(t, editorClient(func(context.Context, string) (string, error) { return "", nil }))
	ui.wait(t, func(s snapshot) bool { return s.metadataLoaded })
	ui.key(tcell.KeyF2, 0)
	ui.wait(t, func(s snapshot) bool { return s.macroEditorFocus })
	ui.b.app.QueueUpdateDraw(func() {
		ui.b.macroEditor.SetText(strings.Repeat("editor line\n", 80), true)
		ui.b.macroOutput.SetText(strings.Repeat("output line\n", 80)).ScrollToBeginning()
	})
	s := ui.wait(t, func(s snapshot) bool { return s.macroEditorRow > 0 })
	ui.key(tcell.KeyPgUp, 0)
	ui.wait(t, func(next snapshot) bool { return next.macroEditorRow < s.macroEditorRow })
	ui.key(tcell.KeyTab, 0)
	ui.key(tcell.KeyPgDn, 0)
	ui.wait(t, func(s snapshot) bool { return s.macroOutputFocus && s.macroOutputRow > 0 })
}

func TestMacroRunStopButtonsAndShortcut(t *testing.T) {
	for _, stopWithKey := range []bool{false, true} {
		t.Run(fmt.Sprint(stopWithKey), func(t *testing.T) {
			started := make(chan struct{})
			var calls atomic.Int32
			ui := startBrowser(t, editorClient(func(ctx context.Context, command string) (string, error) {
				calls.Add(1)
				close(started)
				<-ctx.Done()
				return "", ctx.Err()
			}))
			ui.wait(t, func(s snapshot) bool { return s.metadataLoaded })
			ui.key(tcell.KeyF2, 0)
			ui.wait(t, func(s snapshot) bool { return s.macroEditorFocus })
			ui.b.app.QueueUpdateDraw(func() {
				ui.b.macroEditor.SetText("first\nnever sent", false)
			})
			ui.key(tcell.KeyTab, 0) // Output.
			ui.key(tcell.KeyTab, 0) // Run once.
			ui.key(tcell.KeyEnter, 0)
			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatal("Run once button did not execute")
			}
			if stopWithKey {
				ui.key(tcell.KeyF6, 0)
			} else {
				ui.key(tcell.KeyTab, 0) // Stop.
				ui.key(tcell.KeyEnter, 0)
			}
			ui.wait(t, func(s snapshot) bool {
				return !s.macroRunning && strings.Contains(s.macroOutput, "Macro stopped")
			})
			if calls.Load() != 1 {
				t.Fatal("stop allowed remaining macro lines to run")
			}
		})
	}
}
