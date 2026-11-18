package main

import (
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

type actionBar struct {
	*tview.Flex
	buttons []*tview.Button
	width   int
}

func newActionBar() *actionBar {
	return &actionBar{Flex: tview.NewFlex().SetDirection(tview.FlexRow)}
}

func (a *actionBar) rows(width int) int {
	rows, used := 1, 0
	for _, button := range a.buttons {
		size := tview.TaggedStringWidth(button.GetLabel()) + 4
		if used > 0 && used+size > width {
			rows++
			used = 0
		}
		used += size
	}
	return rows
}

func (a *actionBar) Draw(screen tcell.Screen) {
	_, _, width, _ := a.GetRect()
	if width != a.width {
		a.width = width
		a.Clear()
		row := tview.NewFlex()
		used := 0
		for _, button := range a.buttons {
			size := tview.TaggedStringWidth(button.GetLabel()) + 4
			if used > 0 && used+size > width {
				a.AddItem(row, 1, 0, false)
				row, used = tview.NewFlex(), 0
			}
			row.AddItem(button, size, 0, false)
			used += size
		}
		a.AddItem(row, 1, 0, false)
	}
	a.Flex.Draw(screen)
}

func (b *browser) quit() {
	b.stopMacro("Quit requested")
	b.cancel()
	b.app.Stop()
}

func (b *browser) editing() bool {
	return b.command.HasFocus() || b.macroEditor.HasFocus()
}

func (b *browser) switchWorkspace() {
	b.cancelEdit()
	b.macros = !b.macros
	if b.macros {
		b.tabs.SetText("Objects | [Macros] (F2)")
		b.pages.SwitchToPage("macros")
		b.app.SetFocus(b.macroEditor)
	} else {
		b.tabs.SetText("[Objects] | Macros (F2)")
		b.pages.SwitchToPage("objects")
		b.app.SetFocus(b.tree)
	}
	b.updateActions()
	b.updateDetails()
}

func (b *browser) refreshSelected() {
	n := b.selected()
	b.invalidate(n)
	n.state = unloaded
	b.load(n)
	b.inspect(n, true)
}

func (b *browser) updateActions() {
	b.bar.buttons = nil
	add := func(label string, action func()) {
		b.bar.buttons = append(b.bar.buttons, tview.NewButton(label).SetSelectedFunc(func() {
			if !b.macroDialog {
				action()
			}
		}))
	}
	if b.macros {
		add("F5 Run", b.runMacro)
		add("F6 Stop", func() { b.stopMacro("Stop requested") })
		add("^O Load", func() { b.promptMacroFile(false) })
		add("^S Save", func() { b.promptMacroFile(true) })
	} else {
		add("r Refresh", b.refreshSelected)
		add("g Get", func() { b.selectedMemberAction('g') })
		add("s Set", func() { b.selectedMemberAction('s') })
		add("Enter Call", func() { b.selectedMemberAction('c') })
		add("G Globals", b.toggleGlobals)
	}
	add("F2 Switch", b.switchWorkspace)
	add("P Port", b.promptPort)
	add("R Reconnect", func() { b.connect(true) })
	add("Q Quit", b.quit)
	b.bar.width = -1
}

func (b *browser) selectedMemberAction(action rune) {
	// Buttons act on the selected row even though they hold keyboard focus.
	b.app.SetFocus(b.table)
	b.memberAction(action)
}

func (b *browser) focusItems() []tview.Primitive {
	var items []tview.Primitive
	if b.macros {
		items = []tview.Primitive{b.macroEditor, b.macroOutput}
	} else {
		io := tview.Primitive(b.response)
		if b.edit != nil {
			io = b.command
		}
		items = []tview.Primitive{b.tree, b.table, io, b.commandDisplay, b.details}
	}
	for _, button := range b.bar.buttons {
		items = append(items, button)
	}
	return items
}
