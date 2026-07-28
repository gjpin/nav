package main

import (
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// FileStatus is the combined status of a path relative to HEAD.
type FileStatus int

const (
	StatusNone FileStatus = iota
	StatusChanged
	StatusAdded
	StatusDeleted
)

// LoadState describes how much of a directory has been read. Directories are
// deliberately loaded on demand so opening a repository never walks it.
type LoadState int

const (
	LoadUnloaded LoadState = iota
	LoadLoading
	LoadPartial
	LoadLoaded
	LoadFailed
)

// Node is one entry in the explorer. Ghost entries represent deleted Git paths
// that no longer exist in the working tree.
type Node struct {
	Name      string
	Path      string
	Rel       string
	Dir       bool
	Symlink   bool
	Ghost     bool
	Status    FileStatus
	Expanded  bool
	LoadState LoadState
	LoadError string
	Parent    *Node
	Children  []*Node
	seen      bool // set while a directory refresh is in progress
}

const directoryBatchSize = 256

type directoryBatch struct {
	entries []os.DirEntry
	done    bool
	err     error
}

// directoryLoader keeps an open directory descriptor and emits small batches.
// That lets the UI paint a very large flat directory before enumeration ends.
type directoryLoader struct {
	path    string
	changes chan directoryBatch
	done    chan struct{}
	once    sync.Once
}

func newDirectoryLoader(path string) *directoryLoader {
	l := &directoryLoader{path: path, changes: make(chan directoryBatch), done: make(chan struct{})}
	go l.read()
	return l
}

func (l *directoryLoader) read() {
	f, err := os.Open(l.path)
	if err != nil {
		l.send(directoryBatch{done: true, err: err})
		return
	}
	defer f.Close()
	for {
		entries := make([]os.DirEntry, 0, directoryBatchSize)
		var err error
		// ReadDir may return fewer than the requested number of entries with a
		// nil error, reporting EOF only on a later call. Fill each batch (or
		// reach EOF) so small directories complete in a single UI update.
		for len(entries) < directoryBatchSize && err == nil {
			more, nextErr := f.ReadDir(directoryBatchSize - len(entries))
			entries = append(entries, more...)
			err = nextErr
			if len(more) == 0 && err == nil {
				err = io.EOF
			}
		}
		batch := directoryBatch{entries: entries, done: err == io.EOF, err: err}
		if err != nil && err != io.EOF {
			batch.done = true
		}
		if !l.send(batch) || batch.done {
			return
		}
	}
}

func (l *directoryLoader) send(batch directoryBatch) bool {
	select {
	case l.changes <- batch:
		return true
	case <-l.done:
		return false
	}
}

func (l *directoryLoader) close() { l.once.Do(func() { close(l.done) }) }

func lazyRoot(root string) (*Node, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	root = filepath.Clean(root)
	return &Node{Name: filepath.Base(root), Path: root, Dir: true, Expanded: true, LoadState: LoadUnloaded}, nil
}

func appendEntries(root *Node, entries []os.DirEntry, statuses map[string]FileStatus) {
	for _, entry := range entries {
		if entry.Name() == ".git" {
			continue
		}
		path := filepath.Join(root.Path, entry.Name())
		rel, _ := filepath.Rel(treeRoot(root), path)
		rel = filepath.ToSlash(rel)
		symlink := entry.Type()&os.ModeSymlink != 0
		for _, existing := range root.Children {
			if existing.Path != path {
				continue
			}
			existing.seen = true
			existing.Name, existing.Rel = entry.Name(), rel
			existing.Dir, existing.Symlink, existing.Ghost = entry.IsDir() && !symlink, symlink, false
			if status, ok := statuses[rel]; ok {
				existing.Status = status
			}
			goto next
		}
		root.add(&Node{Name: entry.Name(), Path: path, Rel: rel, Dir: entry.IsDir() && !symlink, Symlink: symlink, Status: statuses[rel], LoadState: LoadUnloaded, seen: true})
	next:
	}
}

func treeRoot(n *Node) string {
	for n.Parent != nil {
		n = n.Parent
	}
	return n.Path
}

func (n *Node) add(child *Node) {
	child.Parent = n
	n.Children = append(n.Children, child)
}

func (n *Node) sort() {
	sort.Slice(n.Children, func(i, j int) bool {
		if n.Children[i].Dir != n.Children[j].Dir {
			return n.Children[i].Dir
		}
		a, b := strings.ToLower(n.Children[i].Name), strings.ToLower(n.Children[j].Name)
		if a == b {
			return n.Children[i].Name < n.Children[j].Name
		}
		return a < b
	})
	for _, child := range n.Children {
		child.sort()
	}
}

// BuildTree reads the filesystem without following directory symlinks. statuses
// is keyed by slash-separated relative path. Deleted paths are included as ghost
// entries, together with any parent directories which no longer exist.
func BuildTree(root string, statuses map[string]FileStatus, expanded map[string]bool) (*Node, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	root = filepath.Clean(root)
	tree := &Node{Name: filepath.Base(root), Path: root, Dir: true, Expanded: true, LoadState: LoadLoaded}
	byRel := map[string]*Node{"": tree}

	var read func(*Node) error
	read = func(parent *Node) error {
		entries, err := os.ReadDir(parent.Path)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if entry.Name() == ".git" {
				continue
			}
			path := filepath.Join(parent.Path, entry.Name())
			rel, _ := filepath.Rel(root, path)
			rel = filepath.ToSlash(rel)
			// DirEntry.Type is intentionally used here: Info may resolve a link on
			// some filesystems, while Type keeps directory symlinks as leaf nodes.
			symlink := entry.Type()&os.ModeSymlink != 0
			n := &Node{Name: entry.Name(), Path: path, Rel: rel, Dir: entry.IsDir() && !symlink, Symlink: symlink, Status: statuses[rel], Expanded: expanded[rel], LoadState: LoadLoaded}
			parent.add(n)
			byRel[rel] = n
			if n.Dir {
				if err := read(n); err != nil {
					// A permission error should not make the rest of the explorer unusable.
					continue
				}
			}
		}
		return nil
	}
	if err := read(tree); err != nil {
		return nil, err
	}

	for rawRel, status := range statuses {
		rel := filepath.ToSlash(filepath.Clean(rawRel))
		if rel == "." || strings.HasPrefix(rel, "../") || status != StatusDeleted {
			continue
		}
		if n := byRel[rel]; n != nil {
			n.Status = status
			continue
		}
		parts := strings.Split(rel, "/")
		parent := tree
		for i, part := range parts {
			prefix := strings.Join(parts[:i+1], "/")
			if existing := byRel[prefix]; existing != nil {
				parent = existing
				continue
			}
			ghost := i == len(parts)-1
			n := &Node{Name: part, Path: filepath.Join(root, filepath.FromSlash(prefix)), Rel: prefix, Dir: !ghost, Ghost: ghost, Status: StatusNone, Expanded: expanded[prefix], LoadState: LoadLoaded}
			if ghost {
				n.Status = StatusDeleted
			}
			parent.add(n)
			byRel[prefix] = n
			parent = n
		}
	}
	tree.sort()
	return tree, nil
}

func visibleNodes(root *Node) []*Node {
	if root == nil {
		return nil
	}
	var nodes []*Node
	var walk func(*Node)
	walk = func(n *Node) {
		nodes = append(nodes, n)
		if n.Dir && n.Expanded {
			for _, child := range n.Children {
				walk(child)
			}
		}
	}
	// The root represents the directory supplied by the user. It remains in
	// the tree for path resolution and scanning, but is not an explorer entry.
	for _, child := range root.Children {
		walk(child)
	}
	return nodes
}

func findNode(root *Node, path string) *Node {
	var found *Node
	var walk func(*Node)
	walk = func(n *Node) {
		if n.Path == path {
			found = n
			return
		}
		for _, child := range n.Children {
			if found == nil {
				walk(child)
			}
		}
	}
	walk(root)
	return found
}
