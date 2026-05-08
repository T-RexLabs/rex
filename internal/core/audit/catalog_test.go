package audit

import (
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// TestCatalogDocCoversEveryRegisteredType keeps the audit/doc.go
// catalog (audit.audit-event-types) in sync with the runtime
// registry. Whenever a new audit-class type lands in
// auditEventTypes, this test fails until the doc enumerates it.
//
// The check parses doc.go's package comment and looks for each
// type-name string. Reserved-future types are not registered, so
// they don't trigger this test — the doc lists them separately.
func TestCatalogDocCoversEveryRegisteredType(t *testing.T) {
	t.Parallel()

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "doc.go", nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse doc.go: %v", err)
	}
	if f.Doc == nil {
		t.Fatal("audit/doc.go has no package comment")
	}
	docText := f.Doc.Text()

	for _, name := range EventTypes() {
		if !strings.Contains(docText, name) {
			t.Errorf("audit/doc.go catalog is missing event type %q", name)
		}
	}
}
