package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

type gitInfo struct {
	Root         string
	RepoRoot     string
	Statuses     map[string]FileStatus
	TreeStatuses map[string]FileStatus
}

func runGit(dir string, args ...string) ([]byte, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	// git status normally refreshes the index and can rewrite it. Navigator
	// watches Git metadata for changes, so that write would be observed as a
	// new change and cause an endless refresh cycle. These are read-only
	// queries; disable Git's optional locks (and the accompanying index write).
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
	return cmd.Output()
}

func inspectGit(root string) gitInfo {
	return inspectGitWithUntracked(root, "all")
}

// gitMetadataDir returns the directory whose changes alter Git status (index,
// HEAD, and refs). It also resolves linked worktrees, where .git is a file.
func gitMetadataDir(root string) string {
	out, err := runGit(root, "rev-parse", "--git-dir")
	if err != nil {
		return ""
	}
	dir := strings.TrimSpace(string(out))
	if dir == "" {
		return ""
	}
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(root, dir)
	}
	return filepath.Clean(dir)
}

// inspectGitTracked avoids walking untracked directories. It is safe to run
// after the UI has appeared and supplies tracked changes/deleted ghosts first.
func inspectGitTracked(root string) gitInfo {
	return inspectGitWithUntracked(root, "no")
}

func inspectGitWithUntracked(root, mode string) gitInfo {
	info := gitInfo{Root: root, Statuses: make(map[string]FileStatus)}
	top, err := runGit(root, "rev-parse", "--show-toplevel")
	if err != nil {
		return info
	}
	info.RepoRoot = strings.TrimSpace(string(top))
	compareRoot, compareRepo := root, info.RepoRoot
	// macOS commonly exposes /var through /private/var. Git resolves one form
	// while a caller may supply the other, so normalize only for this comparison.
	if physical, err := filepath.EvalSymlinks(root); err == nil {
		compareRoot = physical
	}
	if physical, err := filepath.EvalSymlinks(info.RepoRoot); err == nil {
		compareRepo = physical
	}
	out, err := runGit(root, "status", "--porcelain=v1", "-z", "--untracked-files="+mode)
	if err != nil {
		return info
	}
	info.Statuses = parseGitStatuses(compareRoot, compareRepo, out)
	info.TreeStatuses = aggregateGitStatuses(info.Statuses)
	return info
}

// aggregateGitStatuses adds a decoration for every directory containing a
// changed path. Git porcelain reports tracked changes only at their leaf paths,
// while the explorer deliberately does not load a directory until it is
// expanded. Keeping this separate from Statuses preserves the exact porcelain
// paths used for deleted ghosts and directory-scoped refreshes.
func aggregateGitStatuses(statuses map[string]FileStatus) map[string]FileStatus {
	treeStatuses := make(map[string]FileStatus, len(statuses))
	for rel, status := range statuses {
		rel = filepath.ToSlash(filepath.Clean(rel))
		if rel == "." || rel == "" || rel == ".." || strings.HasPrefix(rel, "../") {
			continue
		}
		treeStatuses[rel] = mergeFileStatus(treeStatuses[rel], status)
		for parent := rel; ; {
			slash := strings.LastIndexByte(parent, '/')
			if slash < 0 {
				break
			}
			parent = parent[:slash]
			treeStatuses[parent] = mergeFileStatus(treeStatuses[parent], status)
		}
	}
	return treeStatuses
}

// A directory with one kind of descendant change uses that change's colour.
// Mixed additions/deletions/modifications are shown as changed (yellow).
func mergeFileStatus(current, next FileStatus) FileStatus {
	if current == StatusNone || current == next {
		return next
	}
	if next == StatusNone {
		return current
	}
	return StatusChanged
}

// inspectGitDirectory resolves untracked entries only for one opened branch.
// --untracked-files=normal reports direct files and opaque untracked folders,
// avoiding a full walk of the branch's descendants.
func inspectGitDirectory(info gitInfo, rel string) map[string]FileStatus {
	if info.RepoRoot == "" {
		return nil
	}
	args := []string{"status", "--porcelain=v1", "-z", "--untracked-files=normal", "--"}
	if rel == "" {
		args = append(args, ".")
	} else {
		args = append(args, filepath.FromSlash(rel))
	}
	out, err := runGit(info.Root, args...)
	if err != nil {
		return nil
	}
	compareRoot, compareRepo := info.Root, info.RepoRoot
	if physical, err := filepath.EvalSymlinks(info.Root); err == nil {
		compareRoot = physical
	}
	if physical, err := filepath.EvalSymlinks(info.RepoRoot); err == nil {
		compareRepo = physical
	}
	return parseGitStatuses(compareRoot, compareRepo, out)
}

func parseGitStatuses(compareRoot, compareRepo string, out []byte) map[string]FileStatus {
	statuses := make(map[string]FileStatus)
	parts := bytes.Split(out, []byte{0})
	for i := 0; i < len(parts); i++ {
		entry := string(parts[i])
		if len(entry) < 4 {
			continue
		}
		x, y := entry[0], entry[1]
		path := entry[3:]
		// In -z porcelain rename/copy records are path then original path.
		if x == 'R' || x == 'C' {
			i++
		}
		rel, err := filepath.Rel(compareRoot, filepath.Join(compareRepo, filepath.FromSlash(path)))
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		rel = filepath.ToSlash(rel)
		statuses[rel] = porcelainStatus(x, y)
	}
	return statuses
}

func porcelainStatus(x, y byte) FileStatus {
	if x == '?' && y == '?' || x == 'A' || y == 'A' {
		return StatusAdded
	}
	if x == 'D' || y == 'D' {
		return StatusDeleted
	}
	if x != ' ' || y != ' ' {
		return StatusChanged
	}
	return StatusNone
}

func gitDiff(info gitInfo, node *Node) (string, error) {
	if info.RepoRoot == "" {
		return "", nil
	}
	if node.Status == StatusAdded && node.Path != "" {
		out, err := exec.Command("git", "diff", "--no-index", "--", "/dev/null", node.Path).CombinedOutput()
		if err != nil && len(out) == 0 {
			return "", err
		}
		return string(out), nil // git diff returns 1 when differences were found
	}
	if node.Rel == "" {
		return "", nil
	}
	cmd := exec.Command("git", "-C", info.Root, "diff", "--no-ext-diff", "--unified=3", "HEAD", "--", filepath.FromSlash(node.Rel))
	out, err := cmd.CombinedOutput()
	if err != nil && len(out) == 0 {
		return "", err
	}
	return string(out), nil
}

const maxPreviewBytes = 4 << 20

func readPreview(node *Node) (string, error) {
	if node.Ghost {
		return "Deleted from working tree. Press d to view its diff against HEAD.", nil
	}
	if node.Dir {
		return "Select a file to preview it.", nil
	}
	info, err := os.Stat(node.Path)
	if err != nil {
		return "Unreadable: " + err.Error(), nil
	}
	if info.Size() > maxPreviewBytes {
		return fmt.Sprintf("Preview unavailable: file is larger than %d MiB.", maxPreviewBytes>>20), nil
	}
	b, err := os.ReadFile(node.Path)
	if err != nil {
		return "Unreadable: " + err.Error(), nil
	}
	if !utf8.Valid(b) || bytes.IndexByte(b, 0) >= 0 {
		return "Preview unavailable: binary or non-UTF-8 file.", nil
	}
	return string(b), nil
}

// diffLineMarks maps current-file line indices to added/changed markers and
// removed-line counts. Deleted hunks are attached to the nearest following
// live line (or EOF). A run of additions following a removal is a replacement,
// so it is marked changed rather than added.
func diffLineMarks(diff string, lineCount int) (map[int]bool, map[int]bool, map[int]int) {
	adds, changes, dels := map[int]bool{}, map[int]bool{}, map[int]int{}
	var newLine int
	replacing := false
	for _, line := range strings.Split(diff, "\n") {
		if strings.HasPrefix(line, "@@ ") {
			fields := strings.Fields(line)
			if len(fields) >= 3 {
				start := strings.TrimPrefix(fields[2], "+")
				start = strings.SplitN(start, ",", 2)[0]
				fmt.Sscanf(start, "%d", &newLine)
			}
			replacing = false
			continue
		}
		if newLine == 0 || strings.HasPrefix(line, "+++") || strings.HasPrefix(line, "---") {
			continue
		}
		switch {
		case strings.HasPrefix(line, "+"):
			if replacing {
				changes[newLine-1] = true
			} else {
				adds[newLine-1] = true
			}
			newLine++
		case strings.HasPrefix(line, "-"):
			idx := newLine - 1
			if idx >= lineCount {
				idx = lineCount
			}
			dels[idx]++
			replacing = true
		case strings.HasPrefix(line, " "):
			newLine++
			replacing = false
		}
	}
	return adds, changes, dels
}

// diffMarks retains the original aggregate marker API for callers that only
// need to know that a current line has any addition, including replacements.
func diffMarks(diff string, lineCount int) (map[int]bool, map[int]int) {
	adds, changes, dels := diffLineMarks(diff, lineCount)
	for line := range changes {
		adds[line] = true
	}
	return adds, dels
}

// firstCurrentFileChange returns the earliest current-file line that was
// added or changed. Replacement edits are tracked separately from pure
// additions so the gutter can use a different color, but both should be used
// when choosing where to open a file.
func firstCurrentFileChange(adds, changes map[int]bool, lineCount int) (int, bool) {
	for line := 0; line < lineCount; line++ {
		if adds[line] || changes[line] {
			return line, true
		}
	}
	return 0, false
}
