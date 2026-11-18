package main

import (
	"context"
	"fmt"
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

type memberSelection struct {
	member
	method  bool
	address string
}

func (s memberSelection) expression(value string, read bool) string {
	target := s.address + "." + s.name
	if s.method {
		return target + "(" + value + ")"
	}
	if read {
		return target
	}
	if value == "" {
		value = `""`
	}
	return target + "=" + value
}

type inspector struct {
	inspectorLayout *tview.Flex
	table           *tview.Table
	command         *tview.InputField
	response        *tview.TextView
	io              *tview.Pages
	commandDisplay  *tview.TextView
	showGlobals     bool
	inspectTarget   *branch
	inspectSequence uint64
	inspectCancel   context.CancelFunc
	info            metadata
	inspectErr      error
	inspectLoading  bool
	rendering       bool
	selection       *memberSelection
	edit            *memberSelection
	commandSequence uint64
	commandCancel   context.CancelFunc
	sending         bool
}

func (b *browser) initInspector() {
	b.table = tview.NewTable().SetSelectable(true, false).SetFixed(1, 0)
	b.table.SetBorder(true).SetTitle(" Properties / Methods ")
	b.command = tview.NewInputField()
	b.command.SetDoneFunc(func(key tcell.Key) {
		switch key {
		case tcell.KeyEscape:
			b.cancelEdit()
			b.app.SetFocus(b.table)
		case tcell.KeyEnter:
			if b.edit == nil {
				return
			}
			command := b.edit.expression(b.command.GetText(), false)
			b.cancelEdit()
			b.app.SetFocus(b.table)
			b.submit(command)
		}
	})
	b.response = tview.NewTextView().SetScrollable(true).SetWrap(false)
	b.commandDisplay = tview.NewTextView().SetScrollable(true).SetWrap(false).SetText("Command: ")
	b.response.SetText("Result: ")
	b.io = tview.NewPages().AddPage("output", b.commandDisplay, true, true).AddPage("input", b.command, true, false)
	ioLayout := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(b.io, 1, 0, false).AddItem(b.response, 1, 0, false)
	ioLayout.SetBorder(true).SetTitle(" Input / Output ")
	b.inspectorLayout = tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(b.table, 0, 1, true).AddItem(ioLayout, 4, 0, false)
	b.table.SetSelectionChangedFunc(func(row, column int) {
		if !b.rendering {
			b.selectMember(row)
		}
	})
	b.renderInspector()
}

func (b *browser) cancelEdit() {
	b.edit = nil
	b.command.SetText("")
	b.io.SwitchToPage("output")
}

func (b *browser) invalidateCommand() {
	pending := b.sending
	if b.commandCancel != nil {
		b.commandCancel()
		b.commandCancel = nil
	}
	b.commandSequence++
	b.sending = false
	b.cancelEdit()
	b.response.SetText("Result: ")
	b.commandDisplay.SetText("Command: ")
	if pending {
		b.response.SetText("Result: local wait canceled; the sent operation may already have executed. It was not replayed.")
	}
}

func (b *browser) clearInspector() {
	if b.inspectCancel != nil {
		b.inspectCancel()
		b.inspectCancel = nil
	}
	b.inspectSequence++
	b.inspectTarget = nil
	b.inspectLoading = false
	b.info, b.inspectErr = metadata{}, nil
	b.selection = nil
	if b.command != nil {
		b.invalidateCommand()
		b.renderInspector()
	}
}

func (b *browser) inspect(n *branch, force bool) {
	if !b.ready || (b.inspectTarget == n && !force) {
		return
	}
	if b.inspectCancel != nil {
		b.inspectCancel()
	}
	b.inspectSequence++
	b.inspectTarget = n
	b.inspectLoading = true
	b.info, b.inspectErr = metadata{}, nil
	b.selection = nil
	b.invalidateCommand()
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
		b.commandDisplay.SetText("Command: " + singleLine(r.command)).ScrollToBeginning()
		message := "Result: "
		if r.err != nil {
			message += "ERROR: " + singleLine(r.err.Error()) + " | The operation may have executed; it was not replayed."
		} else {
			message += singleLine(r.text)
			if r.text == "" {
				message += "<ready>"
			}
		}
		b.response.SetText(message).ScrollToBeginning()
	}
}

func singleLine(text string) string {
	text = strings.NewReplacer("\n", `\n`, "\r", `\r`, "\t", `\t`).Replace(text)
	return clean(text)
}

func displayMultiline(text string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return '\uFFFD'
		}
		return r
	}, text)
}

func (b *browser) renderInspector() {
	b.rendering = true
	defer func() { b.rendering = false }()
	b.table.Clear()
	title := " Properties / Methods | positive only (G: globals) "
	if b.showGlobals {
		title = " Properties / Methods | including globals (G) "
	}
	b.table.SetTitle(title)
	b.table.SetCell(0, 0, tview.NewTableCell("Name").SetSelectable(false).SetTextColor(tcell.ColorYellow))
	message := ""
	switch {
	case b.inspectLoading:
		message = "Loading metadata..."
	case b.inspectErr != nil:
		message = "ERROR: " + b.inspectErr.Error()
	case b.inspectTarget == nil:
		message = "No metadata loaded"
	}
	row := 1
	addStatus := func(text string) {
		b.table.SetCell(row, 0, tview.NewTableCell(tview.Escape(clean(text))).SetSelectable(false))
		row++
	}
	if message != "" {
		addStatus(message)
		b.updateDetails()
		return
	}
	for _, methods := range []bool{false, true} {
		list, err := b.info.properties, b.info.propertyErr
		label := "properties"
		if methods {
			addStatus("------------------------------")
			list, err, label = b.info.methods, b.info.methodErr, "methods"
		}
		if err != nil {
			addStatus("ERROR (" + label + "): " + err.Error())
			continue
		}
		visible := 0
		for _, m := range list {
			if m.handle == 0 || (m.handle < 0 && !b.showGlobals) {
				continue
			}
			visible++
			s := memberSelection{member: m, method: methods, address: b.inspectTarget.address()}
			name := m.name
			if methods {
				name += "()"
			}
			b.table.SetCell(row, 0, tview.NewTableCell(tview.Escape(clean(name))).SetReference(s).SetExpansion(1))
			row++
		}
		if visible == 0 {
			addStatus("(no visible " + label + ")")
		}
	}
	b.table.Select(0, 0)
	if b.selection != nil {
		for i := 1; i < row; i++ {
			if s, ok := b.table.GetCell(i, 0).GetReference().(memberSelection); ok && s == *b.selection {
				b.table.Select(i, 0)
				break
			}
		}
	}
	b.updateDetails()
}

func (b *browser) selectMember(row int) {
	s, ok := b.table.GetCell(row, 0).GetReference().(memberSelection)
	if !ok {
		return
	}
	if b.selection == nil || *b.selection != s {
		b.invalidateCommand()
		b.selection = &s
	}
	b.updateDetails()
}

func (b *browser) updateDetails() {
	n := b.selected()
	if n == nil {
		return
	}
	b.details.SetTitle(" Selected object ")
	class := "not loaded"
	if b.inspectTarget == n {
		class = fmt.Sprint(b.info.class)
		if b.inspectLoading {
			class = "loading..."
		}
		if b.inspectErr != nil {
			class = "ERROR: " + b.inspectErr.Error()
		}
	}
	if !b.tree.HasFocus() && b.selection != nil {
		s := b.selection
		kind, formats := "Property", fmt.Sprintf("Dimension: %d | Datatype: %s", s.dimension, s.datatype)
		if s.method {
			kind, formats = "Method", "Request: "+s.request+" | Response: "+s.response
		}
		b.details.SetTitle(" Selected " + kind + " ")
		b.details.SetText(fmt.Sprintf("Name: %s.%s | %sHandle: %d | ClassHandle: %s\nSecurity: %d | %s\nDescription: %s",
			s.address, clean(s.name), kind, s.handle, clean(class), s.security, clean(formats), displayMultiline(s.description)))
		return
	}
	b.details.SetText(fmt.Sprintf("Name: %s (%s) | ClassHandle: %s\nPath: %s\nDescription: unavailable",
		clean(n.name), n.address(), clean(class), clean(n.path)))
}

func (b *browser) cycleFocus(back bool) {
	if b.macros {
		return
	}
	io := tview.Primitive(b.response)
	if b.edit != nil {
		io = b.command
	}
	items := []tview.Primitive{b.tree, b.table, io, b.commandDisplay, b.details}
	index := 0
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
	b.app.SetFocus(items[(index+step)%len(items)])
	if b.table.HasFocus() {
		row, _ := b.table.GetSelection()
		b.selectMember(row)
	}
	b.updateDetails()
}

func (b *browser) memberAction(action rune) {
	if b.macros || !b.table.HasFocus() {
		b.setStatus("Select a property/method in the member list first")
		return
	}
	row, _ := b.table.GetSelection()
	if _, ok := b.table.GetCell(row, 0).GetReference().(memberSelection); !ok {
		b.setStatus("No member selected")
		return
	}
	b.selectMember(row)
	if b.selection == nil {
		b.setStatus("No member selected")
		return
	}
	if b.sending {
		b.setStatus("An operation is already pending")
		return
	}
	s := *b.selection
	switch {
	case action == 'g' && s.method:
		b.setStatus("g reads properties only; use Enter to call a method")
		return
	case action != 'g' && action != '\n':
		b.setStatus("Unknown member action")
		return
	}
	if action == 'g' {
		b.cancelEdit()
		b.submit(s.expression("", true))
		return
	}
	b.edit = &s
	label := "Command: " + s.address + "." + singleLine(s.name) + "="
	if s.method {
		label = "Command: " + s.address + "." + singleLine(s.name) + "("
	}
	b.command.SetLabel(tview.Escape(label)).SetText("")
	b.io.SwitchToPage("input")
	b.app.SetFocus(b.command)
	b.updateDetails()
}

func (b *browser) submit(command string) {
	if b.sending {
		return
	}
	if !b.ready {
		b.response.SetText("Result: not connected; R in the tree reconnects")
		return
	}
	b.commandSequence++
	sequence, generation := b.commandSequence, b.generation
	ctx, cancel := context.WithTimeout(b.ctx, 30*time.Second)
	b.commandCancel = cancel
	b.sending = true
	b.commandDisplay.SetText("Command: " + singleLine(command)).ScrollToBeginning()
	b.response.SetText("Result: pending...")
	b.workers.Add(1)
	go func() {
		defer b.workers.Done()
		defer cancel()
		text, err := b.client.AskTxtContext(ctx, command)
		b.post(result{kind: commandResult, generation: generation, sequence: sequence, command: command, text: text, err: err})
	}()
}

func (b *browser) toggleGlobals() {
	b.showGlobals = !b.showGlobals
	if b.selection != nil && b.selection.handle < 0 && !b.showGlobals {
		b.invalidateCommand()
		b.selection = nil
	}
	b.renderInspector()
	row, _ := b.table.GetSelection()
	if _, ok := b.table.GetCell(row, 0).GetReference().(memberSelection); !ok {
		for i := 1; i < b.table.GetRowCount(); i++ {
			if _, ok := b.table.GetCell(i, 0).GetReference().(memberSelection); ok {
				b.table.Select(i, 0)
				break
			}
		}
	}
	row, _ = b.table.GetSelection()
	b.selectMember(row)
}
