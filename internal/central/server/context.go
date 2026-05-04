package server

import "context"

// orgIDKey is the package-private context key under which the
// resolved org id is stamped. tenant-routing middleware writes
// it on every request; the PostgresStore reads it on every
// query that touches a multi-tenant table.
//
// Using a package-private key type keeps the value namespaced —
// callers outside this package can only read/write through the
// helpers below.
type orgIDKey struct{}

// WithOrgID returns ctx with the org id stamped. Used by the
// tenant-routing middleware after auth resolution + by tests
// that bypass the middleware (the freshPostgresStore helper
// returns a context already pre-stamped with the default org).
func WithOrgID(ctx context.Context, id string) context.Context {
	if id == "" {
		return ctx
	}
	return context.WithValue(ctx, orgIDKey{}, id)
}

// OrgIDFromContext returns the org id stamped into ctx, or
// empty when nothing is stamped. PostgresStore methods reject
// the empty case so an unscoped query never reaches the
// database — defense-in-depth at the application layer until
// tenant-rls adds Postgres-level enforcement.
func OrgIDFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(orgIDKey{}).(string); ok {
		return v
	}
	return ""
}
