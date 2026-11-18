package main

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"
)

func editorClient(ask func(context.Context, string) (string, error)) *fakeClient {
	return &fakeClient{
		ask: func(ctx context.Context, command string) (string, error) {
			if strings.Contains(command, ".getNextChildObject(") {
				return "0", nil
			}
			return ask(ctx, command)
		},
		metadata: func(ctx context.Context, command string) (string, error) {
			switch {
			case strings.HasSuffix(command, ".classHandle"):
				return "181", nil
			case strings.Contains(command, ".getPropertyInfos("):
				return oneProperty, nil
			default:
				return oneMethod, nil
			}
		},
	}
}

func TestInspectorEditorExplicitSend(t *testing.T) {
	var sends atomic.Int32
	commands := make(chan string, 8)
	client := editorClient(func(ctx context.Context, command string) (string, error) {
		sends.Add(1)
		commands <- command
		return "line 1\n[red]line 2\x1b", nil
	})
	ui := startBrowser(t, client)
	ui.wait(t, func(s snapshot) bool { return s.metadataLoaded && s.class == 181 })
	ui.key(tcell.KeyTab, 0) // table
	ui.key(tcell.KeyDown, 0)
	ui.wait(t, func(s snapshot) bool { return s.command == "/pvServer.value" })
	if sends.Load() != 0 {
		t.Fatal("member selection executed a command")
	}
	ui.key(tcell.KeyEnter, 0)
	ui.wait(t, func(s snapshot) bool { return s.commandFocus })
	for _, r := range "=qRr" {
		ui.key(tcell.KeyRune, r)
	}
	ui.wait(t, func(s snapshot) bool { return s.command == "/pvServer.value=qRr" })
	ui.key(tcell.KeyEnter, 0) // move to Send, without submitting
	ui.wait(t, func(s snapshot) bool { return s.sendFocus })
	if sends.Load() != 0 {
		t.Fatal("Enter in editor sent a command")
	}
	ui.key(tcell.KeyEnter, 0)
	s := ui.wait(t, func(s snapshot) bool { return strings.Contains(s.response, "Result:") })
	if !strings.Contains(s.response, "line 1\n[red]line 2�") {
		t.Fatalf("response formatting: %q", s.response)
	}
	if command := <-commands; command != "/pvServer.value=qRr" {
		t.Fatalf("sent %q", command)
	}
	ui.key(tcell.KeyF2, 0)
	ui.key(tcell.KeyBacktab, 0) // command
	ui.key(tcell.KeyBacktab, 0) // methods table
	ui.key(tcell.KeyDown, 0)
	ui.wait(t, func(s snapshot) bool { return s.command == "/pvServer.set()" && s.methods })
	if sends.Load() != 1 {
		t.Fatal("method selection executed it")
	}
	ui.key(tcell.KeyEnter, 0)
	ui.key(tcell.KeyEnter, 0)
	ui.key(tcell.KeyEnter, 0)
	ui.wait(t, func(s snapshot) bool { return strings.Contains(s.response, "Command: /pvServer.set()\nResult:") })
	if command := <-commands; command != "/pvServer.set()" {
		t.Fatalf("sent %q", command)
	}
}

func TestInspectorSendFailureEmptyAndDuplicate(t *testing.T) {
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	defer close(release)
	var count atomic.Int32
	client := editorClient(func(ctx context.Context, command string) (string, error) {
		n := count.Add(1)
		started <- struct{}{}
		select {
		case <-release:
		case <-ctx.Done():
			return "", ctx.Err()
		}
		if n == 1 {
			return "", errors.New("remote failure")
		}
		return "", nil
	})
	// Direct widget-state execution keeps control of the pending result.
	b := newBrowser(context.Background(), client, "test:5002", new(logBuffer))
	defer b.cancel()
	b.ready = true
	b.command.SetText("#-1.value")
	b.submitCommand()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("command not started")
	}
	b.submitCommand()
	if count.Load() != 1 {
		t.Fatal("duplicate send accepted")
	}
	release <- struct{}{}
	r := <-b.results
	b.apply(r)
	if !strings.Contains(b.response.GetText(false), "remote failure") || b.sending {
		t.Fatal("command error not shown")
	}
	b.submitCommand()
	<-started
	release <- struct{}{}
	b.apply(<-b.results)
	if !strings.Contains(b.response.GetText(false), "(empty response)") {
		t.Fatal("empty result not shown")
	}
	b.workers.Wait()
}

func TestInspectorStaleMetadataAndCommand(t *testing.T) {
	b := newBrowser(context.Background(), &fakeClient{}, "test:5002", new(logBuffer))
	defer b.cancel()
	b.inspectSequence = 5
	b.apply(result{kind: metadataResult, generation: b.generation, sequence: 4, info: metadata{class: 999}})
	if b.info.class == 999 {
		t.Fatal("old selection metadata applied")
	}
	b.apply(result{kind: metadataResult, generation: b.generation, sequence: 5, info: metadata{class: 181}})
	if b.info.class != 181 {
		t.Fatal("current metadata dropped")
	}
	b.commandSequence = 7
	b.clearInspector()
	b.apply(result{kind: commandResult, generation: b.generation, sequence: 7, text: "stale"})
	if strings.Contains(b.response.GetText(false), "stale") {
		t.Fatal("canceled command overwrote new state")
	}
}

func TestInspectorSelectionCancellation(t *testing.T) {
	started := make(chan struct{}, 1)
	canceled := make(chan struct{}, 1)
	client := editorClient(nil)
	client.metadata = func(ctx context.Context, command string) (string, error) {
		if strings.HasPrefix(command, "#-1.") {
			started <- struct{}{}
			<-ctx.Done()
			canceled <- struct{}{}
			return "", ctx.Err()
		}

		if strings.HasSuffix(command, ".classHandle") {
			return "182", nil
		}
		return "0;0;0;0;0;0", nil
	}
	b := newBrowser(context.Background(), client, "test:5002", new(logBuffer))
	defer b.cancel()
	b.ready = true
	first := b.makeBranch(object{id: -1}, b.root)
	second := b.makeBranch(object{id: -2}, b.root)
	b.inspect(first, false)
	<-started
	b.command.SetText("#-1.value=old")
	b.inspect(second, false)
	if b.command.GetText() != "" {
		t.Fatal("old command retained on selection change")
	}
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("old metadata query not canceled")
	}
	b.workers.Wait()
	b.drain()
	if b.info.class != 182 {
		t.Fatalf("stale metadata applied: %d", b.info.class)
	}
}

func TestInspectorReconnectCancelsCommand(t *testing.T) {
	started := make(chan struct{})
	canceled := make(chan struct{})
	var calls atomic.Int32
	client := editorClient(func(ctx context.Context, command string) (string, error) {
		calls.Add(1)
		close(started)
		<-ctx.Done()
		close(canceled)
		return "old response", ctx.Err()
	})
	b := newBrowser(context.Background(), client, "test:5002", new(logBuffer))
	defer b.cancel()
	b.ready = true
	b.command.SetText("#-1.value=new")
	b.submitCommand()
	<-started
	b.connect(true)
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("reconnect did not cancel local wait")
	}
	b.workers.Wait()
	for len(b.results) > 0 {
		r := <-b.results
		if r.kind == commandResult {
			b.apply(r)
		}
	}
	if b.command.GetText() != "" || b.sending || strings.Contains(b.response.GetText(false), "old response") {
		t.Fatal("reconnect retained command target/result")
	}
	if calls.Load() != 1 {
		t.Fatal("command replayed")
	}
}

func TestInspectorQuitDuringCommand(t *testing.T) {
	started := make(chan struct{})
	canceled := make(chan struct{})
	client := editorClient(func(ctx context.Context, command string) (string, error) {
		close(started)
		<-ctx.Done()
		close(canceled)
		return "", ctx.Err()
	})
	ui := startBrowser(t, client)
	ui.wait(t, func(s snapshot) bool { return s.metadataLoaded })
	ui.key(tcell.KeyTab, 0)
	ui.key(tcell.KeyEnter, 0)
	ui.key(tcell.KeyEnter, 0)
	ui.key(tcell.KeyRune, ' ')
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("Send not started")
	}
	ui.key(tcell.KeyCtrlC, 0)
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("quit did not cancel command wait")
	}
}

func TestInspectorPartialMetadataError(t *testing.T) {
	b := newBrowser(context.Background(), &fakeClient{}, "test:5002", new(logBuffer))
	defer b.cancel()
	b.inspectTarget = b.root
	b.info = metadata{class: 181, propertyErr: errors.New("bad property list"), methods: []member{{handle: -1, name: "get"}}}
	b.renderInspector()
	if !strings.Contains(b.memberDetails.GetText(false), "bad property list") {
		t.Fatal("property error hidden")
	}
	b.switchTab()
	if b.table.GetCell(1, 1).Text != "get" {
		t.Fatal("successful method list hidden by property failure")
	}
}
