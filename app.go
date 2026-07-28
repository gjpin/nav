package main

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/fsnotify/fsnotify"
)

var (
	selectedStyle = lipgloss.NewStyle().Background(lipgloss.Color("62")).Foreground(lipgloss.Color("230"))
	mutedStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	yellowStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("220"))
	greenStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	redStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
	cyanStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("81"))
)

type keyMap struct {
	Up, Down, Toggle, Open, Preview, Diff, Find, Next, Refresh, Quit, Copy key.Binding
}

func newKeyMap() keyMap {
	return keyMap{
		Up: key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/k", "move")), Down: key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓/j", "move")),
		Toggle: key.NewBinding(key.WithKeys("left", "right"), key.WithHelp("←/→", "collapse/expand")), Open: key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "open")),
		Preview: key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "preview")), Diff: key.NewBinding(key.WithKeys("d"), key.WithHelp("d", "diff")),
		Find: key.NewBinding(key.WithKeys("/"), key.WithHelp("/", "find")), Next: key.NewBinding(key.WithKeys("n", "N"), key.WithHelp("n/N", "next match")),
		Refresh: key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "refresh")),
		Quit:    key.NewBinding(key.WithKeys("q", "ctrl+c"), key.WithHelp("q", "quit")), Copy: key.NewBinding(key.WithKeys("y", "ctrl+c"), key.WithHelp("y", "copy selection")),
	}
}

func (k keyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Up, k.Open, k.Preview, k.Diff, k.Find, k.Refresh, k.Quit}
}
func (k keyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{k.ShortHelp(), {k.Copy, k.Next}}
}

type directoryMsg struct {
	path       string
	generation int
	batch      directoryBatch
}
type gitMsg struct {
	generation int
	info       gitInfo
}
type directoryGitMsg struct {
	path       string
	generation int
	statuses   map[string]FileStatus
}
type previewMsg struct {
	generation   int
	path         string
	diffMode     bool
	openToChange bool
	text         string
	rendered     string
	added        map[int]bool
	changed      map[int]bool
	removed      map[int]int
}
type watchMsg struct{ path string }

type fileWatcher struct {
	w       *fsnotify.Watcher
	changes chan string
	done    chan struct{}
	once    sync.Once
	mu      sync.Mutex
	watched map[string]bool
	gitDirs map[string]bool
}

func newFileWatcher(root string) *fileWatcher {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return nil
	}
	fw := &fileWatcher{w: w, changes: make(chan string, 1), done: make(chan struct{}), watched: make(map[string]bool), gitDirs: make(map[string]bool)}
	fw.watch(root)
	go fw.loop()
	return fw
}

// watch adds one visible directory. Recursive watches make opening a large
// repository expensive and can exhaust the operating system watch limit.
func (f *fileWatcher) watch(root string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.watched[root] || f.w.Add(root) != nil {
		return
	}
	f.watched[root] = true
}

func (f *fileWatcher) watchGit(root string) {
	dir := gitMetadataDir(root)
	if dir == "" {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.watched[dir] && f.w.Add(dir) == nil {
		f.watched[dir] = true
	}
	if f.watched[dir] {
		f.gitDirs[dir] = true
	}
}

func (f *fileWatcher) isGitPath(path string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for dir := range f.gitDirs {
		if path == dir || strings.HasPrefix(path, dir+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func (f *fileWatcher) unwatchBelow(root string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for path := range f.watched {
		if path != root && strings.HasPrefix(path, root+string(filepath.Separator)) {
			_ = f.w.Remove(path)
			delete(f.watched, path)
			delete(f.gitDirs, path)
		}
	}
}

func (f *fileWatcher) loop() {
	var timer *time.Timer
	var timerC <-chan time.Time
	var changed string
	for {
		select {
		case <-f.done:
			if timer != nil {
				timer.Stop()
			}
			_ = f.w.Close()
			return
		case event, ok := <-f.w.Events:
			if !ok {
				return
			}
			changed = event.Name
			if timer == nil {
				timer = time.NewTimer(140 * time.Millisecond)
			} else {
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				timer.Reset(140 * time.Millisecond)
			}
			timerC = timer.C
		case <-timerC:
			select {
			case f.changes <- changed:
			default:
			}
			timerC = nil
		case <-f.w.Errors:
		}
	}
}
func (f *fileWatcher) next() tea.Cmd {
	return func() tea.Msg { return watchMsg{path: <-f.changes} }
}
func (f *fileWatcher) close() { f.once.Do(func() { close(f.done) }) }

type model struct {
	root                             string
	tree                             *Node
	git                              gitInfo
	selected                         string
	treeOffset                       int
	width, height, treeHeight        int
	focusPreview, diffMode, finding  bool
	preview                          string
	previewPath                      string
	renderedPreview                  string
	previewLines                     []string
	findMatches                      [][]int
	findIndex                        int
	added                            map[int]bool
	changed                          map[int]bool
	removed                          map[int]int
	selectionStart, selectionEnd     int
	selecting                        bool
	viewport                         viewport.Model
	find                             textinput.Model
	help                             help.Model
	keys                             keyMap
	watcher                          *fileWatcher
	loaders                          map[string]*directoryLoader
	loadGeneration                   map[string]int
	gitGeneration, previewGeneration int
	rows                             []*Node
}

func newModel(root string) (model, error) {
	tree, err := lazyRoot(root)
	if err != nil {
		return model{}, err
	}
	v := viewport.New()
	v.SoftWrap = false
	v.HighlightStyle = lipgloss.NewStyle().Background(lipgloss.Color("237"))
	v.SelectedHighlightStyle = lipgloss.NewStyle().Background(lipgloss.Color("62")).Foreground(lipgloss.Color("230"))
	f := textinput.New()
	f.Prompt = "Find: "
	f.Placeholder = "text"
	m := model{root: root, tree: tree, git: gitInfo{Root: root, Statuses: make(map[string]FileStatus)}, selected: root, viewport: v, find: f, help: help.New(), keys: newKeyMap(), watcher: newFileWatcher(root), loaders: make(map[string]*directoryLoader), loadGeneration: make(map[string]int), selectionStart: -1, selectionEnd: -1}
	m.rebuildRows()
	return m, nil
}

func hasExpandedChild(n *Node) bool {
	for _, child := range n.Children {
		if child.Dir && child.Expanded {
			return true
		}
	}
	return false
}

func (m model) Init() tea.Cmd {
	cmds := []tea.Cmd{m.startDirectoryLoad(m.tree), gitCmd(m.root, m.gitGeneration)}
	if m.watcher != nil {
		cmds = append(cmds, m.watcher.next())
	}
	return tea.Batch(cmds...)
}

func (m *model) rebuildRows() { m.rows = visibleNodes(m.tree) }

func (m *model) visible() []*Node { return m.rows }

func (m *model) startDirectoryLoad(node *Node) tea.Cmd {
	if node == nil || !node.Dir || node.Ghost {
		return nil
	}
	if old := m.loaders[node.Path]; old != nil {
		old.close()
	}
	m.loadGeneration[node.Path]++
	generation := m.loadGeneration[node.Path]
	for _, child := range node.Children {
		// Keep the last completed view on screen while the replacement scan is
		// running. Clearing it here made newly created directories briefly
		// disappear after each filesystem event.
		child.seen = false
	}
	node.LoadError = ""
	node.LoadState = LoadLoading
	loader := newDirectoryLoader(node.Path)
	m.loaders[node.Path] = loader
	return func() tea.Msg {
		batch, ok := <-loader.changes
		if !ok {
			return directoryMsg{path: node.Path, generation: generation, batch: directoryBatch{done: true}}
		}
		return directoryMsg{path: node.Path, generation: generation, batch: batch}
	}
}

func hasGhost(n *Node) bool {
	if n.Ghost {
		return true
	}
	for _, child := range n.Children {
		if hasGhost(child) {
			return true
		}
	}
	return false
}

func (m *model) nextDirectoryLoad(path string, generation int) tea.Cmd {
	loader := m.loaders[path]
	if loader == nil {
		return nil
	}
	return func() tea.Msg {
		batch, ok := <-loader.changes
		if !ok {
			return directoryMsg{path: path, generation: generation, batch: directoryBatch{done: true}}
		}
		return directoryMsg{path: path, generation: generation, batch: batch}
	}
}

func (m *model) loadDirectoryResult(msg directoryMsg) tea.Cmd {
	if msg.generation != m.loadGeneration[msg.path] {
		return nil
	}
	node := findNode(m.tree, msg.path)
	if node == nil || !node.Dir {
		return nil
	}
	appendEntries(node, msg.batch.entries, m.git.Statuses)
	if msg.batch.err != nil && !msg.batch.done {
		node.LoadError = msg.batch.err.Error()
	}
	if msg.batch.done {
		delete(m.loaders, msg.path)
		if msg.batch.err != nil && msg.batch.err.Error() != "EOF" {
			node.LoadState, node.LoadError = LoadFailed, msg.batch.err.Error()
		} else {
			// Drop paths that were absent from the completed scan, but retain
			// deleted Git ghosts (including their ancestors) until Git says they
			// are no longer deleted.
			children := node.Children[:0]
			for _, child := range node.Children {
				if child.seen || hasGhost(child) {
					children = append(children, child)
				}
			}
			node.Children = children
			node.LoadState = LoadLoaded
			node.sort()
		}
	} else {
		node.LoadState = LoadPartial
	}
	if node == m.tree && !hasExpandedChild(node) {
		// During the initial flat-directory stream the rows are exactly the
		// root's direct children. Reuse that slice so each batch does not
		// repeatedly traverse every entry received so far.
		m.rows = node.Children
	} else {
		m.rebuildRows()
	}
	if m.selected == m.root && len(m.rows) > 0 {
		m.selected = m.rows[0].Path
	}
	if node.Expanded && m.watcher != nil {
		m.watcher.watch(node.Path)
	}
	if msg.batch.done {
		if m.git.RepoRoot != "" {
			return directoryGitCmd(m.git, node, msg.generation)
		}
		return nil
	}
	return m.nextDirectoryLoad(msg.path, msg.generation)
}

func (m *model) applyGit(info gitInfo) {
	// The initial Git probe deliberately skips untracked files so opening a
	// large repository stays cheap. Keep any added badges from the preceding
	// directory probe until its replacement arrives; otherwise an untracked
	// directory briefly changes from green to plain on every refresh.
	clearGitDecorations(m.tree, true)
	m.git = info
	walkNodes(m.tree, func(n *Node) {
		if status, ok := info.Statuses[n.Rel]; ok {
			n.Status = status
		}
	})
	m.addDeletedGhosts()
	m.rebuildRows()
}

func clearGitDecorations(n *Node, keepAdded bool) {
	if n == nil {
		return
	}
	if !keepAdded || n.Status != StatusAdded {
		n.Status = StatusNone
	}
	children := n.Children[:0]
	for _, child := range n.Children {
		if child.Ghost {
			continue
		}
		clearGitDecorations(child, keepAdded)
		children = append(children, child)
	}
	n.Children = children
}

func walkNodes(n *Node, visit func(*Node)) {
	if n == nil {
		return
	}
	visit(n)
	for _, child := range n.Children {
		walkNodes(child, visit)
	}
}

func (m *model) addDeletedGhosts() {
	for rel, status := range m.git.Statuses {
		if status != StatusDeleted || rel == "" || strings.HasPrefix(rel, "../") {
			continue
		}
		parent := m.tree
		parts := strings.Split(filepath.ToSlash(filepath.Clean(rel)), "/")
		for i, part := range parts {
			path := filepath.Join(m.root, filepath.FromSlash(strings.Join(parts[:i+1], "/")))
			n := findNode(m.tree, path)
			if n == nil {
				n = &Node{Name: part, Path: path, Rel: filepath.ToSlash(strings.Join(parts[:i+1], "/")), Dir: i != len(parts)-1, Ghost: i == len(parts)-1, LoadState: LoadUnloaded}
				if n.Ghost {
					n.Status = StatusDeleted
				}
				parent.add(n)
			}
			parent = n
		}
	}
}

func gitCmd(root string, generation int) tea.Cmd {
	return func() tea.Msg { return gitMsg{generation: generation, info: inspectGitTracked(root)} }
}

func directoryGitCmd(info gitInfo, node *Node, generation int) tea.Cmd {
	path, rel := node.Path, node.Rel
	return func() tea.Msg {
		return directoryGitMsg{path: path, generation: generation, statuses: inspectGitDirectory(info, rel)}
	}
}

func (m *model) applyDirectoryGit(msg directoryGitMsg) {
	if msg.generation != m.loadGeneration[msg.path] {
		return
	}
	node := findNode(m.tree, msg.path)
	if node == nil {
		return
	}
	// This probe is authoritative for the directory's direct entries. Clear a
	// prior untracked badge only after the replacement probe has completed, so
	// a tracked-only refresh cannot make it flicker in the meantime.
	if msg.statuses != nil {
		for _, child := range node.Children {
			if child.Status == StatusAdded {
				if _, ok := msg.statuses[child.Rel]; !ok {
					child.Status = StatusNone
					delete(m.git.Statuses, child.Rel)
				}
			}
		}
	}
	for rel, status := range msg.statuses {
		m.git.Statuses[rel] = status
		if node := findNode(m.tree, filepath.Join(m.root, filepath.FromSlash(rel))); node != nil {
			node.Status = status
		}
	}
	m.rebuildRows()
}

func (m model) expanded() map[string]bool {
	r := map[string]bool{}
	if m.tree == nil {
		return r
	}
	var walk func(*Node)
	walk = func(n *Node) {
		if n.Dir && n.Expanded {
			r[n.Rel] = true
		}
		for _, c := range n.Children {
			walk(c)
		}
	}
	walk(m.tree)
	r[""] = true
	return r
}

func (m *model) selectedNode() *Node {
	if m.tree == nil {
		return nil
	}
	for _, n := range m.visible() {
		if n.Path == m.selected {
			return n
		}
	}
	return m.tree
}

func previewCmd(info gitInfo, node Node, diffMode bool, openToChange bool, generation int) tea.Cmd {
	return func() tea.Msg {
		var text string
		if diffMode {
			text, _ = gitDiff(info, &node)
		} else {
			text, _ = readPreview(&node)
		}
		if diffMode && text == "" {
			text = "No diff against HEAD."
		}
		lines := strings.Split(text, "\n")
		var added, changed map[int]bool
		var removed map[int]int
		if !diffMode {
			diff, _ := gitDiff(info, &node)
			added, changed, removed = diffLineMarks(diff, len(lines))
			if removed[len(lines)] > 0 {
				lines = append(lines, "")
				text = strings.Join(lines, "\n")
			}
		}
		rendered := text
		if !diffMode {
			rendered = renderPreview(node.Path, text)
		}
		return previewMsg{generation: generation, path: node.Path, diffMode: diffMode, openToChange: openToChange, text: text, rendered: rendered, added: added, changed: changed, removed: removed}
	}
}

func (m *model) requestPreview(openToChange bool) tea.Cmd {
	n := m.selectedNode()
	if n == nil || n.Dir {
		return nil
	}
	m.previewGeneration++
	m.previewPath = n.Path
	m.preview = "Loading preview…"
	m.renderedPreview = m.preview
	m.previewLines = []string{m.preview}
	m.viewport.SetContent(m.preview)
	return previewCmd(m.git, *n, m.diffMode, openToChange, m.previewGeneration)
}

// applyPreview accepts a completed background preview for the current file.
func (m *model) applyPreview(msg previewMsg, openToChange bool) {
	if msg.generation != m.previewGeneration || msg.path != m.selected || msg.diffMode != m.diffMode {
		return
	}
	m.preview = msg.text
	m.previewLines = strings.Split(msg.text, "\n")
	m.added, m.changed, m.removed = msg.added, msg.changed, msg.removed
	m.renderedPreview = msg.rendered
	m.viewport.SetContent(m.renderedPreview)
	// Only an explicit file open jumps to its first current-file change. This
	// avoids resetting a touchpad scroll when the tree or watcher refreshes.
	if openToChange {
		m.viewport.GotoTop()
	}
	if openToChange && !m.diffMode {
		if line, ok := firstCurrentFileChange(m.added, m.changed, len(m.previewLines)); ok {
			// Keep a small lead-in around the first current-file change so the
			// surrounding code is visible immediately.
			m.viewport.SetYOffset(max(0, line-5))
		}
	}
	m.viewport.StyleLineFunc = func(i int) lipgloss.Style {
		if m.diffMode && i < len(m.previewLines) {
			if strings.HasPrefix(m.previewLines[i], "+") && !strings.HasPrefix(m.previewLines[i], "+++") {
				return greenStyle
			}
			if strings.HasPrefix(m.previewLines[i], "-") && !strings.HasPrefix(m.previewLines[i], "---") {
				return redStyle
			}
		}
		return lipgloss.NewStyle()
	}
	m.viewport.LeftGutterFunc = func(c viewport.GutterContext) string {
		if c.Soft {
			return "       "
		}
		if c.Index >= len(m.previewLines) {
			return mutedStyle.Render("     ~ ")
		}
		marker := mutedStyle.Render(" ")
		if m.added != nil && m.added[c.Index] {
			marker = greenStyle.Render("▏")
		}
		if m.changed != nil && m.changed[c.Index] {
			marker = yellowStyle.Render("▏")
		}
		if m.removed != nil && m.removed[c.Index] > 0 {
			// A replacement can have both removed and changed content. Yellow
			// communicates that the visible line was modified; red is reserved
			// for deletion-only ranges attached to the next live line (or EOF).
			if m.changed == nil || !m.changed[c.Index] {
				marker = redStyle.Render("▏")
			}
		}
		return mutedStyle.Render(fmt.Sprintf("%4d ", c.Index+1)) + marker + " "
	}
	m.applyFind()
}

func (m *model) applyFind() {
	// viewport's built-in highlighter cannot track byte offsets through ANSI
	// sequences. Apply a background-only ANSI overlay instead, preserving the
	// syntax formatter's foreground colours and attributes.
	m.viewport.ClearHighlights()
	needle := m.find.Value()
	if needle == "" {
		m.findMatches, m.findIndex = nil, 0
		m.viewport.SetContent(m.renderedPreview)
		return
	}
	m.findMatches = findRanges(m.preview, needle)
	m.findIndex = 0
	m.renderFindHighlights()
}

// findRanges treats user input as literal text, not a regular expression, and
// uses Go's Unicode-aware case-insensitive matching for byte ranges accepted
// by the viewport.
func findRanges(source, needle string) [][]int {
	pattern, err := regexp.Compile("(?i)" + regexp.QuoteMeta(needle))
	if err != nil {
		return nil
	}
	return pattern.FindAllStringIndex(source, -1)
}

const (
	findBackground         = "\x1b[48;5;237m"
	selectedFindBackground = "\x1b[48;5;62m"
	clearFindBackground    = "\x1b[49m"
)

func (m *model) renderFindHighlights() {
	rendered := m.renderedPreview
	for i := len(m.findMatches) - 1; i >= 0; i-- {
		r := rawRangeToRendered(m.preview, m.renderedPreview, m.findMatches[i][0], m.findMatches[i][1])
		if r[0] < 0 || r[1] < r[0] || r[1] > len(rendered) {
			continue
		}
		background := findBackground
		if i == m.findIndex {
			background = selectedFindBackground
		}
		// Chroma resets styles between tokens. Reapply the background after a
		// reset when a text match spans token boundaries.
		middle := strings.ReplaceAll(rendered[r[0]:r[1]], "\x1b[0m", "\x1b[0m"+background)
		rendered = rendered[:r[0]] + background + middle + clearFindBackground + rendered[r[1]:]
	}
	m.viewport.SetContent(rendered)
}

func (m *model) nextFind(delta int) {
	if len(m.findMatches) == 0 {
		return
	}
	m.findIndex = (m.findIndex + delta + len(m.findMatches)) % len(m.findMatches)
	m.renderFindHighlights()
	line := strings.Count(m.preview[:m.findMatches[m.findIndex][0]], "\n")
	m.viewport.SetYOffset(line)
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.treeHeight = max(3, msg.Height/3)
		m.viewport.SetWidth(max(1, msg.Width))
		m.viewport.SetHeight(max(1, msg.Height-m.treeHeight-4))
		m.find.SetWidth(max(12, msg.Width-8))
		return m, nil
	case directoryMsg:
		return m, m.loadDirectoryResult(msg)
	case gitMsg:
		if msg.generation == m.gitGeneration {
			m.applyGit(msg.info)
			if m.watcher != nil && msg.info.RepoRoot != "" {
				m.watcher.watchGit(m.root)
			}
			if m.tree.LoadState == LoadLoaded {
				return m, directoryGitCmd(m.git, m.tree, m.loadGeneration[m.tree.Path])
			}
		}
		return m, nil
	case directoryGitMsg:
		m.applyDirectoryGit(msg)
		return m, nil
	case previewMsg:
		m.applyPreview(msg, msg.openToChange)
		return m, nil
	case watchMsg:
		if m.watcher != nil && m.watcher.isGitPath(msg.path) {
			m.gitGeneration++
			cmds = append(cmds, gitCmd(m.root, m.gitGeneration), m.watcher.next())
			return m, tea.Batch(cmds...)
		}
		node := findNode(m.tree, filepath.Dir(msg.path))
		if node == nil || !node.Dir {
			node = m.tree
		}
		cmds = append(cmds, m.startDirectoryLoad(node))
		m.gitGeneration++
		cmds = append(cmds, gitCmd(m.root, m.gitGeneration))
		if m.watcher != nil {
			cmds = append(cmds, m.watcher.next())
		}
		return m, tea.Batch(cmds...)
	case tea.PasteMsg:
		if m.finding {
			m.find.SetValue(m.find.Value() + msg.String())
			m.applyFind()
		}
		return m, nil
	case tea.MouseWheelMsg:
		mouse := msg.Mouse()
		// Touchpads emit the same wheel events as a physical mouse. Route them
		// by the region under the pointer, so the explorer and preview scroll
		// independently without requiring a focus change.
		if mouse.Y > 0 && mouse.Y <= m.treeHeight {
			if mouse.Button == tea.MouseWheelUp {
				m.treeOffset = max(0, m.treeOffset-3)
			} else {
				m.treeOffset = min(max(0, len(m.visible())-m.treeHeight), m.treeOffset+3)
			}
			return m, nil
		}
		m.viewport, _ = m.viewport.Update(msg)
		return m, nil
	case tea.MouseClickMsg:
		mouse := msg.Mouse()
		if mouse.Y > 0 && mouse.Y <= m.treeHeight {
			nodes := m.visible()
			idx := m.treeOffset + mouse.Y - 1
			if idx >= 0 && idx < len(nodes) {
				node := nodes[idx]
				m.selected = node.Path
				if node.Dir {
					cmds = append(cmds, m.toggleDirectory(node))
				} else {
					m.focusPreview = true
					cmds = append(cmds, m.requestPreview(true))
				}
			}
			return m, tea.Batch(cmds...)
		}
		if mouse.Y > m.treeHeight && mouse.Button == tea.MouseLeft && m.focusPreview {
			m.selectionStart, m.selectionEnd, m.selecting = m.viewport.YOffset()+mouse.Y-m.treeHeight-1, m.viewport.YOffset()+mouse.Y-m.treeHeight-1, true
			return m, nil
		}
	case tea.MouseMotionMsg:
		if m.selecting && m.focusPreview {
			m.selectionEnd = m.viewport.YOffset() + msg.Mouse().Y - m.treeHeight - 1
			return m, nil
		}
	case tea.MouseReleaseMsg:
		m.selecting = false
	case tea.KeyPressMsg:
		k := msg.String()
		if m.finding {
			if k == "esc" || k == "enter" {
				m.finding = false
				m.find.Blur()
				return m, nil
			}
			m.find, _ = m.find.Update(msg)
			m.applyFind()
			return m, nil
		}
		if k == "q" {
			if m.watcher != nil {
				m.watcher.close()
			}
			return m, tea.Quit
		}
		if k == "tab" {
			m.focusPreview = !m.focusPreview
			return m, nil
		}
		if k == "/" {
			// A new search starts from a clean query. Keep the prior query only
			// while its input remains open, so reopening find after Enter/Esc
			// never carries stale highlights into the next search.
			m.find.Reset()
			m.findMatches, m.findIndex = nil, 0
			m.viewport.ClearHighlights()
			m.viewport.SetContent(m.renderedPreview)
			m.finding = true
			m.focusPreview = true
			focus := m.find.Focus()
			if n := m.selectedNode(); n != nil && !n.Dir && m.previewPath != n.Path {
				// Startup avoids preview I/O. A search is an explicit request for
				// the selected file's content, so load it before matching.
				return m, tea.Batch(focus, m.requestPreview(false))
			}
			m.applyFind()
			return m, focus
		}
		if k == "r" {
			cmds = append(cmds, m.startDirectoryLoad(m.tree))
			m.gitGeneration++
			cmds = append(cmds, gitCmd(m.root, m.gitGeneration))
			return m, tea.Batch(cmds...)
		}
		if k == "d" {
			n := m.selectedNode()
			if n != nil && !n.Dir {
				m.diffMode = !m.diffMode
				return m, m.requestPreview(false)
			}
			return m, nil
		}
		if m.focusPreview {
			if k == "n" {
				m.nextFind(1)
				return m, nil
			}
			if k == "N" {
				m.nextFind(1)
				return m, nil
			}
			if k == "y" || k == "ctrl+c" {
				if text := m.selectedText(); text != "" {
					return m, tea.SetClipboard(text)
				}
			}
			if k == "shift+up" || k == "shift+down" {
				if m.selectionStart < 0 {
					m.selectionStart = m.viewport.YOffset()
					m.selectionEnd = m.selectionStart
				}
				if k == "shift+up" {
					m.selectionEnd = max(0, m.selectionEnd-1)
				} else {
					m.selectionEnd = min(len(m.previewLines)-1, m.selectionEnd+1)
				}
				return m, nil
			}
			m.viewport, _ = m.viewport.Update(msg)
			return m, nil
		}
		return m, m.moveTree(k)
	}
	return m, tea.Batch(cmds...)
}

func (m *model) toggleDirectory(n *Node) tea.Cmd {
	if !n.Dir {
		return nil
	}
	n.Expanded = !n.Expanded
	if !n.Expanded {
		for path, loader := range m.loaders {
			if path == n.Path || strings.HasPrefix(path, n.Path+string(filepath.Separator)) {
				loader.close()
				delete(m.loaders, path)
				m.loadGeneration[path]++
			}
		}
		if m.watcher != nil {
			m.watcher.unwatchBelow(n.Path)
		}
		m.rebuildRows()
		return nil
	}
	m.rebuildRows()
	if n.LoadState == LoadUnloaded || n.LoadState == LoadFailed {
		return m.startDirectoryLoad(n)
	}
	if m.watcher != nil {
		m.watcher.watch(n.Path)
	}
	return nil
}

func (m *model) moveTree(k string) tea.Cmd {
	nodes := m.visible()
	if len(nodes) == 0 {
		m.selected = m.root
		return nil
	}
	idx := 0
	for i, n := range nodes {
		if n.Path == m.selected {
			idx = i
			break
		}
	}
	openToChange := false
	switch k {
	case "up", "k":
		idx = max(0, idx-1)
	case "down", "j":
		idx = min(len(nodes)-1, idx+1)
	case "right":
		if n := nodes[idx]; n.Dir {
			if !n.Expanded {
				return m.toggleDirectory(n)
			}
		}
	case "left":
		if n := nodes[idx]; n.Dir && n.Expanded {
			return m.toggleDirectory(n)
		} else if n.Parent != nil {
			m.selected = n.Parent.Path
		}
	case "enter":
		if n := nodes[idx]; n.Dir {
			return m.toggleDirectory(n)
		} else {
			m.focusPreview = true
			openToChange = true
		}
	}
	if len(nodes) > 0 && (k == "up" || k == "k" || k == "down" || k == "j") {
		m.selected = nodes[idx].Path
	}
	return m.requestPreview(openToChange)
}

func (m model) selectedText() string {
	if m.selectionStart < 0 || m.selectionEnd < 0 {
		return ""
	}
	a, b := m.selectionStart, m.selectionEnd
	if a > b {
		a, b = b, a
	}
	a = max(0, a)
	b = min(len(m.previewLines)-1, b)
	if a > b {
		return ""
	}
	return strings.Join(m.previewLines[a:b+1], "\n")
}

func depth(n *Node) int {
	d := 0
	for n.Parent != nil {
		d++
		n = n.Parent
	}
	return d
}
func (m model) View() tea.View {
	if m.tree == nil {
		return tea.NewView("Loading…")
	}
	nodes := m.visible()
	if len(nodes) > m.treeHeight {
		m.treeOffset = min(m.treeOffset, max(0, len(nodes)-m.treeHeight))
	}
	var lines []string
	for i := m.treeOffset; i < len(nodes) && len(lines) < m.treeHeight; i++ {
		n := nodes[i]
		icon := "  "
		if n.Dir {
			if n.Expanded {
				icon = "▾ "
			} else {
				icon = "▸ "
			}
		}
		name := strings.Repeat("  ", max(0, depth(n)-1)) + icon + n.Name
		if n.Dir && (n.LoadState == LoadLoading || n.LoadState == LoadPartial) {
			name += " …"
		}
		if n.Symlink {
			name += " @"
		}
		name = truncate(name, max(1, m.width))
		st := lipgloss.NewStyle()
		switch n.Status {
		case StatusChanged:
			st = yellowStyle
		case StatusAdded:
			st = greenStyle
		case StatusDeleted:
			st = redStyle
		}
		rendered := st.Render(name)
		if n.Path == m.selected && !m.focusPreview {
			rendered = selectedStyle.Render(name)
		}
		lines = append(lines, rendered)
	}
	for len(lines) < m.treeHeight {
		lines = append(lines, "")
	}
	title := cyanStyle.Render("navigator ") + mutedStyle.Render(m.root)
	if m.tree.LoadState == LoadLoading || m.tree.LoadState == LoadPartial {
		title += " " + mutedStyle.Render("loading…")
	}
	if m.git.RepoRoot != "" {
		title += " " + mutedStyle.Render("git")
	}
	previewLabel := "Preview"
	if m.diffMode {
		previewLabel = "Diff"
	}
	if node := m.selectedNode(); node != nil && !node.Dir {
		previewLabel += "  " + node.Rel
	}
	previewLabel = truncate(previewLabel, max(1, m.width))
	previewTitle := mutedStyle.Render(previewLabel)
	if m.diffMode {
		previewTitle = yellowStyle.Render(previewLabel)
	}
	if m.focusPreview {
		previewTitle = selectedStyle.Render(" " + previewLabel + " ")
	}
	footer := m.help.View(m.keys)
	if m.finding {
		footer = m.find.View() + "  " + mutedStyle.Render("Enter/esc to close")
	}
	content := strings.Join([]string{title, strings.Join(lines, "\n"), previewTitle, m.viewport.View(), footer}, "\n")
	v := tea.NewView(content)
	v.MouseMode = tea.MouseModeAllMotion
	v.AltScreen = true
	return v
}

func truncate(s string, width int) string {
	if lipgloss.Width(s) <= width {
		return s
	}
	return s[:max(0, width-1)] + "…"
}
