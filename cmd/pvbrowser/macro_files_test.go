package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

func TestMacroFileRoundTripAndSafeReplacement(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.macro")
	text := " first()\r\n\nsecond(\"value\")\n"
	if err := saveMacroFile(path, text, false); err != nil {
		t.Fatal(err)
	}
	if err := saveMacroFile(path, "replacement", false); !errors.Is(err, os.ErrExist) {
		t.Fatalf("unconfirmed overwrite: %v", err)
	}
	got, err := loadMacroFile(path)
	if err != nil || got != text {
		t.Fatalf("round trip: %q, %v", got, err)
	}
	if err := saveMacroFile(path, "replacement", true); err != nil {
		t.Fatal(err)
	}
	got, err = loadMacroFile(path)
	if err != nil || got != "replacement" {
		t.Fatalf("confirmed replacement: %q, %v", got, err)
	}
	if _, err := loadMacroFile(dir); err == nil {
		t.Fatal("loaded a directory")
	}
	if _, err := loadMacroFile(filepath.Join(dir, "missing")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing file: %v", err)
	}
	if err := saveMacroFile(filepath.Join(dir, "missing", "macro"), text, false); err == nil {
		t.Fatal("saved into nonexistent directory")
	}
	if err := saveMacroFile(dir, text, true); err == nil {
		t.Fatal("replaced a directory")
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 || entries[0].Name() != "test.macro" {
		t.Fatalf("temporary files remain: %v, %v", entries, err)
	}
}

func applyFileResult(t *testing.T, b *browser) {
	t.Helper()
	select {
	case r := <-b.results:
		if r.kind != macroFileResult {
			t.Fatalf("unexpected result: %v", r.kind)
		}
		b.apply(r)
	case <-time.After(time.Second):
		t.Fatal("file operation did not finish")
	}
}

func dialogKey(b *browser, key tcell.Key) {
	event := tcell.NewEventKey(key, 0, tcell.ModNone)
	if event = b.input(event); event != nil {
		b.app.GetFocus().InputHandler()(event, func(p tview.Primitive) { b.app.SetFocus(p) })
	}
}

func TestMacroFilePromptsAndConfirmations(t *testing.T) {
	b := macroBrowser(t, func(context.Context, string) (string, error) {
		t.Error("file operation executed a command")
		return "", nil
	})
	b.macros = true
	b.app.SetFocus(b.macroEditor)
	b.macroEditor.SetText("original", false)
	path := filepath.Join(t.TempDir(), "macro.txt")
	b.promptMacroFile(true)
	dialogKey(b, tcell.KeyEnter)
	if !b.macroDialog || !strings.Contains(b.status.GetText(false), "path is required") {
		t.Fatal("empty path was accepted")
	}
	b.app.GetFocus().(*tview.InputField).SetText(path)
	dialogKey(b, tcell.KeyEnter)
	applyFileResult(t, b)
	if b.macroDialog || b.macroFileBusy || !b.macroEditor.HasFocus() || b.macroPath != path {
		t.Fatal("save did not restore focus or remember path")
	}
	b.macroEditor.SetText("replacement", false)
	b.promptMacroFile(true)
	if got := b.app.GetFocus().(*tview.InputField).GetText(); got != path {
		t.Fatal("save path not retained")
	}
	dialogKey(b, tcell.KeyEnter)
	applyFileResult(t, b)
	if !b.macroDialog {
		t.Fatal("existing file was replaced without confirmation")
	}
	dialogKey(b, tcell.KeyEnter) // Cancel is the safe default.
	if got, _ := loadMacroFile(path); got != "original" {
		t.Fatal("cancel overwrote the file")
	}
	b.promptMacroFile(true)
	dialogKey(b, tcell.KeyEnter)
	applyFileResult(t, b)
	dialogKey(b, tcell.KeyTab)
	dialogKey(b, tcell.KeyEnter)
	applyFileResult(t, b)
	if got, _ := loadMacroFile(path); got != "replacement" {
		t.Fatal("confirmed save did not replace file")
	}
	b.macroEditor.SetText("unsaved", false)
	b.promptMacroFile(false)
	dialogKey(b, tcell.KeyEnter)
	applyFileResult(t, b)
	if !b.macroDialog || b.macroEditor.GetText() != "unsaved" {
		t.Fatal("load replaced editor without confirmation")
	}
	dialogKey(b, tcell.KeyEscape)
	if b.macroEditor.GetText() != "unsaved" || !b.macroEditor.HasFocus() {
		t.Fatal("canceling load changed the editor or focus")
	}
	b.promptMacroFile(false)
	dialogKey(b, tcell.KeyEnter)
	applyFileResult(t, b)
	dialogKey(b, tcell.KeyTab)
	dialogKey(b, tcell.KeyEnter)
	if b.macroEditor.GetText() != "replacement" || !b.macroEditor.HasFocus() {
		t.Fatal("confirmed load failed")
	}
	b.macroEditor.SetText("", false)
	b.promptMacroFile(false)
	dialogKey(b, tcell.KeyEnter)
	applyFileResult(t, b)
	if b.macroDialog || b.macroEditor.GetText() != "replacement" {
		t.Fatal("loading empty editor unexpectedly required confirmation")
	}
	b.promptMacroFile(false)
	dialogKey(b, tcell.KeyEscape)
	if b.macroDialog || !b.macroEditor.HasFocus() {
		t.Fatal("path cancel did not dismiss dialog")
	}
}

func TestMacroFileErrorsPreserveEditor(t *testing.T) {
	b := macroBrowser(t, nil)
	b.macroEditor.SetText("keep me", false)
	for _, save := range []bool{false, true} {
		b.startMacroFile(macroFileEvent{path: filepath.Join(t.TempDir(), "missing", "macro"), text: "keep me", save: save}, false)
		// A duplicate prompt must not start another operation.
		b.promptMacroFile(save)
		if b.macroDialog {
			t.Fatal("opened prompt while file operation was pending")
		}
		applyFileResult(t, b)
		if b.macroFileBusy || b.macroEditor.GetText() != "keep me" ||
			!strings.Contains(b.status.GetText(false), "Macro file failed") {
			t.Fatal("file error was hidden or editor changed")
		}
	}
}

func TestMacroFileDialogKeyboardRouting(t *testing.T) {
	ui := startBrowser(t, editorClient(func(context.Context, string) (string, error) {
		t.Error("dialog executed macro")
		return "", nil
	}))
	ui.wait(t, func(s snapshot) bool { return s.metadataLoaded })
	ui.key(tcell.KeyF2, 0)
	ui.key(tcell.KeyCtrlS, 0)
	ui.wait(t, func(s snapshot) bool { return s.macroDialog && strings.Contains(s.rendered, "Save macro") })
	ui.key(tcell.KeyF2, 0)
	ui.key(tcell.KeyF5, 0)
	ui.key(tcell.KeyTab, 0)
	ui.key(tcell.KeyEscape, 0)
	ui.wait(t, func(s snapshot) bool {
		return !s.macroDialog && s.macros && s.macroEditorFocus && !s.macroRunning
	})
	ui.key(tcell.KeyCtrlO, 0)
	ui.wait(t, func(s snapshot) bool { return s.macroDialog && strings.Contains(s.rendered, "Load macro") })
	ui.key(tcell.KeyEscape, 0)
	ui.wait(t, func(s snapshot) bool { return !s.macroDialog && s.macroEditorFocus })
}
