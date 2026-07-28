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

func TestOrderedSelectionRangeSupportsCharacterOffsetsAndReverseDrags(t *testing.T) {
	source := "first\nsecond\nthird"
	start, end, ok := orderedSelectionRange(source, 10, 8)
	if !ok {
		t.Fatal("selection range was not created")
	}
	if got := source[start:end]; got != "co" {
		t.Fatalf("selected source = %q, want %q", got, "co")
	}
	if _, _, ok := orderedSelectionRange(source, -1, 0); ok {
		t.Fatal("negative selection start produced a range")
	}
}

func TestByteOffsetsAtDisplayCellHandlesWideCharactersCombiningMarksAndTabs(t *testing.T) {
	line := "a界e\u0301\tz"
	tests := []struct {
		cell        int
		before, end int
	}{
		{cell: 0, before: 0, end: 1},
		{cell: 1, before: 1, end: 4},
		{cell: 2, before: 1, end: 4},
		{cell: 3, before: 4, end: 7},
		{cell: 4, before: 7, end: 8},
		{cell: 7, before: 7, end: 8},
		{cell: 8, before: 8, end: 9},
	}
	for _, test := range tests {
		before, end := byteOffsetsAtDisplayCell(line, test.cell)
		if before != test.before || end != test.end {
			t.Errorf("cell %d = (%d, %d), want (%d, %d)", test.cell, before, end, test.before, test.end)
		}
	}
}

func TestPreviewOffsetsAccountForTabStopsAfterLineNumberGutter(t *testing.T) {
	line := "\talpha"
	before, end := byteOffsetsAtDisplayCellFromColumn(line, 1, previewGutterWidth)
	if before != 1 || end != 2 {
		t.Fatalf("cell after gutter-aligned tab = (%d, %d), want first letter (1, 2)", before, end)
	}
}

func TestSelectedTextReturnsOnlyDraggedCharacters(t *testing.T) {
	source := "alpha\nbravo\ncharlie"
	m := model{preview: source, selectionStart: 3, selectionEnd: 11}
	if got := m.selectedText(); got != "ha\nbravo" {
		t.Fatalf("selected text = %q, want %q", got, "ha\\nbravo")
	}
}

func TestSystemCopyShortcutsCopyAnActiveSelection(t *testing.T) {
	for _, mod := range []tea.KeyMod{tea.ModCtrl, tea.ModSuper} {
		m := model{preview: "selected", selectionStart: 0, selectionEnd: len("selected"), focusPreview: true}
		_, cmd := m.Update(tea.KeyPressMsg{Code: 'c', Mod: mod})
		if cmd == nil {
			t.Fatalf("modifier %v did not produce a clipboard command", mod)
		}
		if _, quitting := cmd().(tea.QuitMsg); quitting {
			t.Fatalf("modifier %v quit instead of copying", mod)
		}
	}
}

func TestControlCWithoutASelectionStillQuits(t *testing.T) {
	m := model{selectionStart: -1, selectionEnd: -1}
	_, cmd := m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if cmd == nil {
		t.Fatal("ctrl+c without a selection did not produce a command")
	}
	if _, quitting := cmd().(tea.QuitMsg); !quitting {
		t.Fatal("ctrl+c without a selection did not quit")
	}
}

func TestSelectionHighlightPreservesSyntaxHighlightedSource(t *testing.T) {
	source := "package main\nfunc main() {}\n"
	rendered := renderPreview("main.go", source)
	decorated := highlightRawRange(rendered, source, len("package main\n"), len(source)-1, selectionBackground)
	if got := stripANSI(decorated); got != source {
		t.Fatalf("stripped selection = %q, want %q", got, source)
	}
	if !strings.Contains(decorated, selectionBackground) {
		t.Fatal("selection background was not rendered")
	}
}

func TestSelectionHighlightClearsBackgroundAcrossLineNumberGutters(t *testing.T) {
	source := "alpha\nbravo"
	decorated := highlightRawRange(source, source, 0, len(source), selectionBackground)
	boundary := clearFindBackground + "\n" + selectionBackground
	if !strings.Contains(decorated, boundary) {
		t.Fatalf("selection did not clear and restore its background at newline: %q", decorated)
	}
}

func TestApplyGitClearsStaleStatusesAndGhosts(t *testing.T) {
	root := t.TempDir()
	tree := &Node{Name: "root", Path: root, Dir: true, Expanded: true}
	changed := &Node{Name: "changed.go", Path: filepath.Join(root, "changed.go"), Rel: "changed.go", Status: StatusChanged}
	ghost := &Node{Name: "deleted.go", Path: filepath.Join(root, "deleted.go"), Rel: "deleted.go", Ghost: true, Status: StatusDeleted}
	tree.add(changed)
	tree.add(ghost)
	m := model{root: root, tree: tree}
	m.applyGit(gitInfo{Root: root, Statuses: map[string]FileStatus{}})
	if changed.Status != StatusNone {
		t.Fatalf("stale status = %v, want none", changed.Status)
	}
	if findNode(tree, ghost.Path) != nil {
		t.Fatal("stale deleted ghost was retained")
	}
}

func TestApplyGitKeepsAddedStatusUntilDirectoryProbeCompletes(t *testing.T) {
	root := t.TempDir()
	tree := &Node{Name: "root", Path: root, Dir: true, Expanded: true}
	added := &Node{Name: ".github", Path: filepath.Join(root, ".github"), Rel: ".github", Dir: true, Status: StatusAdded}
	tree.add(added)
	m := model{root: root, tree: tree}

	m.applyGit(gitInfo{Root: root, Statuses: map[string]FileStatus{}})
	if added.Status != StatusAdded {
		t.Fatalf("added status = %v, want it preserved until the directory probe", added.Status)
	}
}

func TestApplyGitDecoratesUnloadedDirectoryFromDescendantStatus(t *testing.T) {
	root := t.TempDir()
	tree := &Node{Name: "root", Path: root, Dir: true, Expanded: true}
	dir := &Node{Name: "dir", Path: filepath.Join(root, "dir"), Rel: "dir", Dir: true, LoadState: LoadUnloaded}
	tree.add(dir)
	m := model{root: root, tree: tree}

	m.applyGit(gitInfo{Root: root, Statuses: map[string]FileStatus{
		"dir/deeper/changed.go": StatusChanged,
	}})
	if dir.Status != StatusChanged {
		t.Fatalf("unloaded directory status = %v, want changed", dir.Status)
	}
}

func TestFileWatcherRecognizesGitMetadataEvents(t *testing.T) {
	gitDir := filepath.Join(t.TempDir(), ".git")
	fw := &fileWatcher{gitDirs: map[string]bool{gitDir: true}}
	if !fw.isGitPath(filepath.Join(gitDir, "index")) {
		t.Fatal("Git index event was not recognized")
	}
	if fw.isGitPath(filepath.Join(filepath.Dir(gitDir), "file.go")) {
		t.Fatal("working-tree event was treated as Git metadata")
	}
}

func TestDirectoryRefreshKeepsExistingEntriesVisibleUntilScanCompletes(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".github", "workflows", "test.yml"), "name: test\n")

	tree := &Node{Name: "root", Path: root, Dir: true, Expanded: true, LoadState: LoadLoaded}
	github := &Node{Name: ".github", Path: filepath.Join(root, ".github"), Rel: ".github", Dir: true}
	stale := &Node{Name: "stale", Path: filepath.Join(root, "stale"), Rel: "stale"}
	tree.add(github)
	tree.add(stale)
	m := model{root: root, tree: tree, loaders: make(map[string]*directoryLoader), loadGeneration: make(map[string]int)}

	cmd := m.startDirectoryLoad(tree)
	if findNode(tree, github.Path) == nil {
		t.Fatal("existing directory disappeared while its refresh was pending")
	}
	msg := cmd().(directoryMsg)
	if next := m.loadDirectoryResult(msg); next != nil {
		t.Fatal("small directory unexpectedly needed another batch")
	}
	if findNode(tree, github.Path) == nil {
		t.Fatal("refreshed directory was removed")
	}
	if findNode(tree, stale.Path) != nil {
		t.Fatal("path absent from completed scan was retained")
	}
}
