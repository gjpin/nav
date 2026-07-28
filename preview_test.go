package main

import (
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/alecthomas/chroma/v2"
)

func TestPreviewLexerSelection(t *testing.T) {
	for _, path := range []string{
		"README.md", "app.js", "component.tsx", "main.go", "lib.rs",
		"tool.py", "script.sh", "config.yaml", "data.json", "Dockerfile",
		"Dockerfile.dev",
	} {
		if lexer := previewLexer(path); lexer == nil {
			t.Errorf("previewLexer(%q) = nil", path)
		}
	}
	if lexer := previewLexer("notes.txt"); lexer != nil {
		t.Errorf("previewLexer for unknown extension = %s", lexer.Config().Name)
	}
}

func TestPreviewStyleUsesVSCodeDarkModernColors(t *testing.T) {
	for token, want := range map[chroma.TokenType]string{
		chroma.Comment:       "#6a9955",
		chroma.Keyword:       "#569cd6",
		chroma.LiteralNumber: "#b5cea8",
		chroma.LiteralString: "#ce9178",
		chroma.NameFunction:  "#dcdcaa",
		chroma.NameVariable:  "#9cdcfe",
	} {
		if got := previewStyle.Get(token).Colour.String(); got != want {
			t.Errorf("%s color = %s, want %s", token, got, want)
		}
	}
}

func TestRenderPreviewForSupportedLanguages(t *testing.T) {
	for _, test := range []struct {
		path, source string
	}{
		{"README.md", "# Heading\n"},
		{"app.js", "const value = 1;\n"},
		{"app.ts", "const value: number = 1;\n"},
		{"main.go", "package main\n"},
		{"lib.rs", "fn main() {}\n"},
		{"tool.py", "def main():\n    pass\n"},
		{"script.sh", "echo hello\n"},
		{"config.yaml", "name: navigator\n"},
		{"data.json", "{\"name\": \"navigator\"}\n"},
		{"Dockerfile", "FROM alpine\n"},
	} {
		t.Run(test.path, func(t *testing.T) {
			rendered := renderPreview(test.path, test.source)
			if rendered == test.source || !strings.Contains(rendered, "\x1b[") {
				t.Fatalf("expected ANSI rendering, got %q", rendered)
			}
			if got := stripANSI(rendered); got != test.source {
				t.Fatalf("stripped preview = %q, want %q", got, test.source)
			}
		})
	}
}

func TestRenderPreviewPreservesSourceAndMapsFindRanges(t *testing.T) {
	source := "package main\n\nfunc main() {\n\tprintln(\"hello\")\n}\n"
	rendered := renderPreview("main.go", source)
	if rendered == source {
		t.Fatal("expected ANSI syntax highlighting")
	}
	if got := stripANSI(rendered); got != source {
		t.Fatalf("stripped preview = %q, want %q", got, source)
	}

	start := strings.Index(source, "main()")
	rangeInRendered := rawRangeToRendered(source, rendered, start, start+len("main"))
	if got := stripANSI(rendered[rangeInRendered[0]:rangeInRendered[1]]); got != "main" {
		t.Fatalf("mapped range renders %q, want main", got)
	}
}

func TestRenderPreviewFallsBackForUnknownAndFailingLexers(t *testing.T) {
	source := "plain text\n"
	if got := renderPreview("notes.txt", source); got != source {
		t.Fatalf("unknown file rendered as %q", got)
	}
	if got := renderPreviewWithLexer(failingLexer{}, source); got != source {
		t.Fatalf("failing lexer rendered as %q", got)
	}
	original := previewFormatter
	previewFormatter = chroma.FormatterFunc(func(io.Writer, *chroma.Style, chroma.Iterator) error {
		return errors.New("format failed")
	})
	t.Cleanup(func() { previewFormatter = original })
	if got := renderPreview("file.go", source); got != source {
		t.Fatalf("failing formatter rendered as %q", got)
	}
}

type failingLexer struct{}

func (failingLexer) Config() *chroma.Config { return &chroma.Config{Name: "failing"} }
func (failingLexer) Tokenise(*chroma.TokeniseOptions, string) (chroma.Iterator, error) {
	return nil, errors.New("tokenise failed")
}
func (failingLexer) SetRegistry(*chroma.LexerRegistry) chroma.Lexer { return failingLexer{} }
func (failingLexer) SetAnalyser(func(string) float32) chroma.Lexer  { return failingLexer{} }
func (failingLexer) AnalyseText(string) float32                     { return 0 }
