package main

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

type ownedFakeClient struct {
	*fakeClient
	closes     atomic.Int32
	closeError error
}

func (c *ownedFakeClient) Close() error {
	c.closes.Add(1)
	return c.closeError
}

func emptyOwnedClient() *ownedFakeClient {
	return &ownedFakeClient{fakeClient: &fakeClient{ask: func(context.Context, string) (string, error) {
		return "0", nil
	}}}
}

func waitBrowserResult(t *testing.T, b *browser, predicate func() bool) {
	t.Helper()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for !predicate() {
		select {
		case r := <-b.results:
			b.apply(r)
		case <-timer.C:
			t.Fatalf("browser result timeout: %s", b.status.GetText(false))
		}
	}
}

func TestChangePortFailureRetainsEndpointAndRetry(t *testing.T) {
	old := emptyOwnedClient()
	b := newBrowser(context.Background(), old, "[::1]:5002", new(logBuffer))
	defer b.cancel()
	b.ownedClient = old
	next := emptyOwnedClient()
	var attempts atomic.Int32
	next.connect = func(context.Context) error { attempts.Add(1); return errors.New("connection refused") }
	next.reconnect = func(context.Context) error { attempts.Add(1); return nil }
	b.newClient = func(address string) (ownedBrowserClient, error) {
		if address != "[::1]:6000" {
			t.Errorf("host changed: %q", address)
		}
		return next, nil
	}
	b.ready = true
	child := b.makeBranch(object{id: 7, name: "old object"}, b.root)
	b.root.node.AddChild(child.node)
	b.macroEditor.SetText("keep macro", false)
	b.macroOutput.SetText("keep output")
	b.macroPath = "keep-path"
	generation := b.generation
	b.changePort("6000")
	waitBrowserResult(t, b, func() bool { return !b.connecting })
	if b.ready || b.address != "[::1]:6000" || b.connectionErr == nil || len(b.root.node.GetChildren()) != 0 {
		t.Fatal("failed connection left a ready client, old endpoint or stale objects")
	}
	if !strings.Contains(b.root.node.GetText(), "connection failed") || !strings.Contains(b.details.GetText(false), "connection refused") {
		t.Fatal("connection failure not visible in tree and information panel")
	}
	b.apply(result{generation: generation})
	b.logs.Write([]byte("unrelated log"))
	b.drain()
	if b.connectionErr == nil || !strings.Contains(b.tree.GetTitle(), "CONNECTION FAILED") {
		t.Fatal("late result or log cleared the persistent failure")
	}
	if b.macroEditor.GetText() != "keep macro" || b.macroOutput.GetText(false) != "keep output" || b.macroPath != "keep-path" {
		t.Fatal("port change discarded macro content")
	}
	b.connect(true)
	waitBrowserResult(t, b, func() bool { return b.ready && b.root.state == loaded && !b.inspectLoading })
	b.workers.Wait()
	if attempts.Load() != 2 || old.closes.Load() != 1 || next.closes.Load() != 0 || b.connectionErr != nil {
		t.Fatal("retry did not use new endpoint or old client was not closed exactly once")
	}
	b.closeClient(next)
	b.workers.Wait()
}

func TestPortDialogValidationAndCancel(t *testing.T) {
	b := macroBrowser(t, nil)
	b.newClient = func(string) (ownedBrowserClient, error) { return emptyOwnedClient(), nil }
	b.app.SetFocus(b.details)
	b.input(tcell.NewEventKey(tcell.KeyRune, 'P', tcell.ModNone))
	if !b.macroDialog {
		t.Fatal("P did not open port prompt")
	}
	field, ok := b.app.GetFocus().(*tview.InputField)
	if !ok || field.GetText() != "5002" {
		t.Fatal("port not prefilled")
	}
	for _, invalid := range []string{"", "0", "65536", "-1", " 5003", "50qPR"} {
		field.SetText(invalid)
		dialogKey(b, tcell.KeyEnter)
		if !b.macroDialog || b.connecting || b.address != "test:5002" {
			t.Fatalf("invalid port accepted: %q", invalid)
		}
	}
	dialogKey(b, tcell.KeyEscape)
	if b.macroDialog || !b.details.HasFocus() || b.address != "test:5002" {
		t.Fatal("cancel changed endpoint or did not restore focus")
	}
	b.promptPort()
	b.app.GetFocus().(*tview.InputField).SetText("06000")
	dialogKey(b, tcell.KeyEnter)
	waitBrowserResult(t, b, func() bool { return b.ready && !b.connecting && !b.inspectLoading && b.root.state == loaded })
	if b.address != "test:6000" || b.macroDialog || !b.details.HasFocus() {
		t.Fatal("valid port did not connect or restore focus")
	}
	b.closeClient(b.ownedClient)
	b.workers.Wait()
}

func TestPortChangeCancelsConnectAndRejectsLateResults(t *testing.T) {
	old := emptyOwnedClient()
	started, release := make(chan struct{}), make(chan struct{})
	old.connect = func(context.Context) error {
		close(started)
		<-release
		return errors.New("late old-port failure")
	}
	b := newBrowser(context.Background(), old, "localhost:5002", new(logBuffer))
	defer b.cancel()
	b.ownedClient = old
	first, second := emptyOwnedClient(), emptyOwnedClient()
	first.connect = func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	}
	b.newClient = func(address string) (ownedBrowserClient, error) {
		if strings.HasSuffix(address, ":6000") {
			return first, nil
		}
		return second, nil
	}
	b.connect(false)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("connection did not start")
	}
	b.changePort("6000")
	b.changePort("7000")
	close(release)
	waitBrowserResult(t, b, func() bool { return b.ready && !b.inspectLoading && b.root.state == loaded })
	b.workers.Wait()
	b.drain()
	if b.address != "localhost:7000" || !b.ready || b.connectionErr != nil || old.closes.Load() != 1 || first.closes.Load() != 1 {
		t.Fatal("late connection result replaced current port or client leaked")
	}
	b.closeClient(second)
	b.workers.Wait()
}

func TestPortChangeDuringMacroDoesNotSendToNewClient(t *testing.T) {
	old := emptyOwnedClient()
	started, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	old.ask = func(ctx context.Context, command string) (string, error) {
		calls.Add(1)
		close(started)
		<-ctx.Done()
		close(canceled)
		<-release
		return "", ctx.Err()
	}
	b := newBrowser(context.Background(), old, "localhost:5002", new(logBuffer))
	defer b.cancel()
	b.ready = true
	b.ownedClient = old
	next := emptyOwnedClient()
	next.ask = func(_ context.Context, command string) (string, error) {
		if command != "/pvServer.getNextChildObject(0)" {
			t.Errorf("old macro reached new client: %q", command)
		}
		return "0", nil
	}
	b.newClient = func(string) (ownedBrowserClient, error) { return next, nil }
	b.macroEditor.SetText("first\nnever sent", false)
	b.runMacro()
	<-started
	b.changePort("6000")
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("port change did not cancel macro wait")
	}
	waitBrowserResult(t, b, func() bool { return b.ready && !b.inspectLoading && b.root.state == loaded })
	b.runMacro()
	if !strings.Contains(b.status.GetText(false), "already running") {
		t.Fatal("overlapping run allowed before canceled worker returned")
	}
	close(release)
	finishMacro(t, b)
	b.workers.Wait()
	if calls.Load() != 1 || !strings.Contains(b.macroOutput.GetText(false), "Port change requested") {
		t.Fatal("macro replayed or cancellation warning missing")
	}
	b.closeClient(next)
	b.workers.Wait()
}

func TestOwnedClientShutdownAndCloseError(t *testing.T) {
	client := emptyOwnedClient()
	client.closeError = errors.New("close failed")
	b := newBrowser(context.Background(), client, "localhost:5002", new(logBuffer))
	b.ownedClient = client
	screen := tcell.NewSimulationScreen("UTF-8")
	b.app.SetAfterDrawFunc(func(tcell.Screen) { b.quit() })
	err := b.run(screen)
	if !errors.Is(err, client.closeError) || client.closes.Load() != 1 {
		t.Fatalf("client cleanup error lost: %v, closes %d", err, client.closes.Load())
	}
}

func TestParsePortBoundaries(t *testing.T) {
	for _, valid := range []string{"1", "65535", "05002"} {
		port, err := parsePort(valid)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := net.SplitHostPort(net.JoinHostPort("::1", port)); err != nil {
			t.Fatal(err)
		}
	}
}
