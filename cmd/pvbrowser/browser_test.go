package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

type snapshot struct {
	nodes      map[string]string
	states     map[string]loadState
	expanded   map[string]bool
	selected   string
	details    string
	status     string
	rendered   string
	generation uint64
	connecting bool
}

func capture(b *browser) snapshot {
	s := snapshot{
		nodes: make(map[string]string), states: make(map[string]loadState), expanded: make(map[string]bool),
		selected: b.selected().address(), details: b.details.GetText(false), status: b.status.GetText(false),
		generation: b.generation, connecting: b.connecting,
	}
	b.root.node.Walk(func(node, parent *tview.TreeNode) bool {
		n := node.GetReference().(*branch)
		s.nodes[n.address()] = node.GetText()
		s.states[n.address()] = n.state
		s.expanded[n.address()] = node.IsExpanded()
		return true
	})
	return s
}

type runningBrowser struct {
	b     *browser
	views chan snapshot
	done  chan error
}

func startBrowser(t *testing.T, client browserClient) *runningBrowser {
	t.Helper()
	b := newBrowser(context.Background(), client, "localhost:5002", new(logBuffer))
	ui := &runningBrowser{b: b, views: make(chan snapshot, 8), done: make(chan error, 1)}
	b.app.SetAfterDrawFunc(func(screen tcell.Screen) {
		s := capture(b)
		width, height := screen.Size()
		var rendered strings.Builder
		for y := 0; y < height; y++ {
			for x := 0; x < width; x++ {
				r, _, _, _ := screen.GetContent(x, y)
				rendered.WriteRune(r)
			}
			rendered.WriteByte('\n')
		}
		s.rendered = rendered.String()
		select {
		case ui.views <- s:
		default:
		}
	})
	go func() { ui.done <- b.run(tcell.NewSimulationScreen("UTF-8")) }()
	t.Cleanup(func() {
		b.cancel()
		select {
		case err := <-ui.done:
			if err != nil {
				t.Errorf("terminal run: %v", err)
			}
		case <-time.After(4 * time.Second):
			t.Error("terminal did not stop")
		}
	})
	return ui
}

func (u *runningBrowser) key(key tcell.Key, r rune) {
	u.b.app.QueueEvent(tcell.NewEventKey(key, r, tcell.ModNone))
}

func (u *runningBrowser) wait(t *testing.T, predicate func(snapshot) bool) snapshot {
	t.Helper()
	timer := time.NewTimer(4 * time.Second)
	defer timer.Stop()
	var last snapshot
	for {
		select {
		case s := <-u.views:
			last = s
			if predicate(s) {
				return s
			}
		case <-timer.C:
			t.Fatalf("UI condition not reached; last snapshot: %+v", last)
		}
	}
}

func TestBrowserNavigationCachingAndEmpty(t *testing.T) {
	var mu sync.Mutex
	counts := make(map[string]int)
	client := &fakeClient{ask: func(_ context.Context, command string) (string, error) {
		mu.Lock()
		counts[command]++
		mu.Unlock()
		replies := map[string]string{
			"/pvServer.getNextChildObject(0)":  "-1",
			"#-1.name":                         "procs[red]\n",
			"/pvServer.getNextChildObject(-1)": "0",
			"#-1.getNextChildObject(0)":        "-2",
			"#-2.name":                         "leaf",
			"#-1.getNextChildObject(-2)":       "0",
			"#-2.getNextChildObject(0)":        "0",
		}
		if reply, ok := replies[command]; ok {
			return reply, nil
		}
		return "", fmt.Errorf("unexpected command %q", command)
	}}
	ui := startBrowser(t, client)
	ui.wait(t, func(s snapshot) bool { return s.states["/pvServer"] == loaded })
	ui.key(tcell.KeyDown, 0)
	s := ui.wait(t, func(s snapshot) bool { return s.selected == "#-1" })
	if !strings.Contains(s.details, "/pvServer/procs[red]") || strings.Contains(s.details, "procs[red]\n") {
		t.Fatalf("details not plain/sanitized: %q", s.details)
	}
	if strings.Contains(s.nodes["#-1"], "[red]") {
		t.Fatalf("tree markup was not escaped: %q", s.nodes["#-1"])
	}
	if !strings.Contains(s.rendered, "procs[red]") {
		t.Fatalf("literal object name not rendered: %s", s.rendered)
	}
	ui.key(tcell.KeyRight, 0)
	ui.wait(t, func(s snapshot) bool { return s.states["#-1"] == loaded && s.expanded["#-1"] })
	ui.key(tcell.KeyEnter, 0)
	ui.wait(t, func(s snapshot) bool { return !s.expanded["#-1"] })
	ui.key(tcell.KeyEnter, 0)
	ui.wait(t, func(s snapshot) bool { return s.expanded["#-1"] })
	ui.key(tcell.KeyDown, 0)
	ui.wait(t, func(s snapshot) bool { return s.selected == "#-2" })
	ui.key(tcell.KeyRight, 0)
	ui.wait(t, func(s snapshot) bool { return strings.Contains(s.nodes["#-2"], tview.Escape("[empty]")) })
	ui.key(tcell.KeyLeft, 0)
	ui.key(tcell.KeyLeft, 0)
	ui.wait(t, func(s snapshot) bool { return s.selected == "#-1" })
	mu.Lock()
	defer mu.Unlock()
	if counts["#-1.getNextChildObject(0)"] != 1 {
		t.Fatal("expanding cached children caused another request")
	}
}

func TestBrowserRefreshErrorAndReconnect(t *testing.T) {
	var enumerations atomic.Int32
	var reconnects atomic.Int32
	client := &fakeClient{
		reconnect: func(context.Context) error { reconnects.Add(1); return nil },
		ask: func(_ context.Context, command string) (string, error) {
			switch command {
			case "/pvServer.getNextChildObject(0)":
				if enumerations.Add(1) == 2 {
					return "", errors.New("refresh failed")
				}
				if reconnects.Load() > 0 {
					return "-9", nil
				}
				return "-1", nil
			case "#-1.name":
				return "old", nil
			case "#-9.name":
				return "new", nil
			case "/pvServer.getNextChildObject(-1)", "/pvServer.getNextChildObject(-9)":
				return "0", nil
			default:
				return "", fmt.Errorf("unexpected %s", command)
			}
		},
	}
	ui := startBrowser(t, client)
	first := ui.wait(t, func(s snapshot) bool { return s.states["/pvServer"] == loaded })
	ui.key(tcell.KeyRune, 'r')
	s := ui.wait(t, func(s snapshot) bool { return s.states["/pvServer"] == failed })
	if _, exists := s.nodes["#-1"]; !exists || !strings.Contains(s.status, "refresh failed") {
		t.Fatal("refresh error hid the prior snapshot/error")
	}
	ui.key(tcell.KeyRune, 'r')
	ui.wait(t, func(s snapshot) bool { return s.states["/pvServer"] == loaded && enumerations.Load() >= 3 })
	ui.key(tcell.KeyRune, 'R')
	s = ui.wait(t, func(s snapshot) bool { return s.generation > first.generation && s.states["/pvServer"] == loaded })
	if _, exists := s.nodes["#-1"]; exists {
		t.Fatal("reconnect retained cached ID")
	}
	if _, exists := s.nodes["#-9"]; !exists {
		t.Fatal("reconnect did not rebuild")
	}
}

func TestBrowserInitialFailureAndRetry(t *testing.T) {
	client := &fakeClient{
		connect: func(context.Context) error { return errors.New("unavailable") },
		ask:     func(context.Context, string) (string, error) { return "0", nil },
	}
	ui := startBrowser(t, client)
	ui.wait(t, func(s snapshot) bool { return strings.Contains(s.status, "unavailable") })
	ui.key(tcell.KeyRune, 'R')
	ui.wait(t, func(s snapshot) bool { return s.states["/pvServer"] == loaded })
}

func TestBrowserStaleResultsAndRefreshInvalidation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b := newBrowser(ctx, &fakeClient{}, "test:5002", new(logBuffer))
	old := b.root
	b.ready = true
	b.apply(result{generation: b.generation, target: old, revision: old.revision, children: []object{{id: -1, name: "a"}}})
	child := old.node.GetChildren()[0].GetReference().(*branch)
	child.state = loading
	childRevision := child.revision
	b.invalidate(old)
	b.apply(result{generation: b.generation, target: child, revision: childRevision, children: []object{{id: -2, name: "stale"}}})
	if len(child.node.GetChildren()) != 0 {
		t.Fatal("invalidated descendant accepted stale load")
	}
	b.apply(result{generation: b.generation, target: old, revision: old.revision, children: []object{{id: -3, name: "fresh"}}})
	if got := old.node.GetChildren()[0].GetReference().(*branch).id; got != -3 {
		t.Fatalf("refresh did not replace descendants: %d", got)
	}
	oldGeneration := b.generation
	b.reset()
	b.apply(result{generation: oldGeneration, target: old, revision: old.revision, children: []object{{id: -99}}})
	if len(b.root.node.GetChildren()) != 0 {
		t.Fatal("old session repopulated tree")
	}
}

func TestBrowserQuitDuringLoad(t *testing.T) {
	started := make(chan struct{})
	canceled := make(chan struct{})
	client := &fakeClient{ask: func(ctx context.Context, _ string) (string, error) {
		close(started)
		<-ctx.Done()
		close(canceled)
		return "", ctx.Err()
	}}
	ui := startBrowser(t, client)
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("load did not start")
	}
	ui.key(tcell.KeyRune, 'q')
	select {
	case <-canceled:
	case <-time.After(3 * time.Second):
		t.Fatal("quit did not cancel pending request")
	}
}
