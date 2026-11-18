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
				return strings.Replace(oneProperty, "1;-1;", "1;1;", 1), nil
			default:
				return strings.Replace(oneMethod, "1;-2;", "1;2;", 1), nil
			}
		},
	}
}

func TestCombinedMemberWorkflow(t *testing.T) {
	commands := make(chan string, 8)
	ui := startBrowser(t, editorClient(func(ctx context.Context, command string) (string, error) {
		commands <- command
		return "line 1\n[red]line 2\x1b", nil
	}))
	ui.wait(t, func(s snapshot) bool { return s.metadataLoaded && s.class == 181 })
	ui.key(tcell.KeyTab, 0)
	ui.wait(t, func(s snapshot) bool { return s.memberFocus && strings.Contains(s.details, "PropertyHandle: 1") })
	select {
	case command := <-commands:
		t.Fatalf("selection sent %s", command)
	default:
	}
	ui.key(tcell.KeyRune, 'g')
	ui.wait(t, func(s snapshot) bool { return strings.Contains(s.response, "Result:") && !s.sending })
	if got := <-commands; got != "/pvServer.value" {
		t.Fatalf("read: %q", got)
	}
	ui.key(tcell.KeyEnter, 0)
	ui.wait(t, func(s snapshot) bool { return s.commandFocus && s.command == "" })
	for _, r := range "qRrgGsc" {
		ui.key(tcell.KeyRune, r)
	}
	ui.key(tcell.KeyEnter, 0)
	s := ui.wait(t, func(s snapshot) bool {
		return s.memberFocus && !s.sending && strings.Contains(s.response, "Result:") && strings.Contains(s.response, "=qRrgGsc")
	})
	if got := <-commands; got != "/pvServer.value=qRrgGsc" {
		t.Fatalf("write: %q", got)
	}
	if !strings.Contains(s.response, `line 1\n[red]line 2�`) {
		t.Fatalf("response: %q", s.response)
	}
	ui.key(tcell.KeyEnter, 0)
	ui.wait(t, func(s snapshot) bool { return s.commandFocus && s.command == "" })
	ui.key(tcell.KeyEscape, 0)
	ui.wait(t, func(s snapshot) bool { return s.memberFocus })
	select {
	case command := <-commands:
		t.Fatalf("Esc sent %s", command)
	default:
	}
	ui.key(tcell.KeyEnter, 0)
	ui.key(tcell.KeyEnter, 0)
	ui.wait(t, func(s snapshot) bool {
		return strings.Contains(s.response, `Command: /pvServer.value=""`) && !s.sending
	})
	if got := <-commands; got != `/pvServer.value=""` {
		t.Fatalf("empty write: %q", got)
	}
	ui.key(tcell.KeyDown, 0) // Skips divider.
	ui.wait(t, func(s snapshot) bool { return strings.Contains(s.details, "MethodHandle: 2") })
	ui.key(tcell.KeyRune, 'g')
	ui.wait(t, func(s snapshot) bool { return strings.Contains(s.status, "g reads properties only") })
	select {
	case command := <-commands:
		t.Fatalf("g invoked %s", command)
	default:
	}
	ui.key(tcell.KeyEnter, 0)
	ui.key(tcell.KeyEnter, 0)
	ui.wait(t, func(s snapshot) bool { return strings.Contains(s.response, "Command: /pvServer.set()") && !s.sending })
	if got := <-commands; got != "/pvServer.set()" {
		t.Fatalf("method: %q", got)
	}
	ui.key(tcell.KeyEnter, 0)
	for _, r := range `1,"hello",2` {
		ui.key(tcell.KeyRune, r)
	}
	ui.key(tcell.KeyEnter, 0)
	ui.wait(t, func(s snapshot) bool { return strings.Contains(s.response, `/pvServer.set(1,"hello",2)`) && !s.sending })
	if got := <-commands; got != `/pvServer.set(1,"hello",2)` {
		t.Fatalf("parameters: %q", got)
	}
}

func TestImmediateFocusAndPendingSuppression(t *testing.T) {
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	defer close(release)
	var count atomic.Int32
	ui := startBrowser(t, editorClient(func(ctx context.Context, command string) (string, error) {
		count.Add(1)
		started <- struct{}{}
		select {
		case <-release:
		case <-ctx.Done():
			return "", ctx.Err()
		}
		return "", errors.New("remote failure")
	}))
	ui.wait(t, func(s snapshot) bool { return s.metadataLoaded })
	ui.key(tcell.KeyTab, 0)
	ui.key(tcell.KeyEnter, 0)
	ui.key(tcell.KeyEnter, 0)
	<-started
	ui.wait(t, func(s snapshot) bool { return s.memberFocus && s.sending })
	ui.key(tcell.KeyRune, 'g')
	ui.key(tcell.KeyEnter, 0)
	ui.wait(t, func(s snapshot) bool { return strings.Contains(s.status, "already pending") })
	if count.Load() != 1 {
		t.Fatal("duplicate operation sent")
	}
	release <- struct{}{}
	ui.wait(t, func(s snapshot) bool { return strings.Contains(s.response, "remote failure") && !s.sending })
}

func TestCombinedListAndMetadataErrors(t *testing.T) {
	b := newBrowser(context.Background(), &fakeClient{}, "test:5002", new(logBuffer))
	defer b.cancel()
	b.inspectTarget = b.root
	b.showGlobals = true
	b.info = metadata{class: 181, properties: []member{{handle: -1, name: "value", description: "property", datatype: "t"}},
		methods: []member{{handle: -2, name: "set", description: "method", request: "t"}}}
	b.renderInspector()
	if b.table.GetCell(1, 0).Text != "value" || b.table.GetCell(3, 0).Text != "set()" || !b.table.GetCell(2, 0).NotSelectable {
		t.Fatal("members not combined with nonselectable divider")
	}
	b.app.SetFocus(b.table)
	b.selectMember(3)
	if !strings.Contains(b.details.GetText(false), "Request: t") {
		t.Fatal("method metadata hidden")
	}
	b.info.propertyErr = errors.New("bad property list")
	b.renderInspector()
	if !strings.Contains(b.table.GetCell(1, 0).Text, "bad property list") || b.table.GetCell(3, 0).Text != "set()" {
		t.Fatal("successful method list hidden")
	}
}

func TestInspectorStaleMetadataAndCommand(t *testing.T) {
	b := newBrowser(context.Background(), &fakeClient{}, "test:5002", new(logBuffer))
	defer b.cancel()
	b.inspectSequence = 5
	b.apply(result{kind: metadataResult, generation: b.generation, sequence: 4, info: metadata{class: 999}})
	if b.info.class == 999 {
		t.Fatal("old metadata applied")
	}
	b.apply(result{kind: metadataResult, generation: b.generation, sequence: 5, info: metadata{class: 181}})
	if b.info.class != 181 {
		t.Fatal("current metadata dropped")
	}
	b.commandSequence = 7
	b.clearInspector()
	b.apply(result{kind: commandResult, generation: b.generation, sequence: 7, text: "stale"})
	if strings.Contains(b.response.GetText(false), "stale") {
		t.Fatal("stale command applied")
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
	b.inspect(b.makeBranch(object{id: -1}, b.root), false)
	<-started
	b.inspect(b.makeBranch(object{id: -2}, b.root), false)
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("metadata not canceled")
	}
	b.workers.Wait()
	b.drain()
	if b.info.class != 182 {
		t.Fatal("stale metadata applied")
	}
}

func TestCommandCancellationAndNoReplay(t *testing.T) {
	started := make(chan struct{})
	canceled := make(chan struct{})
	var calls atomic.Int32
	b := newBrowser(context.Background(), editorClient(func(ctx context.Context, command string) (string, error) {
		calls.Add(1)
		close(started)
		<-ctx.Done()
		close(canceled)
		return "old", ctx.Err()
	}), "test:5002", new(logBuffer))
	defer b.cancel()
	b.ready = true
	b.submit("#-1.value=new")
	<-started
	b.connect(true)
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("command not canceled")
	}
	b.workers.Wait()
	for len(b.results) > 0 {
		r := <-b.results
		if r.kind == commandResult {
			b.apply(r)
		}
	}
	if b.sending || !strings.Contains(b.response.GetText(false), "local wait canceled") {
		t.Fatal("old command result applied")
	}
	if calls.Load() != 1 {
		t.Fatal("command replayed")
	}
}

func TestMacrosPlaceholderCancelsEdit(t *testing.T) {
	var calls atomic.Int32
	ui := startBrowser(t, editorClient(func(context.Context, string) (string, error) { calls.Add(1); return "", nil }))
	ui.wait(t, func(s snapshot) bool { return s.metadataLoaded })
	ui.key(tcell.KeyTab, 0)
	ui.key(tcell.KeyEnter, 0)
	ui.wait(t, func(s snapshot) bool { return s.commandFocus })
	ui.key(tcell.KeyF2, 0)
	ui.wait(t, func(s snapshot) bool { return s.macros && strings.Contains(s.rendered, "not implemented") })
	ui.key(tcell.KeyEnter, 0)
	ui.key(tcell.KeyEnter, 0)
	ui.key(tcell.KeyF2, 0)
	ui.wait(t, func(s snapshot) bool { return !s.macros && !s.commandFocus })
	if calls.Load() != 0 {
		t.Fatal("placeholder executed command")
	}
}

func TestQuitDuringOperation(t *testing.T) {
	started := make(chan struct{})
	canceled := make(chan struct{})
	ui := startBrowser(t, editorClient(func(ctx context.Context, command string) (string, error) {
		close(started)
		<-ctx.Done()
		close(canceled)
		return "", ctx.Err()
	}))
	ui.wait(t, func(s snapshot) bool { return s.metadataLoaded })
	ui.key(tcell.KeyTab, 0)
	ui.key(tcell.KeyRune, 'g')
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("operation not started")
	}
	ui.key(tcell.KeyCtrlC, 0)
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("quit did not cancel")
	}
}
