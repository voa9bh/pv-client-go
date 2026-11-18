package main

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

type resultKind int

const (
	treeResult resultKind = iota
	metadataResult
	commandResult
)

type inspector struct {
	inspectorLayout *tview.Flex
	tabs            *tview.TextView
	table           *tview.Table
	memberDetails   *tview.TextView
	command         *tview.InputField
	send            *tview.Button
	response        *tview.TextView
	inspectTarget   *branch
	inspectSequence uint64
	inspectCancel   context.CancelFunc
	info            metadata
	inspectErr      error
	inspectLoading  bool
	methodsTab      bool
	rendering       bool
	commandSequence uint64
	commandCancel   context.CancelFunc
	sending         bool
}

func (b *browser) initInspector() {
	b.tabs = tview.NewTextView()
	b.table = tview.NewTable().SetSelectable(true, false).SetFixed(1, 0)
	b.table.SetBorder(true)
	b.memberDetails = tview.NewTextView()
	b.memberDetails.SetBorder(true).SetTitle(" Member details ")
	b.command = tview.NewInputField().SetLabel("Command: ")
	b.command.SetDoneFunc(func(key tcell.Key) {
		if key == tcell.KeyEnter {
			b.app.SetFocus(b.send)
		}
	})
	b.send = tview.NewButton("Send").SetSelectedFunc(b.submitCommand)
	b.response = tview.NewTextView().SetScrollable(true)
	b.response.SetBorder(true).SetTitle(" Response (Send may modify server state) ")
	b.inspectorLayout = tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(b.tabs, 1, 0, false).
		AddItem(b.table, 0, 2, true).
		AddItem(b.memberDetails, 4, 0, false)
	b.table.SetSelectionChangedFunc(func(row, column int) {
		if !b.rendering {
			b.prefill(row, false)
		}
	})
	b.table.SetSelectedFunc(func(row, column int) { b.prefill(row, true) })
	b.renderInspector()
}

func (b *browser) clearInspector() {
	if b.inspectCancel != nil {
		b.inspectCancel()
		b.inspectCancel = nil
	}
	if b.commandCancel != nil {
		b.commandCancel()
		b.commandCancel = nil
	}
	b.inspectSequence++
	b.commandSequence++
	b.inspectTarget = nil
	b.inspectLoading, b.sending = false, false
	b.info, b.inspectErr = metadata{}, nil
	if b.command != nil {
		b.command.SetText("")
		b.send.SetDisabled(false)
		b.response.SetText("")
		b.renderInspector()
	}
}

func (b *browser) inspect(n *branch, force bool) {
	if !b.ready {
		return
	}
	if b.inspectTarget == n && !force {
		return
	}
	if b.inspectCancel != nil {
		b.inspectCancel()
	}
	b.inspectSequence++
	b.inspectTarget = n
	b.inspectLoading = true
	b.info, b.inspectErr = metadata{}, nil
	b.command.SetText("")
	b.renderInspector()
	ctx, cancel := context.WithTimeout(b.ctx, 30*time.Second)
	b.inspectCancel = cancel
	generation, sequence, o := b.generation, b.inspectSequence, n.object
	b.workers.Add(1)
	go func() {
		defer b.workers.Done()
		defer cancel()
		info, err := fetchMetadata(ctx, b.client, o)
		b.post(result{kind: metadataResult, generation: generation, sequence: sequence, info: info, err: err})
	}()
}

func (b *browser) applyInspector(r result) {
	switch r.kind {
	case metadataResult:
		if r.sequence != b.inspectSequence {
			return
		}
		b.inspectLoading = false
		b.inspectCancel = nil
		b.info, b.inspectErr = r.info, r.err
		b.renderInspector()
	case commandResult:
		if r.sequence != b.commandSequence {
			return
		}
		b.sending = false
		b.commandCancel = nil
		b.send.SetDisabled(false)
		message := "Command: " + displayMultiline(r.command) + "\n"
		if r.err != nil {
			message += "ERROR: " + displayMultiline(r.err.Error()) +
				"\nA sent command may have executed; it was not replayed."
		} else {
			message += "Result:\n" + displayMultiline(r.text)
			if r.text == "" {
				message += "(empty response)"
			}
		}
		b.response.SetText(message).ScrollToBeginning()
	}
}

func displayMultiline(text string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return '\uFFFD'
		}
		return r
	}, text)
}

func (b *browser) members() ([]member, error) {
	if b.methodsTab {
		return b.info.methods, b.info.methodErr
	}
	return b.info.properties, b.info.propertyErr
}

func (b *browser) renderInspector() {
	b.rendering = true
	defer func() { b.rendering = false }()
	tab := "Properties | Methods"
	if b.methodsTab {
		tab = "Properties | [Methods]"
	} else {
		tab = "[Properties] | Methods"
	}
	b.tabs.SetText(tab + " (F2)")
	b.table.Clear()
	b.memberDetails.SetText("")
	title := " Select an object "
	if b.inspectTarget != nil {
		title = fmt.Sprintf(" %s | class %d ", b.inspectTarget.address(), b.info.class)
	}
	b.table.SetTitle(title)
	message := ""
	switch {
	case b.inspectLoading:
		message = "Loading metadata..."
	case b.inspectErr != nil:
		message = "ERROR: " + b.inspectErr.Error()
	case b.inspectTarget == nil:
		message = "No metadata loaded"
	}
	list, err := b.members()
	if message == "" && err != nil {
		message = "ERROR: " + err.Error()
	}
	if message != "" {
		b.table.SetCell(0, 0, tview.NewTableCell(tview.Escape(clean(message))).SetSelectable(false))
		b.memberDetails.SetText(displayMultiline(message))
		return
	}
	headers := []string{"Handle", "Name", "Description", "Security", "Dimension", "Datatype"}
	if b.methodsTab {
		headers[4], headers[5] = "Request", "Response"
	}
	for col, header := range headers {
		b.table.SetCell(0, col, tview.NewTableCell(header).SetSelectable(false).SetTextColor(tcell.ColorYellow))
	}
	for row, m := range list {
		for col, value := range b.memberFields(m) {
			b.table.SetCell(row+1, col, tview.NewTableCell(tview.Escape(clean(value))).SetMaxWidth(25).SetExpansion(1))
		}
	}
	if len(list) == 0 {
		b.memberDetails.SetText("No members in this list")
	}
	b.table.Select(0, 0)
}

func (b *browser) memberFields(m member) []string {
	fields := []string{strconv.FormatInt(m.handle, 10), m.name, m.description,
		strconv.FormatInt(m.security, 10), strconv.FormatInt(m.dimension, 10), m.datatype}
	if b.methodsTab {
		fields[4], fields[5] = m.request, m.response
	}
	return fields
}

func (b *browser) prefill(row int, focus bool) {
	if b.inspectTarget == nil || b.inspectLoading || b.inspectErr != nil {
		return
	}
	list, err := b.members()
	if err != nil || row < 1 || row > len(list) {
		return
	}
	m := list[row-1]
	command := b.inspectTarget.address() + "." + m.name
	if b.methodsTab {
		command += "()"
	}
	b.command.SetText(command)
	fields := b.memberFields(m)
	last := "Dimension: " + fields[4] + " | Datatype: " + fields[5]
	if b.methodsTab {
		last = "Request: " + fields[4] + " | Response: " + fields[5]
	}
	b.memberDetails.SetText(fmt.Sprintf("Handle: %s | Name: %s | Security: %s\n%s\n%s",
		fields[0], clean(fields[1]), fields[3], clean(last), displayMultiline(m.description)))
	if focus {
		b.app.SetFocus(b.command)
	}
}

func (b *browser) switchTab() {
	b.methodsTab = !b.methodsTab
	b.renderInspector()
}

func (b *browser) cycleFocus(back bool) {
	items := []tview.Primitive{b.tree, b.table, b.command, b.send, b.response}
	index := 0
	if b.memberDetails.HasFocus() {
		index = 1
	}
	for i, item := range items {
		if item.HasFocus() {
			index = i
			break
		}
	}
	step := 1
	if back {
		step = len(items) - 1
	}
	next := items[(index+step)%len(items)]
	b.app.SetFocus(next)
	if next == b.table {
		row, _ := b.table.GetSelection()
		b.prefill(row, false)
	}
}

func (b *browser) submitCommand() {
	if b.sending {
		return
	}
	if !b.ready {
		b.response.SetText("Not connected; use R in the tree to connect")
		return
	}
	command := b.command.GetText()
	if strings.TrimSpace(command) == "" {
		b.response.SetText("Enter a command before Send")
		return
	}
	b.commandSequence++
	sequence, generation := b.commandSequence, b.generation
	ctx, cancel := context.WithTimeout(b.ctx, 30*time.Second)
	b.commandCancel = cancel
	b.sending = true
	b.send.SetDisabled(true)
	b.response.SetText("Sending: " + displayMultiline(command))
	b.workers.Add(1)
	go func() {
		defer b.workers.Done()
		defer cancel()
		text, err := b.client.AskTxtContext(ctx, command)
		b.post(result{kind: commandResult, generation: generation, sequence: sequence,
			command: command, text: text, err: err})
	}()
}
