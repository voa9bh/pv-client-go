package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

type macroWorkspace struct {
	macroEditor   *tview.TextArea
	macroOutput   *tview.TextView
	macroLayout   *tview.Flex
	macroSequence uint64
	macroCancel   context.CancelFunc
	macroRunning  bool
	macroStopping bool
	macroPath     string
	macroFileBusy bool
	macroDialog   bool
	macroReturn   tview.Primitive
}

type macroEvent struct {
	line      int
	command   string
	text      string
	err       error
	done      bool
	stopped   bool
	completed int
	errors    int
}

func (b *browser) initMacros() {
	b.macroEditor = tview.NewTextArea().SetWrap(false)
	b.macroEditor.SetBorder(true).SetTitle(" Macro editor ")
	b.macroOutput = tview.NewTextView().SetScrollable(true).SetWrap(false)
	b.macroOutput.SetBorder(true).SetTitle(" Macro output ")
	panes := tview.NewFlex().
		AddItem(b.macroEditor, 0, 1, true).
		AddItem(b.macroOutput, 0, 1, false)
	b.macroLayout = tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(panes, 0, 1, true)
}

func (b *browser) appendMacro(text string) {
	fmt.Fprintln(b.macroOutput, displayMultiline(text))
	b.macroOutput.ScrollToEnd()
}

func (b *browser) runMacro() {
	if b.macroRunning {
		b.setStatus("A macro is already running")
		return
	}
	if !b.ready {
		b.setStatus("Not ready; R in the Objects tree connects/reconnects")
		return
	}
	text := b.macroEditor.GetText()
	if strings.TrimSpace(text) == "" {
		b.setStatus("Macro has no commands")
		return
	}
	b.macroSequence++
	sequence := b.macroSequence
	client := b.client
	ctx, cancel := context.WithCancel(b.ctx)
	b.macroCancel = cancel
	b.macroRunning, b.macroStopping = true, false
	b.macroOutput.SetText("")
	b.appendMacro("Running macro...")
	b.setStatus("Macro running; F6 stops the local wait")
	b.workers.Add(1)
	go func() {
		defer b.workers.Done()
		defer cancel()
		completed, failures := 0, 0
		for i, line := range strings.Split(text, "\n") {
			line = strings.TrimSuffix(line, "\r")
			if ctx.Err() != nil {
				break
			}
			if strings.TrimSpace(line) == "" {
				continue
			}
			callCtx, callCancel := context.WithTimeout(ctx, 30*time.Second)
			reply, err := client.AskTxtContext(callCtx, line)
			callCancel()
			completed++
			if err != nil {
				failures++
			}
			b.post(result{kind: macroResult, sequence: sequence, macro: macroEvent{
				line: i + 1, command: line, text: reply, err: err,
			}})
		}
		b.post(result{kind: macroResult, sequence: sequence, macro: macroEvent{
			done: true, stopped: ctx.Err() != nil, completed: completed, errors: failures,
		}})
	}()
}

func (b *browser) stopMacro(reason string) {
	if !b.macroRunning || b.macroStopping {
		return
	}
	b.macroStopping = true
	b.macroCancel()
	message := reason + "; a sent operation may already have executed. It was not replayed."
	b.appendMacro(message)
	b.setStatus(message)
}

func (b *browser) applyMacro(r result) {
	if r.sequence != b.macroSequence {
		return
	}
	event := r.macro
	if event.done {
		stopped := event.stopped || b.macroStopping
		b.macroRunning, b.macroStopping = false, false
		b.macroCancel = nil
		message := fmt.Sprintf("Macro completed: %d commands, %d errors", event.completed, event.errors)
		if stopped {
			message = fmt.Sprintf("Macro stopped: %d commands returned, %d errors; no further commands will be sent", event.completed, event.errors)
		}
		b.appendMacro(message)
		b.setStatus(message)
		return
	}
	b.appendMacro(fmt.Sprintf("Line %d > %s", event.line, event.command))
	switch {
	case event.err != nil:
		b.appendMacro("ERROR: " + event.err.Error() + " | The operation may have executed; it was not replayed.")
	case event.text == "":
		b.appendMacro("<ready>")
	default:
		b.appendMacro(event.text)
	}
}

func (b *browser) macroInput(event *tcell.EventKey) *tcell.EventKey {
	switch event.Key() {
	case tcell.KeyF5:
		b.runMacro()
	case tcell.KeyF6:
		b.stopMacro("Stop requested")
	case tcell.KeyCtrlO:
		b.promptMacroFile(false)
	case tcell.KeyCtrlS:
		b.promptMacroFile(true)
	default:
		return event
	}
	return nil
}
