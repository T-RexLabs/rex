package web

import "embed"

// TemplateFS is the embedded template tree shared by both binaries.
// Layout: base.tmpl + pages/*.tmpl + partials/*.tmpl. NewRenderer
// reads from this filesystem; callers do not normally touch it
// directly.
//
//go:embed templates/*.tmpl templates/pages/*.tmpl templates/partials/*.tmpl
var TemplateFS embed.FS

// StaticFS is the embedded static-asset tree (CSS + the small JS
// layer). Callers mount it with http.FileServer(http.FS(staticSub))
// after fs.Sub-ing under "static".
//
//go:embed static/*
var StaticFS embed.FS
