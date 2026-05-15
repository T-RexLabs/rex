package web

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html/template"

	"github.com/alecthomas/chroma/v2"
	chromahtml "github.com/alecthomas/chroma/v2/formatters/html"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
)

// Highlighter renders source as syntax-highlighted HTML.
//
// Why server-side: keeps the JS-disabled view fully readable
// (web-ui.ACCESS.3) and avoids shipping a 30KB+ JS highlighter on
// every page. Chroma is pure Go, no cgo (overview.ENG.2).
//
// All HTML output is wrapped via template.HTML so html/template
// passes it through unescaped. Source bytes are first-class user
// content; chroma escapes them on the way through, so the only
// way unsafe HTML reaches the page is if chroma itself were
// compromised.
//
// Two chroma styles are held: a light one for the default palette
// and a dark one wrapped in @media (prefers-color-scheme: dark)
// so the highlighter colors track the system color scheme.
// Without this, the keys/values in JSON and YAML render in the
// light theme's blues and reds against the dark UI surface,
// where they're nearly invisible.
type Highlighter struct {
	formatter  *chromahtml.Formatter
	styleLight *chroma.Style
	styleDark  *chroma.Style
}

// NewHighlighter constructs the package-wide Highlighter. The
// formatter omits inline style attributes so colors come from
// app.css class rules — that lets the dark/light palette swap
// follow prefers-color-scheme. The light style is `github` (high
// contrast, calm); the dark style is `github-dark` (same palette
// family, designed for dark backgrounds).
func NewHighlighter() *Highlighter {
	return &Highlighter{
		formatter: chromahtml.New(
			chromahtml.WithClasses(true),
			chromahtml.PreventSurroundingPre(true),
			chromahtml.TabWidth(2),
		),
		styleLight: styles.Get("github"),
		styleDark:  styles.Get("github-dark"),
	}
}

// HighlightYAML renders src as highlighted YAML. Returns the
// rendered HTML wrapped in template.HTML so callers can drop it
// straight into a template via {{.Foo}} when .Foo is template.HTML.
func (h *Highlighter) HighlightYAML(src string) template.HTML {
	return h.highlight(src, "yaml")
}

// HighlightJSON pretty-prints src (a single JSON value) and
// renders the result as highlighted JSON. Bytes that don't parse
// as JSON are returned escaped as a code span — never written raw,
// so a malformed payload can't break the page or inject HTML.
func (h *Highlighter) HighlightJSON(src []byte) template.HTML {
	if len(src) == 0 {
		return ""
	}
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, src, "", "  "); err != nil {
		// Not valid JSON — fall back to a plain escaped string
		// so the user still sees what was on disk.
		return template.HTML(template.HTMLEscapeString(string(src)))
	}
	return h.highlight(pretty.String(), "json")
}

// highlight runs src through the named lexer and the configured
// formatter, returning class-based HTML. The output is meant to
// land inside a <pre><code class="chroma">...</code></pre> wrapper
// in the template; the template adds the wrapper so the formatter's
// PreventSurroundingPre option keeps the inner output clean.
//
// Tokenisation uses the light style as the formatting reference;
// since chroma's class-based output records token *kinds* (e.g.
// `nt`, `s2`, `mi`) rather than literal colors, the same HTML
// works for both palettes and the @media-scoped dark CSS swaps
// the colors at render time.
func (h *Highlighter) highlight(src, lang string) template.HTML {
	lexer := lexers.Get(lang)
	if lexer == nil {
		lexer = lexers.Fallback
	}
	lexer = chroma.Coalesce(lexer)

	iter, err := lexer.Tokenise(nil, src)
	if err != nil {
		return template.HTML(template.HTMLEscapeString(src))
	}
	var buf bytes.Buffer
	if err := h.formatter.Format(&buf, h.styleLight, iter); err != nil {
		return template.HTML(template.HTMLEscapeString(src))
	}
	return template.HTML(buf.String())
}

// PrettyJSON returns src indented with two-space steps, or the
// original bytes if src is not valid JSON. Used when we need the
// pretty form as a string (e.g. for SSE wire emission of a
// single-row update where the highlighted HTML is rendered
// upstream).
func PrettyJSON(src []byte) string {
	var buf bytes.Buffer
	if err := json.Indent(&buf, src, "", "  "); err != nil {
		return string(src)
	}
	return buf.String()
}

// HighlightCSS returns the chroma CSS for both light and dark
// styles. The light rules are top-level; the dark rules are
// wrapped in @media (prefers-color-scheme: dark) so browsers in
// dark mode use them automatically. Both palettes use the same
// chroma class names so a single rendered HTML document works
// for either.
func (h *Highlighter) HighlightCSS() string {
	var buf bytes.Buffer

	if err := h.formatter.WriteCSS(&buf, h.styleLight); err != nil {
		return fmt.Sprintf("/* chroma light css failed: %s */\n", err)
	}

	buf.WriteString("\n@media (prefers-color-scheme: dark) {\n")
	if err := h.formatter.WriteCSS(&buf, h.styleDark); err != nil {
		fmt.Fprintf(&buf, "/* chroma dark css failed: %s */\n", err)
	}
	buf.WriteString("}\n")

	return buf.String()
}
