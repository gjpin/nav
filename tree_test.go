package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func childNames(n *Node) []string {
	r := make([]string, len(n.Children))
	for i, n := range n.Children {
		r[i] = n.Name
	}
	return r
}

func TestBuildTreeOrdersEntriesAndKeepsIgnoredFiles(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "z.txt"), "z")
	writeFile(t, filepath.Join(root, "a.txt"), "a")
	writeFile(t, filepath.Join(root, "adir", "x"), "x")
	writeFile(t, filepath.Join(root, ".git", "config"), "private")
	writeFile(t, filepath.Join(root, "ignored.tmp"), "still visible")
	tree, err := BuildTree(root, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := childNames(tree)
	want := []string{"adir", "a.txt", "ignored.tmp", "z.txt"}
	if len(got) != len(want) {
		t.Fatalf("children=%v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("children=%v, want %v", got, want)
		}
	}
}

func TestBuildTreeDoesNotFollowDirectorySymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink permissions vary on Windows")
	}
	root := t.TempDir()
	target := filepath.Join(root, "target")
	writeFile(t, filepath.Join(target, "inside"), "x")
	if err := os.Symlink(target, filepath.Join(root, "link")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	tree, err := BuildTree(root, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	link := findNode(tree, filepath.Join(root, "link"))
	if link == nil || link.Dir || !link.Symlink {
		t.Fatalf("link node=%+v", link)
	}
	if len(link.Children) != 0 {
		t.Fatalf("followed symlink: %+v", link.Children)
	}
}

func TestBuildTreeAddsDeletedGhostAndParents(t *testing.T) {
	root := t.TempDir()
	statuses := map[string]FileStatus{"lost/deep/file.txt": StatusDeleted}
	tree, err := BuildTree(root, statuses, map[string]bool{"lost": true})
	if err != nil {
		t.Fatal(err)
	}
	n := findNode(tree, filepath.Join(root, "lost", "deep", "file.txt"))
	if n == nil || !n.Ghost || n.Status != StatusDeleted {
		t.Fatalf("ghost=%+v", n)
	}
	if n.Parent == nil || !n.Parent.Dir || n.Parent.Ghost {
		t.Fatalf("missing synthesized parent: %+v", n.Parent)
	}
}

func TestExpansionIsPreserved(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "folder", "file"), "x")
	tree, err := BuildTree(root, nil, map[string]bool{"folder": true})
	if err != nil {
		t.Fatal(err)
	}
	folder := findNode(tree, filepath.Join(root, "folder"))
	if folder == nil || !folder.Expanded {
		t.Fatalf("folder=%+v", folder)
	}
}

func TestVisibleNodesOmitsRootAndKeepsChildrenAtTopLevel(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "folder", "nested.txt"), "x")
	writeFile(t, filepath.Join(root, "file.txt"), "x")
	tree, err := BuildTree(root, nil, map[string]bool{"folder": true})
	if err != nil {
		t.Fatal(err)
	}
	nodes := visibleNodes(tree)
	if len(nodes) != 3 {
		t.Fatalf("visible nodes = %d, want 3", len(nodes))
	}
	if nodes[0] == tree || nodes[0].Path == root {
		t.Fatal("opened directory was included in visible nodes")
	}
	if nodes[0].Name != "folder" || nodes[1].Name != "nested.txt" || nodes[2].Name != "file.txt" {
		t.Fatalf("visible nodes = %q, %q, %q", nodes[0].Name, nodes[1].Name, nodes[2].Name)
	}
	if depth(nodes[0]) != 1 {
		t.Fatalf("top-level child depth = %d, want 1", depth(nodes[0]))
	}
}

func TestEmptyExplorerNavigationIsSafe(t *testing.T) {
	root := t.TempDir()
	tree, err := BuildTree(root, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	m := model{root: root, tree: tree, selected: root, selectionStart: -1, selectionEnd: -1}
	m.moveTree("down")
	if m.selected != root {
		t.Fatalf("selected = %q, want root", m.selected)
	}
}

func TestNewModelSelectsFirstVisibleEntry(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "z.txt"), "z")
	writeFile(t, filepath.Join(root, "adir", "file.txt"), "x")
	m, err := newModel(root)
	if err != nil {
		t.Fatal(err)
	}
	if m.watcher != nil {
		defer m.watcher.close()
	}
	nodes := visibleNodes(m.tree)
	if len(nodes) == 0 {
		t.Fatal("expected a visible entry")
	}
	if m.selected != nodes[0].Path {
		t.Fatalf("selected = %q, want first visible entry %q", m.selected, nodes[0].Path)
	}
}
