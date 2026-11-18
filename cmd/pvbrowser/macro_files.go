package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

type macroFileEvent struct {
	path string
	text string
	save bool
	err  error
}

func (b *browser) showMacroDialog(content tview.Primitive) {
	if !b.macroDialog {
		b.macroReturn = b.app.GetFocus()
	}
	b.macroDialog = true
	center := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(nil, 0, 1, false).
		AddItem(content, 10, 0, true).
		AddItem(nil, 0, 1, false)
	overlay := tview.NewFlex().AddItem(nil, 0, 1, false).
		AddItem(center, 0, 8, true).AddItem(nil, 0, 1, false)
	b.pages.AddPage("macro-dialog", overlay, true, true)
	b.app.SetFocus(content)
}

func (b *browser) closeMacroDialog() {
	b.pages.RemovePage("macro-dialog")
	b.macroDialog = false
	b.app.SetFocus(b.macroReturn)
	b.macroReturn = nil
}

func (b *browser) confirmMacro(message string, confirmed func()) {
	dialog := tview.NewModal().SetText(tview.Escape(message)).AddButtons([]string{"Cancel", "Replace"})
	dialog.SetDoneFunc(func(index int, label string) {
		b.closeMacroDialog()
		if index == 1 {
			confirmed()
		} else {
			b.setStatus("Macro file operation canceled")
		}
	})
	b.showMacroDialog(dialog)
}

func (b *browser) promptMacroFile(save bool) {
	if b.macroFileBusy {
		b.setStatus("A macro file operation is already pending")
		return
	}
	action := "Load"
	if save {
		action = "Save"
	}
	path := tview.NewInputField().SetLabel("Path: ").SetText(b.macroPath)
	form := tview.NewForm().AddFormItem(path)
	form.SetBorder(true).SetTitle(" " + action + " macro ")
	cancel := func() {
		b.closeMacroDialog()
		b.setStatus("Macro file operation canceled")
	}
	submit := func() {
		name := path.GetText()
		if strings.TrimSpace(name) == "" {
			b.setStatus("A macro file path is required")
			return
		}
		b.closeMacroDialog()
		b.startMacroFile(macroFileEvent{path: name, text: b.macroEditor.GetText(), save: save}, false)
	}
	form.AddButton(action, submit).AddButton("Cancel", cancel).SetCancelFunc(cancel)
	// Enter in the path field submits directly; Tab still reaches the buttons.
	path.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		switch event.Key() {
		case tcell.KeyEnter:
			submit()
			return nil
		case tcell.KeyEscape:
			cancel()
			return nil
		}
		return event
	})
	b.showMacroDialog(form)
}

func (b *browser) startMacroFile(event macroFileEvent, overwrite bool) {
	b.macroFileBusy = true
	b.setStatus("Macro file operation pending: " + event.path)
	b.workers.Add(1)
	go func() {
		defer b.workers.Done()
		if event.save {
			event.err = saveMacroFile(event.path, event.text, overwrite)
		} else {
			event.text, event.err = loadMacroFile(event.path)
		}
		b.post(result{kind: macroFileResult, file: event})
	}()
}

func loadMacroFile(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("macro %s is not a regular file", path)
	}
	data, err := os.ReadFile(path)
	return string(data), err
}

func (b *browser) applyMacroFile(event macroFileEvent) {
	b.macroFileBusy = false
	if event.save && errors.Is(event.err, os.ErrExist) {
		confirm := func() {
			event.err = nil
			b.startMacroFile(event, true)
		}
		b.confirmMacro("Replace the existing file?\n"+singleLine(event.path), confirm)
		return
	}
	if event.err != nil {
		b.setStatus("Macro file failed: " + event.err.Error())
		return
	}
	if event.save {
		b.macroPath = event.path
		b.setStatus("Macro saved: " + event.path)
		return
	}
	load := func() {
		b.macroEditor.SetText(event.text, false)
		b.macroPath = event.path
		b.setStatus("Macro loaded: " + event.path)
	}
	if b.macroEditor.GetText() != "" {
		b.confirmMacro("Replace the current macro editor contents?", load)
	} else {
		load()
	}
}

// Publish only complete files; linking a new destination never overwrites a
// file created after the save started. Replacement requires explicit approval.
func saveMacroFile(path, text string, overwrite bool) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".pvbrowser-macro-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	_, writeErr := file.WriteString(text)
	closeErr := file.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		return fmt.Errorf("write macro %s: %w", path, err)
	}
	if overwrite {
		return os.Rename(file.Name(), path)
	}
	return os.Link(file.Name(), path)
}
