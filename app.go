package main

import (
	"fmt"
	"os"
	"path/filepath"
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
	Up, Down, Toggle, Open, Preview, Diff, Find, Next, Previous, Refresh, Quit, Copy key.Binding
}

func newKeyMap() keyMap {
	return keyMap{
		Up: key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/k", "move")), Down: key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓/j", "move")),
		Toggle: key.NewBinding(key.WithKeys("left", "right"), key.WithHelp("←/→", "collapse/expand")), Open: key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "open")),
		Preview: key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "preview")), Diff: key.NewBinding(key.WithKeys("d"), key.WithHelp("d", "diff")),
		Find: key.NewBinding(key.WithKeys("/"), key.WithHelp("/", "find")), Next: key.NewBinding(key.WithKeys("n"), key.WithHelp("n/N", "match")),
		Previous: key.NewBinding(key.WithKeys("N"), key.WithHelp("N", "previous match")), Refresh: key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "refresh")),
		Quit: key.NewBinding(key.WithKeys("q", "ctrl+c"), key.WithHelp("q", "quit")), Copy: key.NewBinding(key.WithKeys("y", "ctrl+c"), key.WithHelp("y", "copy selection")),
	}
}

func (k keyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Up, k.Open, k.Preview, k.Diff, k.Find, k.Refresh, k.Quit}
}
func (k keyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{k.ShortHelp(), {k.Copy, k.Next, k.Previous}}
}

type snapshot struct {
	tree *Node
	git  gitInfo
}
type scanMsg struct {
	serial int
	data   snapshot
	err    error
}
type watchMsg struct{}

type fileWatcher struct {
	w       *fsnotify.Watcher
	changes chan struct{}
	done    chan struct{}
	once    sync.Once
	mu      sync.Mutex
	watched map[string]bool
}

func newFileWatcher(root string) *fileWatcher {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return nil
	}
	fw := &fileWatcher{w: w, changes: make(chan struct{}, 1), done: make(chan struct{}), watched: make(map[string]bool)}
	fw.watch(root)
	go fw.loop()
	return fw
}

// watch adds newly discovered directories after a scan. Existing watches are
// retained; fsnotify will report changes from ignored paths as well.
func (f *fileWatcher) watch(root string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var add func(string)
	add = func(dir string) {
		if f.watched[dir] {
			return
		}
		if err := f.w.Add(dir); err != nil {
			return
		}
		f.watched[dir] = true
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range entries {
			if e.Name() == ".git" || !e.IsDir() || e.Type()&os.ModeSymlink != 0 {
				continue
			}
			add(filepath.Join(dir, e.Name()))
		}
	}
	add(root)
}

func (f *fileWatcher) loop() {
	var timer *time.Timer
	var timerC <-chan time.Time
	for {
		select {
		case <-f.done:
			if timer != nil {
				timer.Stop()
			}
			_ = f.w.Close()
			return
		case _, ok := <-f.w.Events:
			if !ok {
				return
			}
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
			case f.changes <- struct{}{}:
			default:
			}
			timerC = nil
		case <-f.w.Errors:
		}
	}
}
func (f *fileWatcher) next() tea.Cmd { return func() tea.Msg { <-f.changes; return watchMsg{} } }
func (f *fileWatcher) close()        { f.once.Do(func() { close(f.done) }) }

type model struct {
	root                            string
	tree                            *Node
	git                             gitInfo
	selected                        string
	treeOffset                      int
	width, height, treeHeight       int
	focusPreview, diffMode, finding bool
	preview                         string
	renderedPreview                 string
	previewLines                    []string
	added                           map[int]bool
	changed                         map[int]bool
	removed                         map[int]int
	selectionStart, selectionEnd    int
	selecting                       bool
	viewport                        viewport.Model
	find                            textinput.Model
	help                            help.Model
	keys                            keyMap
	watcher                         *fileWatcher
	serial                          int
}

func scan(root string, expanded map[string]bool) (snapshot, error) {
	git := inspectGit(root)
	tree, err := BuildTree(root, git.Statuses, expanded)
	return snapshot{tree: tree, git: git}, err
}

func newModel(root string) (model, error) {
	data, err := scan(root, map[string]bool{})
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
	selected := root
	if nodes := visibleNodes(data.tree); len(nodes) > 0 {
		selected = nodes[0].Path
	}
	m := model{root: root, tree: data.tree, git: data.git, selected: selected, viewport: v, find: f, help: help.New(), keys: newKeyMap(), watcher: newFileWatcher(root), selectionStart: -1, selectionEnd: -1}
	m.loadPreview(false)
	return m, nil
}

func (m model) Init() tea.Cmd {
	if m.watcher == nil {
		return nil
	}
	return m.watcher.next()
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
	for _, n := range visibleNodes(m.tree) {
		if n.Path == m.selected {
			return n
		}
	}
	return m.tree
}

// loadPreview refreshes the current file. openToChange is reserved for an
// explicit file open; routine tree movement and filesystem refreshes retain
// the preview's current scroll position.
func (m *model) loadPreview(openToChange bool) {
	n := m.selectedNode()
	if n == nil {
		return
	}
	var text string
	if m.diffMode {
		text, _ = gitDiff(m.git, n)
	} else {
		text, _ = readPreview(n)
	}
	if m.diffMode && text == "" {
		text = "No diff against HEAD."
	}
	m.preview = text
	m.previewLines = strings.Split(text, "\n")
	m.added, m.changed, m.removed = nil, nil, nil
	if !m.diffMode {
		diff, _ := gitDiff(m.git, n)
		m.added, m.changed, m.removed = diffLineMarks(diff, len(m.previewLines))
		// Give a deleted-only range at EOF a concrete line on which to render
		// its red removal-count gutter.
		if m.removed[len(m.previewLines)] > 0 {
			m.previewLines = append(m.previewLines, "")
			m.preview = strings.Join(m.previewLines, "\n")
		}
	}
	m.renderedPreview = m.preview
	if !m.diffMode {
		m.renderedPreview = renderPreview(n.Path, m.preview)
	}
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
	m.viewport.ClearHighlights()
	needle := m.find.Value()
	if needle == "" {
		return
	}
	var ranges [][]int
	for start := 0; ; {
		i := strings.Index(m.preview[start:], needle)
		if i < 0 {
			break
		}
		a := start + i
		ranges = append(ranges, rawRangeToRendered(m.preview, m.renderedPreview, a, a+len(needle)))
		start = a + len(needle)
	}
	m.viewport.SetHighlights(ranges)
}

func scanCmd(root string, serial int, expanded map[string]bool) tea.Cmd {
	return func() tea.Msg {
		data, err := scan(root, expanded)
		return scanMsg{serial: serial, data: data, err: err}
	}
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
	case scanMsg:
		if msg.serial != m.serial || msg.err != nil {
			return m, nil
		}
		m.tree, m.git = msg.data.tree, msg.data.git
		if m.watcher != nil {
			m.watcher.watch(m.root)
		}
		valid := false
		for _, node := range visibleNodes(m.tree) {
			if node.Path == m.selected {
				valid = true
				break
			}
		}
		if !valid {
			nodes := visibleNodes(m.tree)
			m.selected = m.root
			if len(nodes) > 0 {
				m.selected = nodes[0].Path
			}
		}
		m.loadPreview(false)
		return m, nil
	case watchMsg:
		m.serial++
		cmds = append(cmds, scanCmd(m.root, m.serial, m.expanded()))
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
				m.treeOffset = min(max(0, len(visibleNodes(m.tree))-m.treeHeight), m.treeOffset+3)
			}
			return m, nil
		}
		m.viewport, _ = m.viewport.Update(msg)
		return m, nil
	case tea.MouseClickMsg:
		mouse := msg.Mouse()
		if mouse.Y > 0 && mouse.Y <= m.treeHeight {
			nodes := visibleNodes(m.tree)
			idx := m.treeOffset + mouse.Y - 1
			if idx >= 0 && idx < len(nodes) {
				node := nodes[idx]
				m.selected = node.Path
				if node.Dir {
					node.Expanded = !node.Expanded
				} else {
					m.focusPreview = true
				}
				m.loadPreview(!node.Dir)
			}
			return m, nil
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
			m.finding = true
			return m, m.find.Focus()
		}
		if k == "r" {
			m.serial++
			return m, scanCmd(m.root, m.serial, m.expanded())
		}
		if k == "d" {
			n := m.selectedNode()
			if n != nil && !n.Dir {
				m.diffMode = !m.diffMode
				m.loadPreview(false)
			}
			return m, nil
		}
		if m.focusPreview {
			if k == "n" {
				m.viewport.HighlightNext()
				return m, nil
			}
			if k == "N" {
				m.viewport.HighlightPrevious()
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
		m.moveTree(k)
	}
	return m, tea.Batch(cmds...)
}

func (m *model) moveTree(k string) {
	nodes := visibleNodes(m.tree)
	if len(nodes) == 0 {
		m.selected = m.root
		m.loadPreview(false)
		return
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
			n.Expanded = true
		}
	case "left":
		if n := nodes[idx]; n.Dir && n.Expanded {
			n.Expanded = false
		} else if n.Parent != nil {
			m.selected = n.Parent.Path
		}
	case "enter":
		if n := nodes[idx]; n.Dir {
			n.Expanded = !n.Expanded
		} else {
			m.focusPreview = true
			openToChange = true
		}
	}
	if len(nodes) > 0 && (k == "up" || k == "k" || k == "down" || k == "j") {
		m.selected = nodes[idx].Path
	}
	m.loadPreview(openToChange)
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
	nodes := visibleNodes(m.tree)
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
