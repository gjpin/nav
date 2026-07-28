// navigator is a read-only, Git-aware terminal file explorer.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	tea "charm.land/bubbletea/v2"
)

func main() {
	flag.Usage = func() { fmt.Fprintln(os.Stderr, "Usage: navigator [directory]\n\nRead-only Git-aware file explorer.") }
	help := flag.Bool("help", false, "show help")
	flag.Parse()
	if *help {
		flag.Usage()
		return
	}
	if flag.NArg() > 1 {
		flag.Usage()
		os.Exit(2)
	}
	root := "."
	if flag.NArg() == 1 {
		root = flag.Arg(0)
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		fail(err)
	}
	info, err := os.Stat(abs)
	if err != nil || !info.IsDir() {
		if err == nil {
			err = errors.New("not a directory")
		}
		fail(fmt.Errorf("navigator: %s: %w", root, err))
	}
	m, err := newModel(abs)
	if err != nil {
		fail(fmt.Errorf("navigator: %w", err))
	}
	if _, err := tea.NewProgram(m).Run(); err != nil {
		fail(err)
	}
}

func fail(err error) { fmt.Fprintln(os.Stderr, err); os.Exit(1) }
