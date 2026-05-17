package main

import (
	"context"

	"github.com/asabla/rex/internal/central/server"
	centralweb "github.com/asabla/rex/internal/central/web"
)

// searchAdapter bridges *server.PostgresSearch (returns
// []server.SearchHit) to centralweb.SearchHitReader (returns
// []centralweb.SearchHit). Lives in cmd/ so internal/central/web
// stays free of an internal/central/server import — same pattern
// the org-admin adapter uses.
type searchAdapter struct {
	search *server.PostgresSearch
}

func (a searchAdapter) Search(ctx context.Context, workspaceID, query string, limit int) ([]centralweb.SearchHit, error) {
	hits, err := a.search.Search(ctx, workspaceID, query, limit)
	if err != nil {
		return nil, err
	}
	out := make([]centralweb.SearchHit, 0, len(hits))
	for _, h := range hits {
		out = append(out, centralweb.SearchHit{
			EntityType: h.EntityType,
			EntityID:   h.EntityID,
			Title:      h.Title,
			Snippet:    h.Snippet,
			Score:      h.Score,
		})
	}
	return out, nil
}
