package web

import (
	"context"
	"html/template"
	"net/http"
	"strings"

	"gopkg.in/yaml.v3"

	internalweb "github.com/asabla/rex/internal/web"
)

// centralSettingsData backs settings_central.tmpl. Mirrors the
// workspace section of the local settings page; per-machine
// config (identity, log levels, hooks) is omitted because it has
// no central equivalent (web-ui amendment 2026-05-16, Decision C).
type centralSettingsData struct {
	centralPageData
	WorkspaceName       string
	WorkspaceState      string
	WorkspaceCreatedAt  string
	WorkspaceYAMLRaw    string
	WorkspaceYAMLPretty template.HTML
}

// handleSettings is GET /orgs/<org>/workspaces/<ws>/settings.
// Renders the workspace.yaml synced through /sync/git plus the
// metadata fields the local settings page surfaces. Read-only;
// the only edit affordance lives on the local shell.
func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	if s.opts.Resolver == nil {
		http.Error(w, "central web: resolver not configured", http.StatusServiceUnavailable)
		return
	}
	orgID := r.PathValue("org")
	wsID := r.PathValue("ws")
	ws, err := s.opts.Resolver.Resolve(wsID)
	if err != nil {
		http.Error(w, "central web: resolve workspace: "+err.Error(), http.StatusNotFound)
		return
	}
	data := centralSettingsData{
		centralPageData: s.pageData(orgID, wsID, "settings"),
	}
	if reader, ok := workspaceYAMLReader(s.opts.Resolver, wsID); ok {
		raw, err := reader.workspaceYAML(context.Background())
		if err == nil && raw != "" {
			data.WorkspaceYAMLRaw = raw
			if s.highlighter != nil {
				data.WorkspaceYAMLPretty = s.highlighter.HighlightYAML(raw)
			}
			fields := parseWorkspaceFields(raw)
			data.Workspace.ID = firstNonEmpty(fields.ID, ws.ID)
			data.WorkspaceName = fields.Name
			data.WorkspaceState = fields.State
			data.WorkspaceCreatedAt = fields.CreatedAt
		}
	}
	s.renderer.Render(w, r, "settings_central.tmpl", data)
}

// workspaceYAMLSource is the in-process subset of the central
// resolver needed to fetch workspace.yaml bytes. The
// centralWorkspaceResolver satisfies it directly; tests that
// inject a stub resolver implement it explicitly when they want
// to exercise the settings page.
type workspaceYAMLSource interface {
	workspaceYAML(ctx context.Context) (string, error)
}

// workspaceYAMLReader returns the YAML-source for a given
// workspace id. Returns (_, false) when the bound resolver
// doesn't expose the source (tests with a custom resolver, etc).
func workspaceYAMLReader(r internalweb.WorkspaceResolver, wsID string) (workspaceYAMLSource, bool) {
	cr, ok := r.(centralWorkspaceResolver)
	if !ok {
		return nil, false
	}
	if cr.git == nil {
		return nil, false
	}
	return centralWorkspaceYAMLSource{store: cr.git, wsID: wsID}, true
}

type centralWorkspaceYAMLSource struct {
	store GitEntityReader
	wsID  string
}

func (c centralWorkspaceYAMLSource) workspaceYAML(ctx context.Context) (string, error) {
	if c.wsID == "" {
		return "", nil
	}
	rec, err := c.store.Get(ctx, c.wsID, "workspace.yaml")
	if err != nil {
		return "", err
	}
	return rec.Content, nil
}

// workspaceFields is the subset of workspace.yaml the settings
// page renders. yaml.v3 leaves missing keys as zero values, so
// unknown fields don't fail the page.
type workspaceFields struct {
	ID        string
	Name      string
	State     string
	CreatedAt string
}

func parseWorkspaceFields(raw string) workspaceFields {
	var fm struct {
		ID        string `yaml:"id"`
		Name      string `yaml:"name"`
		State     string `yaml:"state"`
		CreatedAt string `yaml:"created_at"`
	}
	if err := yaml.Unmarshal([]byte(raw), &fm); err != nil {
		return workspaceFields{}
	}
	return workspaceFields{
		ID:        strings.TrimSpace(fm.ID),
		Name:      strings.TrimSpace(fm.Name),
		State:     strings.TrimSpace(fm.State),
		CreatedAt: strings.TrimSpace(fm.CreatedAt),
	}
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
