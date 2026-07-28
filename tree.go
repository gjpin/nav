package main

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// FileStatus is the combined status of a path relative to HEAD.
type FileStatus int

const (
	StatusNone FileStatus = iota
	StatusChanged
	StatusAdded
	StatusDeleted
)

// Node is one entry in the explorer. Ghost entries represent deleted Git paths
// that no longer exist in the working tree.
type Node struct {
	Name     string
	Path     string
	Rel      string
	Dir      bool
	Symlink  bool
	Ghost    bool
	Status   FileStatus
	Expanded bool
	Parent   *Node
	Children []*Node
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
	tree := &Node{Name: filepath.Base(root), Path: root, Dir: true, Expanded: true}
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
			n := &Node{Name: entry.Name(), Path: path, Rel: rel, Dir: entry.IsDir() && !symlink, Symlink: symlink, Status: statuses[rel], Expanded: expanded[rel]}
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
			n := &Node{Name: part, Path: filepath.Join(root, filepath.FromSlash(prefix)), Rel: prefix, Dir: !ghost, Ghost: ghost, Status: StatusNone, Expanded: expanded[prefix]}
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
