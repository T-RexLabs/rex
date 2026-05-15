// Package web is the central node's web UI shell
// (web-ui.CENTRAL-LAYOUT.1). It composes the shared internal/web
// renderer + static assets with central-specific handlers as those
// land in later tasks.
//
// At this point the package is intentionally tiny: a wiring-proof
// page at /_web/health and the /static/ mount, both behind
// rex-central's --web flag. The read-side pages
// (/orgs/<id>/workspaces/<ws-id>/...) and the org-admin pages
// (/orgs/<id>, /orgs/<id>/members, etc.) land with
// central-read-side-pages and central-org-admin-pages.
package web
