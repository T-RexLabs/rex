// Package web is the local-side web UI server (web-ui.LOCAL).
//
// Architecture per the spec:
//
//   - Server-rendered HTML using Go's html/template (web-ui.STACK.1).
//   - Templates and static assets are embedded via embed.FS so the
//     binary ships with no external file dependency. No build step
//     for frontend (no node, no preprocessor) per web-ui.STACK.3.
//   - htmx + hx-ext=sse are wired in from the run-detail page on;
//     pages render usefully without JavaScript per web-ui.ACCESS.3.
//   - Loopback bind by default (web-ui.LOCAL.2). Listening on a
//     non-loopback address requires --addr to be set explicitly,
//     and at that point the operator is consenting to the trust
//     model "local user is the workspace owner".
//
// V1 first slice covers the workspace overview, spec list/detail,
// run list/detail (with live SSE), audit log, and remotes view —
// the routes daily-drive most needs. Settings, search UI, and the
// permission-prompt cards land alongside their dependencies
// (identity selector, search-from-CLI parity, execution.PERM).
package web
