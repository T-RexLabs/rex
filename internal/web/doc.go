// Package web is the shared web-UI core that the local and central
// binaries both build their UIs on top of (web-ui.CENTRAL-LAYOUT.1).
//
// What lives here: the embedded templates and static assets, the
// template-loading and rendering machinery, the syntax highlighter,
// the page-data scaffolding that's shape-portable across binaries,
// and the WorkspaceResolver interface that handlers use to talk
// about a workspace without binding to its on-disk location.
//
// What does NOT live here (yet): the read-side handlers themselves
// (specs, runs, audit, search, amendments). Those still live in
// internal/local/web/ and will lift up as the central-read-side-pages
// task pulls them into shared use. This package is the foundation
// that lift will sit on (web-ui.CENTRAL-LAYOUT.2).
package web
