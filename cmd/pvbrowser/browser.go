package main

import (
	"context"
	"errors"
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
	node        *tview.TreeNode
	parent      *branch
	state       loadState
	revision    uint64
	cancel      context.CancelFunc
	childKnown  bool
	hasChildren bool
	probeErr    error
}

type result struct {
	kind        resultKind
	sequence    uint64
	info        metadata
	text        string
	command     string
	generation  uint64
	target      *branch // nil means connection result
	revision    uint64
	children    []object
	err         error
	hasChildren bool
	macro       macroEvent
	file        macroFileEvent
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
	inspector
	macroWorkspace
	app           *tview.Application
	tree          *tview.TreeView
	details       *tview.TextView
	status        *tview.TextView
	pages         *tview.Pages
	tabs          *tview.TextView
	bar           *actionBar
	layout        *tview.Flex
	newClient     clientFactory
	ownedClient   ownedBrowserClient
	connectCancel context.CancelFunc
	connectionErr error
	closeMu       sync.Mutex
	closeErr      error
	pendingFiles  []macroFileEvent
	macros        bool
	lastLog       string
	logs          *logBuffer
	client        browserClient
	address       string
	ctx           context.Context
	cancel        context.CancelFunc
	workers       sync.WaitGroup
	results       chan result
	root          *branch
	generation    uint64
	connecting    bool
	ready         bool
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
		logs: logs, client: client, address: address,
		ctx: ctx, cancel: cancel, results: make(chan result, 32),
	}
	b.tree.SetBorder(true).SetTitle(" Objects ")
	b.details.SetBorder(true).SetTitle(" Selected object ")
	b.initInspector()
	b.initMacros()
	columns := tview.NewFlex().
		AddItem(b.tree, 0, 1, true).
		AddItem(b.inspectorLayout, 0, 2, false)
	b.pages = tview.NewPages().AddPage("objects", columns, true, true).
		AddPage("macros", b.macroLayout, true, false)
	b.tabs = tview.NewTextView().SetText("[Objects] | Macros (F2)")
	b.bar = newActionBar()
	b.updateActions()
	layout := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(b.tabs, 1, 0, false).
		AddItem(b.pages, 0, 1, true).
		AddItem(b.details, 5, 0, false).
		AddItem(b.status, 1, 0, false).
		AddItem(b.bar, 2, 0, false)
	b.layout = layout
	b.app.SetRoot(layout, true).SetFocus(b.tree).EnablePaste(true).EnableMouse(true)
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
	b.inspect(n, false)
	b.updateDetails()
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
		switch {
		case n.probeErr != nil:
			name += " [check failed - r to retry]"
		case !n.childKnown:
			name += " [checking...]"
		case n.hasChildren:
			name += " [+]"
		default:
			name += " [ ]"
		}
	case loading:
		name += " [loading...]"
	case failed:
		name += " [error/stale - r to retry]"
	case loaded:
		if len(n.node.GetChildren()) == 0 {
			name += " [ ]"
		} else {
			name += " [+]"
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
		if n.state == unloaded {
			n.probeErr = context.Canceled
			b.label(n)
		}
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
	b.clearInspector()
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
	b.startConnection(reconnect)
}

func ancestorsOf(n *branch) map[int64]bool {
	ancestors := make(map[int64]bool)
	for p := n; p != nil; p = p.parent {
		if !p.root {
			ancestors[p.id] = true
		}
	}
	return ancestors
}

func (b *browser) probeChildren(nodes []*branch) {
	type check struct {
		result
		ctx       context.Context
		cancel    context.CancelFunc
		object    object
		ancestors map[int64]bool
	}
	checks := make([]check, 0, len(nodes))
	for _, n := range nodes {
		ctx, cancel := context.WithCancel(b.ctx)
		n.cancel = cancel
		checks = append(checks, check{
			result: result{kind: probeResult, generation: b.generation, target: n, revision: n.revision},
			ctx:    ctx, cancel: cancel, object: n.object, ancestors: ancestorsOf(n),
		})
	}
	if len(checks) == 0 {
		return
	}
	b.workers.Add(1)
	client := b.client
	go func() {
		defer b.workers.Done()
		for _, c := range checks {
			ctx, cancel := context.WithTimeout(c.ctx, 30*time.Second)
			id, err := nextChild(ctx, client, c.object, 0, c.ancestors)
			cancel()
			c.cancel()
			c.result.hasChildren, c.result.err = id != 0, err
			b.post(c.result)
		}
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
	ancestors := ancestorsOf(n)
	// Copy immutable input before starting the worker.
	parent := n.object
	client := b.client
	b.workers.Add(1)
	go func() {
		defer b.workers.Done()
		defer cancel()
		list, err := children(ctx, client, parent, ancestors)
		b.post(result{generation: generation, target: n, revision: revision, children: list, err: err})
	}()
}

func (b *browser) apply(r result) {
	if r.kind == clientCloseResult {
		b.setStatus("Client close failed: " + r.err.Error())
		return
	}
	if r.kind == macroResult {
		b.applyMacro(r)
		return
	}
	if r.kind == macroFileResult {
		if b.macroDialog {
			b.pendingFiles = append(b.pendingFiles, r.file)
		} else {
			b.applyMacroFile(r.file)
		}
		return
	}
	if r.generation != b.generation {
		return
	}
	if r.kind == probeResult {
		n := r.target
		if n.revision != r.revision || n.state != unloaded {
			return
		}
		n.cancel = nil
		n.probeErr = r.err
		if r.err != nil {
			b.setStatus("Child check failed: " + r.err.Error() + "; r retries")
		} else {
			n.childKnown, n.hasChildren = true, r.hasChildren
			if !r.hasChildren {
				n.state = loaded
			}
		}
		b.label(n)
		return
	}
	if r.kind != treeResult {
		b.applyInspector(r)
		return
	}
	if r.target == nil {
		b.connecting = false
		b.connectCancel = nil
		if r.err != nil {
			b.connectionFailed(r.err)
			return
		}
		b.ready = true
		b.tree.SetTitle(" Objects ")
		b.inspect(b.selected(), true)
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
	var nodes []*branch
	for _, o := range r.children {
		child := b.makeBranch(o, n)
		n.node.AddChild(child.node)
		nodes = append(nodes, child)
	}
	n.state = loaded
	n.childKnown, n.hasChildren, n.probeErr = true, len(nodes) > 0, nil
	b.label(n)
	b.setStatus(fmt.Sprintf("Loaded %d children of %s (last operation)", len(r.children), n.address()))
	b.showDetails(b.selected())
	b.probeChildren(nodes)
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
			if !b.macroDialog && len(b.pendingFiles) > 0 {
				event := b.pendingFiles[0]
				b.pendingFiles = b.pendingFiles[1:]
				b.applyMacroFile(event)
			}
			logs := b.logs.snapshot()
			if logs != b.lastLog {
				b.lastLog = logs
				lines := strings.Split(strings.TrimSpace(logs), "\n")
				b.setStatus(lines[len(lines)-1])
			}
			return
		}
	}
}

func (b *browser) input(event *tcell.EventKey) *tcell.EventKey {
	if event.Key() == tcell.KeyF24 {
		b.drain()
		return nil
	}
	if event.Key() == tcell.KeyCtrlC {
		b.quit()
		return nil
	}
	if b.macroDialog {
		return event
	}
	if event.Key() == tcell.KeyRune && event.Modifiers() == tcell.ModNone && !b.editing() {
		switch event.Rune() {
		case 'Q':
			b.quit()
			return nil
		case 'P':
			b.promptPort()
			return nil
		case 'R':
			b.connect(true)
			return nil
		}
	}
	if event.Key() == tcell.KeyTab || event.Key() == tcell.KeyBacktab {
		b.cycleFocus(event.Key() == tcell.KeyBacktab)
		return nil
	}
	if event.Key() == tcell.KeyF2 {
		b.switchWorkspace()
		return nil
	}
	if !b.macros && b.table.HasFocus() && event.Key() == tcell.KeyEnter && event.Modifiers() == tcell.ModNone {
		b.memberAction('\n')
		return nil
	}
	if !b.macros && b.table.HasFocus() && event.Key() == tcell.KeyRune && event.Modifiers() == tcell.ModNone {
		switch event.Rune() {
		case 'g':
			b.memberAction('g')
			return nil
		case 's':
			b.memberAction('s')
			return nil
		case 'G':
			b.toggleGlobals()
			return nil
		}
	}
	if b.macros {
		return b.macroInput(event)
	}
	if !b.tree.HasFocus() {
		return event
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
		b.refreshSelected()
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
		width, _ := screen.Size()
		b.layout.ResizeItem(b.bar, b.bar.rows(width), 0)
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
	if b.ownedClient != nil {
		b.closeClient(b.ownedClient)
		b.ownedClient = nil
	}
	b.workers.Wait()
	b.closeMu.Lock()
	defer b.closeMu.Unlock()
	return errors.Join(err, b.closeErr)
}
