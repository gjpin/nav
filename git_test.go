package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

func TestInspectGitStatusesAndDeletedGhost(t *testing.T) {
	root := t.TempDir()
	git(t, root, "init", "-q")
	git(t, root, "config", "user.email", "test@example.com")
	git(t, root, "config", "user.name", "Test")
	writeFile(t, filepath.Join(root, "changed.txt"), "old\n")
	writeFile(t, filepath.Join(root, "gone.txt"), "gone\n")
	writeFile(t, filepath.Join(root, "staged.txt"), "old\n")
	git(t, root, "add", ".")
	git(t, root, "commit", "-qm", "initial")
	writeFile(t, filepath.Join(root, "changed.txt"), "new\n")
	writeFile(t, filepath.Join(root, "staged.txt"), "new\n")
	git(t, root, "add", "staged.txt")
	writeFile(t, filepath.Join(root, "untracked.txt"), "new\n")
	if err := os.Remove(filepath.Join(root, "gone.txt")); err != nil {
		t.Fatal(err)
	}
	info := inspectGit(root)
	for _, path := range []string{"changed.txt", "staged.txt"} {
		if info.Statuses[path] != StatusChanged {
			t.Errorf("%s status=%v", path, info.Statuses[path])
		}
	}
	if info.Statuses["untracked.txt"] != StatusAdded {
		t.Errorf("untracked=%v", info.Statuses["untracked.txt"])
	}
	if info.Statuses["gone.txt"] != StatusDeleted {
		t.Errorf("gone=%v", info.Statuses["gone.txt"])
	}
	tree, err := BuildTree(root, info.Statuses, nil)
	if err != nil {
		t.Fatal(err)
	}
	gone := findNode(tree, filepath.Join(root, "gone.txt"))
	if gone == nil || !gone.Ghost {
		t.Fatalf("deleted ghost=%+v", gone)
	}
}

func TestDiffMarksHandlesAddedAndDeletedOnlyHunks(t *testing.T) {
	diff := strings.Join([]string{"diff --git a/a b/a", "@@ -1,2 +1,2 @@", "-old", "+new", " keep", "@@ -9 +9,0 @@", "-last"}, "\n")
	adds, dels := diffMarks(diff, 2)
	if !adds[0] {
		t.Fatalf("added marks=%v", adds)
	}
	if dels[0] != 1 {
		t.Fatalf("removed at replacement=%v", dels)
	}
	if dels[2] != 1 {
		t.Fatalf("EOF deletion=%v", dels)
	}
}

func TestDiffLineMarksDistinguishesAddedChangedAndDeleted(t *testing.T) {
	diff := strings.Join([]string{
		"diff --git a/a b/a",
		"@@ -1,2 +1,3 @@",
		"-old",
		"+new",
		"+extra",
		" keep",
		"@@ -9 +10,0 @@",
		"-last",
	}, "\n")
	adds, changes, dels := diffLineMarks(diff, 3)
	if len(adds) != 0 {
		t.Fatalf("added markers = %v, want no pure additions", adds)
	}
	if !changes[0] || !changes[1] {
		t.Fatalf("changed markers = %v, want replacement lines 0 and 1", changes)
	}
	if dels[0] != 1 || dels[3] != 1 {
		t.Fatalf("removed markers = %v", dels)
	}
}

func TestFirstCurrentFileChangeIncludesReplacements(t *testing.T) {
	adds := map[int]bool{20: true}
	changes := map[int]bool{7: true}
	line, ok := firstCurrentFileChange(adds, changes, 30)
	if !ok || line != 7 {
		t.Fatalf("first change = (%d, %t), want (7, true)", line, ok)
	}
}

func TestReadPreviewRejectsBinaryAndLarge(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "binary")
	if err := os.WriteFile(binary, []byte{0, 1}, 0o644); err != nil {
		t.Fatal(err)
	}
	text, err := readPreview(&Node{Path: binary})
	if err != nil || !strings.Contains(text, "binary") {
		t.Fatalf("%q %v", text, err)
	}
	large := filepath.Join(root, "large")
	if err := os.WriteFile(large, make([]byte, maxPreviewBytes+1), 0o644); err != nil {
		t.Fatal(err)
	}
	text, err = readPreview(&Node{Path: large})
	if err != nil || !strings.Contains(text, "larger") {
		t.Fatalf("%q %v", text, err)
	}
}
