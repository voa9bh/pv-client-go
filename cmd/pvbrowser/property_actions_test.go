package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

func TestPropertyEnterGetsAndSetIsExplicit(t *testing.T) {
	commands := make(chan string, 8)
	ui := startBrowser(t, editorClient(func(_ context.Context, command string) (string, error) {
		commands <- command
		return "got value", nil
	}))
	ui.wait(t, func(s snapshot) bool { return s.metadataLoaded })
	ui.key(tcell.KeyTab, 0)
	ui.key(tcell.KeyEnter, 0)
	ui.wait(t, func(s snapshot) bool { return !s.sending && strings.Contains(s.response, "got value") })
	if got := <-commands; got != "/pvServer.value" {
		t.Fatalf("Enter should get, got %q", got)
	}
	ui.key(tcell.KeyRune, 's')
	ui.wait(t, func(s snapshot) bool { return s.commandFocus && s.command == "" })
	select {
	case got := <-commands:
		t.Fatalf("opening Set sent %q", got)
	default:
	}
	for _, r := range "qQPRs" {
		ui.key(tcell.KeyRune, r)
	}
	ui.key(tcell.KeyEnter, 0)
	ui.wait(t, func(s snapshot) bool { return !s.sending && strings.Contains(s.response, "=qQPRs") })
	if got := <-commands; got != "/pvServer.value=qQPRs" {
		t.Fatalf("explicit set: %q", got)
	}
	ui.key(tcell.KeyDown, 0)
	ui.wait(t, func(s snapshot) bool { return strings.Contains(s.details, "MethodHandle") })
	ui.key(tcell.KeyRune, 's')
	ui.wait(t, func(s snapshot) bool { return strings.Contains(s.status, "s sets properties only") })
	select {
	case got := <-commands:
		t.Fatalf("s on method sent %q", got)
	default:
	}
}

func clickMemberCell(t *testing.T, ui *runningBrowser, row int) {
	t.Helper()
	time.Sleep(tview.DoubleClickInterval + 10*time.Millisecond)
	ui.b.app.QueueUpdateDraw(func() {
		x, y, _ := ui.b.table.GetCell(row, 0).GetLastPosition()
		ui.b.app.QueueEvent(tcell.NewEventMouse(x, y, tcell.Button1, tcell.ModNone))
		ui.b.app.QueueEvent(tcell.NewEventMouse(x, y, tcell.ButtonNone, tcell.ModNone))
	})
}

func TestPropertyMouseReadsOnceAndMethodOnlySelects(t *testing.T) {
	commands := make(chan string, 8)
	ui := startBrowser(t, editorClient(func(_ context.Context, command string) (string, error) {
		commands <- command
		return "clicked value", nil
	}))
	ui.wait(t, func(s snapshot) bool { return s.metadataLoaded })
	clickMemberCell(t, ui, 1)
	ui.wait(t, func(s snapshot) bool { return !s.sending && strings.Contains(s.response, "clicked value") })
	if got := <-commands; got != "/pvServer.value" {
		t.Fatalf("click: %q", got)
	}
	select {
	case extra := <-commands:
		t.Fatalf("one click sent two commands: %q", extra)
	default:
	}
	clickMemberCell(t, ui, 1)
	select {
	case got := <-commands:
		if got != "/pvServer.value" {
			t.Fatalf("repeated click: %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("clicking already-selected property did not read it")
	}
	ui.wait(t, func(s snapshot) bool { return !s.sending })
	clickMemberCell(t, ui, 3)
	ui.wait(t, func(s snapshot) bool { return s.memberFocus && strings.Contains(s.details, "MethodHandle") })
	select {
	case got := <-commands:
		t.Fatalf("method click executed %q", got)
	default:
	}
	ui.key(tcell.KeyEnter, 0)
	ui.wait(t, func(s snapshot) bool { return s.commandFocus })
	select {
	case got := <-commands:
		t.Fatalf("opening method parameters executed %q", got)
	default:
	}
	ui.key(tcell.KeyEscape, 0)
	ui.key(tcell.KeyUp, 0)
	ui.wait(t, func(s snapshot) bool { return s.memberFocus && strings.Contains(s.details, "PropertyHandle") })
	ui.b.app.QueueUpdateDraw(func() {
		ui.b.table.Select(3, 0)
		ui.b.table.Select(1, 0)
	})
	clickMemberCell(t, ui, 0)
	clickMemberCell(t, ui, 2)
	ui.wait(t, func(s snapshot) bool { return s.memberFocus })
	select {
	case got := <-commands:
		t.Fatalf("navigation/header/separator executed %q", got)
	default:
	}
}
