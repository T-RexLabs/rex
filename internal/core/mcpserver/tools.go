package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/asabla/rex/internal/core/event"
	"github.com/asabla/rex/internal/core/harnessbrief"
	"github.com/asabla/rex/internal/core/runner"
	"github.com/asabla/rex/internal/core/specfmt"
	"github.com/asabla/rex/internal/core/specverify"
	"github.com/asabla/rex/internal/core/storage/eventlog"
)

// WorkspaceTools registers the v1 read-mostly tool surface
// (tools.INTROSPECT.3) on srv. Every handler is stateless and
// re-reads the workspace's current state per call so the model
// always sees fresh content.
func WorkspaceTools(srv *Server, workspaceRoot string) {
	srv.Register(Tool{
		Name:        "rex.workspace.brief",
		Description: "Fetch the same workspace context primer Rex injects at session/new. Use this to refresh your understanding of the current workspace mid-session.",
		InputSchema: jsonRawObject(map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		}),
	}, func(_ context.Context, _ json.RawMessage) (Result, error) {
		brief, _ := harnessbrief.Build(harnessbrief.Options{WorkspaceRoot: workspaceRoot})
		return TextResult(brief), nil
	})

	srv.Register(Tool{
		Name:        "rex.spec.list",
		Description: "List every spec in the workspace with id, name, and lifecycle state. Returns a Markdown bullet list.",
		InputSchema: jsonRawObject(map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		}),
	}, func(_ context.Context, _ json.RawMessage) (Result, error) {
		return TextResult(renderSpecList(workspaceRoot)), nil
	})

	srv.Register(Tool{
		Name:        "rex.spec.read",
		Description: "Fetch a spec's full YAML body by id. Returns the file contents verbatim.",
		InputSchema: jsonRawObject(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id": map[string]any{"type": "string", "description": "The spec's metadata.id (kebab-case)."},
			},
			"required": []string{"id"},
		}),
	}, func(_ context.Context, args json.RawMessage) (Result, error) {
		var p struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(args, &p); err != nil {
			return ErrorResult("invalid arguments: " + err.Error()), nil
		}
		if p.ID == "" {
			return ErrorResult("id is required"), nil
		}
		body, err := os.ReadFile(filepath.Join(workspaceRoot, ".rex", "specs", p.ID+".yaml"))
		if err != nil {
			return ErrorResult(err.Error()), nil
		}
		return TextResult(string(body)), nil
	})

	srv.Register(Tool{
		Name:        "rex.spec.validate",
		Description: "Run the schema validator. With no id, validates every spec in the workspace; with an id, validates just that one. Returns the issue list as Markdown.",
		InputSchema: jsonRawObject(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id": map[string]any{"type": "string", "description": "Optional spec id; omit to validate every spec."},
			},
		}),
	}, func(_ context.Context, args json.RawMessage) (Result, error) {
		var p struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(args, &p)
		out, err := runValidate(workspaceRoot, p.ID)
		if err != nil {
			return ErrorResult(err.Error()), nil
		}
		return TextResult(out), nil
	})

	srv.Register(Tool{
		Name:        "rex.spec.verify",
		Description: "Exercise structured proof entries against on-disk evidence (paths exist, test funcs grep, ACIDs resolve). Without an id, verifies every spec.",
		InputSchema: jsonRawObject(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id": map[string]any{"type": "string", "description": "Optional spec id; omit to verify every spec."},
			},
		}),
	}, func(_ context.Context, args json.RawMessage) (Result, error) {
		var p struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(args, &p)
		out, err := runVerify(workspaceRoot, p.ID)
		if err != nil {
			return ErrorResult(err.Error()), nil
		}
		return TextResult(out), nil
	})

	srv.Register(Tool{
		Name:        "rex.events.recent",
		Description: "Tail recent audit-class events. Optional `type` filter (e.g. \"run.started\", \"spec.created\") narrows the result; `n` caps the count (default 20).",
		InputSchema: jsonRawObject(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"type": map[string]any{"type": "string", "description": "Event-type filter."},
				"n":    map[string]any{"type": "integer", "description": "Max rows to return (default 20)."},
			},
		}),
	}, func(_ context.Context, args json.RawMessage) (Result, error) {
		var p struct {
			Type string `json:"type"`
			N    int    `json:"n"`
		}
		_ = json.Unmarshal(args, &p)
		if p.N <= 0 {
			p.N = 20
		}
		return TextResult(renderRecentEvents(workspaceRoot, p.Type, p.N)), nil
	})
}

// renderSpecList walks the workspace's specs/ directory and
// emits one Markdown bullet per parseable spec.
func renderSpecList(root string) string {
	dir := filepath.Join(root, ".rex", "specs")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "no specs found (" + err.Error() + ")"
	}
	var b strings.Builder
	count := 0
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".yaml" {
			continue
		}
		doc, err := specfmt.ParseFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		fmt.Fprintf(&b, "- `%s` — %s · %s\n", doc.Metadata.ID, doc.Metadata.Name, doc.Metadata.State)
		count++
	}
	if count == 0 {
		return "no specs in this workspace"
	}
	return b.String()
}

// runValidate routes through specfmt.ValidateWorkspace so the
// MCP-served result matches what `rex spec validate` would
// print.
func runValidate(root, id string) (string, error) {
	dir := filepath.Join(root, ".rex", "specs")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	ws := specfmt.NewWorkspace()
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".yaml" {
			continue
		}
		path := filepath.Join(dir, e.Name())
		doc, err := specfmt.ParseFile(path)
		if err != nil {
			continue
		}
		_ = ws.Add(doc)
	}
	res := specfmt.ValidateWorkspace(ws, specfmt.ModeStrict)
	var b strings.Builder
	hits := 0
	for _, iss := range res.Issues {
		if id != "" {
			specID, _ := specIDFromIssue(iss.File)
			if specID != id {
				continue
			}
		}
		fmt.Fprintf(&b, "- [%s] %s — %s: %s\n", iss.Severity, iss.File, iss.Path, iss.Message)
		hits++
	}
	if hits == 0 {
		if id != "" {
			return "validate `" + id + "`: 0 errors, 0 warnings", nil
		}
		return fmt.Sprintf("validate: %d spec(s), 0 errors, 0 warnings", len(ws.Specs())), nil
	}
	return b.String(), nil
}

// runVerify routes through specverify.Verify with the same
// auto-detected git/eventlog options the CLI uses.
func runVerify(root, id string) (string, error) {
	dir := filepath.Join(root, ".rex", "specs")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	ws := specfmt.NewWorkspace()
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".yaml" {
			continue
		}
		path := filepath.Join(dir, e.Name())
		doc, err := specfmt.ParseFile(path)
		if err != nil {
			continue
		}
		_ = ws.Add(doc)
	}
	res := specverify.Verify(ws, specverify.FromCLI(root, specfmt.ModeStrict))
	var b strings.Builder
	hits := 0
	for _, iss := range res.Issues {
		if id != "" {
			specID, _ := specIDFromIssue(iss.File)
			if specID != id {
				continue
			}
		}
		fmt.Fprintf(&b, "- [%s] %s — %s: %s\n", iss.Severity, iss.File, iss.Path, iss.Message)
		hits++
	}
	if hits == 0 {
		if id != "" {
			return "verify `" + id + "`: 0 errors, 0 warnings", nil
		}
		return fmt.Sprintf("verify: %d spec(s), 0 errors, 0 warnings", len(ws.Specs())), nil
	}
	return b.String(), nil
}

// specIDFromIssue extracts the spec id from an Issue.File path
// (e.g. `.../specs/foo.yaml` → "foo"). Returns ("", false) on
// inputs that don't look like spec paths.
func specIDFromIssue(path string) (string, bool) {
	base := filepath.Base(path)
	if !strings.HasSuffix(base, ".yaml") {
		return "", false
	}
	return strings.TrimSuffix(base, ".yaml"), true
}

// renderRecentEvents tails events.log and emits the last n rows
// that match the optional type filter. Best-effort — a missing
// log returns the explanation rather than an error.
func renderRecentEvents(root, eventType string, n int) string {
	r, err := eventlog.OpenReader(filepath.Join(root, ".rex", "events.log"))
	if err != nil {
		return "events.log not available: " + err.Error()
	}
	defer r.Close()
	reg := event.NewRegistry()
	runner.RegisterEvents(reg)
	var matched []eventlog.Record
	for {
		rec, err := r.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "read events.log: " + err.Error()
		}
		if eventType != "" && rec.Type != eventType {
			continue
		}
		matched = append(matched, rec)
	}
	if len(matched) == 0 {
		if eventType != "" {
			return "no events of type `" + eventType + "`"
		}
		return "no events yet"
	}
	if len(matched) > n {
		matched = matched[len(matched)-n:]
	}
	var b strings.Builder
	for _, rec := range matched {
		fmt.Fprintf(&b, "- %s · `%s`\n", rec.ID, rec.Type)
	}
	return b.String()
}

// jsonRawObject is a tiny helper so the schema literal in tool
// registrations stays readable. JSON-marshals the supplied map
// and panics on failure (the inputs are all hard-coded literals,
// so a marshal error is a programming bug).
func jsonRawObject(v map[string]any) json.RawMessage {
	body, err := json.Marshal(v)
	if err != nil {
		panic("mcpserver: marshal tool schema: " + err.Error())
	}
	return body
}
