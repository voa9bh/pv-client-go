package main

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

type browserClient interface {
	textClient
	Connect(context.Context) error
	Reconnect(context.Context) error
}

type loadState int

const (
	unloaded loadState = iota
	loading
	loaded
	failed
)

type branch struct {
	object
	node     *tview.TreeNode
	parent   *branch
	state    loadState
	revision uint64
	cancel   context.CancelFunc
}

type result struct {
	generation uint64
	target     *branch // nil means connection result
	revision   uint64
	children   []object
	err        error
}

type logBuffer struct {
	mu   sync.Mutex
	text string
}

func (b *logBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.text += string(p)
	if len(b.text) > 4096 {
		b.text = b.text[len(b.text)-4096:]
	}
	return len(p), nil
}

func (b *logBuffer) snapshot() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.text
}

type browser struct {
	app        *tview.Application
	tree       *tview.TreeView
	details    *tview.TextView
	status     *tview.TextView
	logView    *tview.TextView
	logs       *logBuffer
	client     browserClient
	address    string
	ctx        context.Context
	cancel     context.CancelFunc
	workers    sync.WaitGroup
	results    chan result
	root       *branch
	generation uint64
	connecting bool
	ready      bool
}

func clean(text string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return '\uFFFD'
		}
		return r
	}, text)
}

func newBrowser(parent context.Context, client browserClient, address string, logs *logBuffer) *browser {
	ctx, cancel := context.WithCancel(parent)
	b := &browser{
		app: tview.NewApplication(), tree: tview.NewTreeView(),
		details: tview.NewTextView(), status: tview.NewTextView(),
		logView: tview.NewTextView(), logs: logs, client: client, address: address,
		ctx: ctx, cancel: cancel, results: make(chan result, 32),
	}
	b.tree.SetBorder(true).SetTitle(" Objects ")
	b.details.SetBorder(true).SetTitle(" Selected object ")
	b.logView.SetBorder(true).SetTitle(" Client diagnostics (latest) ")
	layout := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(b.tree, 0, 1, true).
		AddItem(b.details, 5, 0, false).
		AddItem(b.status, 2, 0, false).
		AddItem(b.logView, 3, 0, false).
		AddItem(tview.NewTextView().SetText("Up/Down: navigate  Enter/Right: expand  Left: collapse/parent  r: refresh  R: reconnect  q/Ctrl+C: quit"), 2, 0, false)
	b.app.SetRoot(layout, true).SetFocus(b.tree)
	b.tree.SetChangedFunc(func(node *tview.TreeNode) {
		if branch, ok := node.GetReference().(*branch); ok {
			b.showDetails(branch)
		}
	})
	b.app.SetInputCapture(b.input)
	b.reset()
	b.setStatus("Not connected")
	return b
}

func (b *browser) setStatus(text string) {
	b.status.SetText(clean(b.address + " | " + text))
}

func (b *browser) showDetails(n *branch) {
	b.details.SetText(fmt.Sprintf("Name: %s\nAddress: %s\nPath (informational): %s",
		clean(n.name), n.address(), clean(n.path)))
}

func (b *browser) label(n *branch) {
	name := clean(n.name)
	if n.root {
		name = "/pvServer"
	} else {
		name += fmt.Sprintf(" (#%d)", n.id)
	}
	switch n.state {
	case unloaded:
		name += " [+]"
	case loading:
		name += " [loading...]"
	case failed:
		name += " [error/stale - r to retry]"
	case loaded:
		if len(n.node.GetChildren()) == 0 {
			name += " [empty]"
		}
	}
	// TreeView interprets color tags even when TextViews use plain text.
	n.node.SetText(tview.Escape(name))
}

func (b *browser) makeBranch(o object, parent *branch) *branch {
	n := &branch{object: o, parent: parent, node: tview.NewTreeNode("")}
	n.node.SetReference(n).SetExpanded(false)
	b.label(n)
	return n
}

func (b *browser) invalidate(n *branch) {
	if n.cancel != nil {
		n.cancel()
		n.cancel = nil
	}
	n.revision++
	if n.state == loading {
		n.state = failed
		b.label(n)
	}
	for _, child := range n.node.GetChildren() {
		b.invalidate(child.GetReference().(*branch))
	}
}

func (b *browser) reset() {
	if b.root != nil {
		b.invalidate(b.root)
	}
	b.generation++
	b.root = b.makeBranch(rootObject(), nil)
	b.tree.SetRoot(b.root.node).SetCurrentNode(b.root.node)
	b.showDetails(b.root)
}

func (b *browser) post(r result) {
	select {
	case b.results <- r:
	case <-b.ctx.Done():
	}
}

func (b *browser) connect(reconnect bool) {
	if b.connecting {
		return
	}
	b.ready, b.connecting = false, true
	b.reset()
	b.setStatus("Connecting...")
	generation := b.generation
	b.workers.Add(1)
	go func() {
		defer b.workers.Done()
		ctx, cancel := context.WithTimeout(b.ctx, 30*time.Second)
		defer cancel()
		var err error
		if reconnect {
			err = b.client.Reconnect(ctx)
		} else {
			err = b.client.Connect(ctx)
		}
		b.post(result{generation: generation, err: err})
	}()
}

func (b *browser) load(n *branch) {
	if !b.ready {
		b.setStatus("Not ready; R connects/reconnects")
		return
	}
	if n.state == loading {
		return
	}
	b.invalidate(n)
	n.state = loading
	n.node.SetExpanded(true)
	b.label(n)
	b.setStatus("Loading children of " + n.address() + "...")
	ctx, cancel := context.WithTimeout(b.ctx, 30*time.Second)
	n.cancel = cancel
	generation, revision := b.generation, n.revision
	ancestors := make(map[int64]bool)
	for p := n; p != nil; p = p.parent {
		if !p.root {
			ancestors[p.id] = true
		}
	}
	// Copy immutable input before starting the worker.
	parent := n.object
	b.workers.Add(1)
	go func() {
		defer b.workers.Done()
		defer cancel()
		list, err := children(ctx, b.client, parent, ancestors)
		b.post(result{generation: generation, target: n, revision: revision, children: list, err: err})
	}()
}

func (b *browser) apply(r result) {
	if r.generation != b.generation {
		return
	}
	if r.target == nil {
		b.connecting = false
		if r.err != nil {
			b.setStatus("Connection failed: " + r.err.Error() + "; R to retry")
			return
		}
		b.ready = true
		b.load(b.root)
		return
	}
	n := r.target
	if n.revision != r.revision {
		return
	}
	n.cancel = nil
	if r.err != nil {
		n.state = failed
		b.label(n)
		b.setStatus("Load failed: " + r.err.Error() + "; r retries, R rebuilds handles")
		return
	}
	// Invalidate old descendants before replacing the snapshot.
	for selected := b.selected(); selected != nil; selected = selected.parent {
		if selected == n {
			b.tree.SetCurrentNode(n.node)
			break
		}
	}
	for _, child := range n.node.GetChildren() {
		b.invalidate(child.GetReference().(*branch))
	}
	n.node.ClearChildren()
	for _, o := range r.children {
		n.node.AddChild(b.makeBranch(o, n).node)
	}
	n.state = loaded
	b.label(n)
	b.setStatus(fmt.Sprintf("Loaded %d children of %s (last operation)", len(r.children), n.address()))
	b.showDetails(b.selected())
}

func (b *browser) selected() *branch {
	if node := b.tree.GetCurrentNode(); node != nil {
		if n, ok := node.GetReference().(*branch); ok {
			return n
		}
	}
	return b.root
}

func (b *browser) expand(n *branch, toggle bool) {
	if n.state == unloaded || n.state == failed {
		b.load(n)
		return
	}
	if toggle {
		n.node.SetExpanded(!n.node.IsExpanded())
	} else {
		n.node.SetExpanded(true)
	}
}

func (b *browser) drain() {
	for {
		select {
		case r := <-b.results:
			b.apply(r)
		default:
			b.logView.SetText(clean(b.logs.snapshot())).ScrollToEnd()
			return
		}
	}
}

func (b *browser) input(event *tcell.EventKey) *tcell.EventKey {
	if event.Key() == tcell.KeyF24 {
		b.drain()
		return nil
	}
	if event.Key() == tcell.KeyCtrlC || event.Rune() == 'q' {
		b.cancel()
		b.app.Stop()
		return nil
	}
	n := b.selected()
	switch event.Key() {
	case tcell.KeyEnter:
		b.expand(n, true)
		return nil
	case tcell.KeyRight:
		b.expand(n, false)
		return nil
	case tcell.KeyLeft:
		if n.node.IsExpanded() {
			n.node.SetExpanded(false)
		} else if n.parent != nil {
			b.tree.SetCurrentNode(n.parent.node)
			b.showDetails(n.parent)
		}
		return nil
	}
	switch event.Rune() {
	case 'r':
		b.invalidate(n)
		n.state = unloaded
		b.load(n)
		return nil
	case 'R':
		b.connect(true)
		return nil
	}
	return event
}

func (b *browser) run(screen tcell.Screen) error {
	if screen != nil {
		b.app.SetScreen(screen)
	}
	stopPump := make(chan struct{})
	var pump sync.WaitGroup
	started := false
	b.app.SetBeforeDrawFunc(func(screen tcell.Screen) bool {
		if !started {
			started = true
			b.connect(false)
			pump.Add(1)
			go func() {
				defer pump.Done()
				ticker := time.NewTicker(50 * time.Millisecond)
				defer ticker.Stop()
				for {
					select {
					case <-stopPump:
						return
					case <-ticker.C:
						key := tcell.KeyF24
						if b.ctx.Err() != nil {
							key = tcell.KeyCtrlC
						}
						// PostEvent is nonblocking. If the event queue is full,
						// retry on the next tick rather than blocking shutdown.
						screen.PostEvent(tcell.NewEventKey(key, 0, tcell.ModNone))
					}
				}
			}()
		}
		return false
	})
	err := b.app.Run()
	close(stopPump)
	pump.Wait()
	b.cancel()
	b.workers.Wait()
	return err
}
