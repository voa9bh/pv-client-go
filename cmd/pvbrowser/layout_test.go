package main

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"
)

func TestCompactAndWideLayout(t *testing.T) {
	for _, size := range [][2]int{{80, 24}, {140, 40}} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			ui := startBrowserSize(t, editorClient(func(context.Context, string) (string, error) { return "", nil }), size[0], size[1])
			s := ui.wait(t, func(s snapshot) bool { return s.metadataLoaded && strings.Contains(s.rendered, "set()") })
			for _, label := range []string{"[Objects]", "Macros", "Properties / Methods", "Input / Output", "Selected object", "g get, s set, c call"} {
				if !strings.Contains(s.rendered, label) {
					t.Fatalf("missing %q:\n%s", label, s.rendered)
				}
			}
			if strings.Contains(s.rendered, "diagnostics") {
				t.Fatal("diagnostics still occupies the layout")
			}
			if strings.Contains(s.details, "(#1)") {
				t.Fatal("invented root ID")
			}
			ui.key(tcell.KeyTab, 0)
			ui.wait(t, func(s snapshot) bool {
				return strings.Contains(s.details, "PropertyHandle: -1") &&
					strings.Contains(s.details, "Datatype: t") && strings.Contains(s.details, "A property")
			})
			ui.key(tcell.KeyBacktab, 0)
			ui.wait(t, func(s snapshot) bool { return strings.Contains(s.details, "Name: pvServer") })
		})
	}
}

func TestEmptyOutputAndMemberChangeRejectsOldReply(t *testing.T) {
	b := newBrowser(context.Background(), &fakeClient{}, "test:5002", new(logBuffer))
	defer b.cancel()
	b.inspectTarget = b.root
	b.info = metadata{properties: []member{{handle: 1, name: "value"}}, methods: []member{{handle: 2, name: "get"}}}
	b.renderInspector()
	b.selectMember(1)
	b.commandSequence = 10
	b.apply(result{kind: commandResult, generation: b.generation, sequence: 10, command: "/pvServer.value"})
	if !strings.Contains(b.response.GetText(false), "(empty response)") {
		t.Fatal("empty result hidden")
	}
	b.selectMember(3)
	b.apply(result{kind: commandResult, generation: b.generation, sequence: 10, text: "old reply"})
	if strings.Contains(b.response.GetText(false), "old reply") {
		t.Fatal("old member result applied")
	}
}
