package main

import (
	"path/filepath"
	"strings"
	"testing"

	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
)

func TestFindUsesRawPreviewWhenSyntaxHighlightingIsPresent(t *testing.T) {
	source := "package main\n\nfunc main() {}\n"
	rendered := renderPreview("main.go", source)
	if rendered == source {
		t.Fatal("expected syntax-highlighted preview")
	}
	v := viewport.New(viewport.WithWidth(80), viewport.WithHeight(1))
	v.SetContent(rendered)
	m := model{preview: source, renderedPreview: rendered, viewport: v, finding: true}
	m.find.SetValue("main")
	m.applyFind()
	if got := m.viewport.GetContent(); !strings.Contains(got, "\x1b[38;2;") || stripANSI(got) != source {
		t.Fatalf("find content lost syntax highlighting: %q", got)
	}
	// The second match is on a later line.
	m.nextFind(1)
	if m.viewport.YOffset() == 0 && strings.Count(source, "main") > 1 {
		t.Fatal("next match did not navigate away from the first occurrence")
	}
}

func TestFindShortcutAcceptsTypedQuery(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "main.go")
	source := "package main\n\nfunc main() {}\n"
	rendered := renderPreview(path, source)
	m, err := newModel(root)
	if err != nil {
		t.Fatal(err)
	}
	if m.watcher != nil {
		defer m.watcher.close()
	}
	node := &Node{Name: "main.go", Path: path, Rel: "main.go"}
	m.tree.Children = []*Node{node}
	node.Parent = m.tree
	m.rebuildRows()
	m.selected, m.preview, m.previewPath, m.renderedPreview = path, source, path, rendered
	m.viewport.SetContent(rendered)

	updated, _ := m.Update(tea.KeyPressMsg{Code: '/', Text: "/"})
	m = updated.(model)
	for _, r := range "main" {
		updated, _ = m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
		m = updated.(model)
	}
	if got := m.find.Value(); got != "main" {
		t.Fatalf("find value = %q, want main", got)
	}
	if got := m.viewport.GetContent(); !strings.Contains(got, "\x1b[38;2;") || stripANSI(got) != source {
		t.Fatalf("find content lost syntax highlighting: %q", got)
	}
}

func TestFindLoadsInitiallySelectedFileBeforeMatching(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "main.go")
	writeFile(t, path, "package main\n")
	m, err := newModel(root)
	if err != nil {
		t.Fatal(err)
	}
	if m.watcher != nil {
		defer m.watcher.close()
	}
	node := &Node{Name: "main.go", Path: path, Rel: "main.go"}
	m.tree.Children = []*Node{node}
	node.Parent = m.tree
	m.rebuildRows()
	m.selected = path

	updated, cmd := m.Update(tea.KeyPressMsg{Code: '/', Text: "/"})
	m = updated.(model)
	if cmd == nil {
		t.Fatal("find did not request the selected file preview")
	}
	if m.previewPath != path || m.preview != "Loading preview…" {
		t.Fatalf("preview request = (%q, %q), want selected path and loading state", m.previewPath, m.preview)
	}
}

func TestFindRangesIgnoreCaseAndTreatInputLiterally(t *testing.T) {
	source := "Main main MAIN [main]"
	ranges := findRanges(source, "mAiN")
	if len(ranges) != 4 {
		t.Fatalf("matches = %v, want four case-insensitive matches", ranges)
	}
	for _, r := range ranges {
		if !strings.EqualFold(source[r[0]:r[1]], "main") {
			t.Fatalf("match %q is not main", source[r[0]:r[1]])
		}
	}
	if ranges := findRanges(source, "[main]"); len(ranges) != 1 || source[ranges[0][0]:ranges[0][1]] != "[main]" {
		t.Fatalf("literal bracket query matched incorrectly: %v", ranges)
	}
}

func TestReopeningFindClearsThePreviousQueryAndHighlights(t *testing.T) {
	source := "package main\n"
	rendered := renderPreview("main.go", source)
	v := viewport.New(viewport.WithWidth(80), viewport.WithHeight(4))
	v.SetContent(rendered)
	m := model{preview: source, renderedPreview: rendered, viewport: v, find: textinput.New(), focusPreview: true}
	m.find.SetValue("main")
	m.findMatches = findRanges(source, "main")
	m.renderFindHighlights()

	updated, _ := m.Update(tea.KeyPressMsg{Code: '/', Text: "/"})
	m = updated.(model)
	if got := m.find.Value(); got != "" {
		t.Fatalf("reopened find value = %q, want empty", got)
	}
	if len(m.findMatches) != 0 {
		t.Fatalf("reopened find matches = %v, want none", m.findMatches)
	}
	if got := m.viewport.GetContent(); got != rendered {
		t.Fatal("reopened find did not restore the syntax-rendered preview")
	}
}

func TestUppercaseNAdvancesToTheNextMatch(t *testing.T) {
	source := "main\n\nmain\n"
	v := viewport.New(viewport.WithWidth(80), viewport.WithHeight(1))
	m := model{preview: source, renderedPreview: source, viewport: v, focusPreview: true}
	m.findMatches = findRanges(source, "main")
	m.renderFindHighlights()
	updated, _ := m.Update(tea.KeyPressMsg{Code: 'N', Text: "N"})
	m = updated.(model)
	if m.findIndex != 1 || m.viewport.YOffset() == 0 {
		t.Fatalf("N navigated to index=%d, y=%d; want second match", m.findIndex, m.viewport.YOffset())
	}
}
