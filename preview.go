package main

import (
	"bytes"
	"path/filepath"
	"strings"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/formatters"
	"github.com/alecthomas/chroma/v2/lexers"
)

// previewLexer returns a lexer only for the file types Navigator deliberately
// highlights. Letting Chroma guess every extension produces surprising results
// for plain text files, so unknown files are intentionally left untouched.
func previewLexer(path string) chroma.Lexer {
	name := strings.ToLower(filepath.Base(path))
	if name == "dockerfile" || strings.HasPrefix(name, "dockerfile.") {
		return lexers.Get("dockerfile")
	}

	switch strings.ToLower(filepath.Ext(name)) {
	case ".md", ".markdown", ".mdown", ".mkdn":
		return lexers.Get("markdown")
	case ".js", ".mjs", ".cjs", ".jsx":
		return lexers.Get("javascript")
	case ".ts", ".tsx":
		return lexers.Get("typescript")
	case ".go":
		return lexers.Get("go")
	case ".rs":
		return lexers.Get("rust")
	case ".py":
		return lexers.Get("python")
	case ".sh", ".bash", ".zsh", ".fish":
		return lexers.Get("bash")
	case ".yaml", ".yml":
		return lexers.Get("yaml")
	case ".json":
		return lexers.Get("json")
	}
	return nil
}

var (
	// terminal16m keeps the theme's actual RGB values instead of approximating
	// them to the 256-colour palette.
	previewFormatter = formatters.Get("terminal16m")
	// VS Code's Dark Modern theme inherits its token colors from Dark+. Chroma
	// token classes are not TextMate scopes, so these map the corresponding
	// broad categories used by the supported lexers.
	previewStyle = chroma.MustNewStyle("vscode-dark-modern", chroma.StyleEntries{
		chroma.Background:           "bg:#1f1f1f",
		chroma.Text:                 "#cccccc",
		chroma.Error:                "#f44747",
		chroma.Comment:              "#6a9955",
		chroma.Keyword:              "#569cd6",
		chroma.KeywordConstant:      "#569cd6",
		chroma.KeywordDeclaration:   "#569cd6",
		chroma.KeywordNamespace:     "#569cd6",
		chroma.KeywordType:          "#4ec9b0",
		chroma.Name:                 "#9cdcfe",
		chroma.NameAttribute:        "#9cdcfe",
		chroma.NameBuiltin:          "#4ec9b0",
		chroma.NameClass:            "#4ec9b0",
		chroma.NameConstant:         "#4fc1ff",
		chroma.NameDecorator:        "#dcdcaa",
		chroma.NameFunction:         "#dcdcaa",
		chroma.NameTag:              "#569cd6",
		chroma.NameVariable:         "#9cdcfe",
		chroma.Literal:              "#ce9178",
		chroma.LiteralNumber:        "#b5cea8",
		chroma.LiteralString:        "#ce9178",
		chroma.LiteralStringEscape:  "#d7ba7d",
		chroma.LiteralStringRegex:   "#d16969",
		chroma.Operator:             "#cccccc",
		chroma.Punctuation:          "#cccccc",
		chroma.GenericDeleted:       "#ce9178",
		chroma.GenericHeading:       "bold #569cd6",
		chroma.GenericInserted:      "#b5cea8",
		chroma.GenericStrong:        "bold",
		chroma.GenericEmph:          "italic",
		chroma.GenericSubheading:    "#569cd6",
		chroma.GenericUnderline:     "underline",
		chroma.TextSymbol:           "#6796e6",
		chroma.LiteralStringBoolean: "#569cd6",
	})
)

// renderPreview produces the ANSI form used by the viewport. The raw preview
// remains the authoritative representation for all file-oriented operations.
func renderPreview(path, source string) string {
	return renderPreviewWithLexer(previewLexer(path), source)
}

func renderPreviewWithLexer(lexer chroma.Lexer, source string) string {
	if lexer == nil || previewFormatter == nil || previewStyle == nil {
		return source
	}
	iterator, err := lexer.Tokenise(nil, source)
	if err != nil {
		return source
	}
	var rendered bytes.Buffer
	if err := previewFormatter.Format(&rendered, previewStyle, iterator); err != nil {
		return source
	}
	result := rendered.String()
	// Formatting should never change source text. Keep the preview usable even
	// if a lexer or formatter does something unexpected.
	if stripANSI(result) != source {
		return source
	}
	return result
}

// rawRangeToRendered maps byte offsets in the raw source to byte offsets in
// terminal-formatted content. The viewport expects offsets in its displayed
// string, while find operates on the unformatted source.
func rawRangeToRendered(raw, rendered string, start, end int) []int {
	if start < 0 || end < start || end > len(raw) || raw == rendered {
		return []int{start, end}
	}

	starts := make([]int, len(raw))
	ends := make([]int, len(raw)+1)
	ansi, source := 0, 0
	for ansi < len(rendered) && source < len(raw) {
		if next, ok := ansiEscapeEnd(rendered, ansi); ok {
			ansi = next
			continue
		}
		if rendered[ansi] != raw[source] {
			return []int{start, end}
		}
		starts[source] = ansi
		ansi++
		source++
		ends[source] = ansi
	}
	if source != len(raw) || stripANSI(rendered) != raw {
		return []int{start, end}
	}
	if start == len(raw) {
		start = len(rendered)
	} else {
		start = starts[start]
	}
	return []int{start, ends[end]}
}

func ansiEscapeEnd(s string, start int) (int, bool) {
	if start+1 >= len(s) || s[start] != '\x1b' || s[start+1] != '[' {
		return start, false
	}
	for i := start + 2; i < len(s); i++ {
		if s[i] >= 0x40 && s[i] <= 0x7e {
			return i + 1, true
		}
	}
	return start, false
}

func stripANSI(s string) string {
	var plain strings.Builder
	for i := 0; i < len(s); {
		if next, ok := ansiEscapeEnd(s, i); ok {
			i = next
			continue
		}
		plain.WriteByte(s[i])
		i++
	}
	return plain.String()
}
