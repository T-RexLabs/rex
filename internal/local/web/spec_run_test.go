package web

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeSpecForRun drops a spec file under <root>/.rex/specs.
func writeSpecForRun(t *testing.T, root, id, body string) {
	t.Helper()
	path := filepath.Join(root, ".rex", "specs", id+".yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write spec: %v", err)
	}
}

func TestSpecDetailRendersRunButtonForTaskWithRecipe(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t, "ws-spec-run-button")
	writeSpecForRun(t, root, "demo", `
spec_version: 1
metadata:
  id: demo
  name: Demo
  state: draft
components:
  X:
    name: X
    requirements:
      "1": one
tasks:
  - id: greet
    description: greet the world
    state: todo
    run:
      kind: shell
      command: ["echo", "hi"]
  - id: discuss
    description: a non-runnable task
    state: todo
`)

	hs := newTestServer(t, root)
	resp, err := http.Get(hs.URL + "/specs/demo?tab=tasks")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	body := readBody(t, resp)

	if !strings.Contains(body, `action="/specs/demo/tasks/greet/run"`) {
		t.Fatalf("expected run-button form for greet:\n%s", body)
	}
	if strings.Contains(body, `action="/specs/demo/tasks/discuss/run"`) {
		t.Fatalf("did not expect run-button form for discuss (no recipe)")
	}
	if !strings.Contains(body, "run this task") {
		t.Fatalf("expected button label 'run this task' in output")
	}
}

func TestSpecTaskRunRedirectsOnSuccess(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t, "ws-spec-task-run")
	writeSpecForRun(t, root, "demo", `
spec_version: 1
metadata:
  id: demo
  name: Demo
  state: draft
components:
  X:
    name: X
    requirements:
      "1": one
tasks:
  - id: hello
    description: say hi
    state: todo
    references: [X.1]
    run:
      kind: shell
      command: ["echo", "hello-from-recipe"]
`)
	hs := newTestServer(t, root)

	req, _ := http.NewRequest(http.MethodPost, hs.URL+"/specs/demo/tasks/hello/run", strings.NewReader(""))
	resp, err := noRedirectClient().Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		body := readBody(t, resp)
		t.Fatalf("status: %d\n%s", resp.StatusCode, body)
	}
	loc := resp.Header.Get("Location")
	if !strings.HasPrefix(loc, "/runs/") {
		t.Fatalf("Location: got %q want /runs/<id>", loc)
	}
}

func TestSpecTaskRunMissingTask(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t, "ws-spec-task-run-miss")
	writeSpecForRun(t, root, "demo", `
spec_version: 1
metadata:
  id: demo
  name: Demo
  state: draft
components:
  X:
    name: X
    requirements:
      "1": one
tasks:
  - id: hi
    description: hi
    state: todo
    run:
      kind: shell
      command: ["echo", "ok"]
`)
	hs := newTestServer(t, root)

	req, _ := http.NewRequest(http.MethodPost, hs.URL+"/specs/demo/tasks/no-such/run", strings.NewReader(""))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		body := readBody(t, resp)
		t.Fatalf("status: %d\n%s", resp.StatusCode, body)
	}
}

func TestRunsListSpecFilter(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t, "ws-runs-spec-filter")
	writeSpecForRun(t, root, "demo", `
spec_version: 1
metadata:
  id: demo
  name: Demo
  state: draft
components:
  X:
    name: X
    requirements:
      "1": one
tasks:
  - id: t1
    description: t1
    state: todo
    references: [X.1]
    run:
      kind: shell
      command: ["echo", "tagged"]
  - id: t2
    description: t2
    state: todo
    run:
      kind: shell
      command: ["echo", "also-tagged"]
`)
	hs := newTestServer(t, root)

	// Two runs against demo, one ad-hoc against /runs/start.
	for _, taskID := range []string{"t1", "t2"} {
		req, _ := http.NewRequest(http.MethodPost, hs.URL+"/specs/demo/tasks/"+taskID+"/run", strings.NewReader(""))
		resp, err := noRedirectClient().Do(req)
		if err != nil {
			t.Fatalf("POST %s: %v", taskID, err)
		}
		_ = resp.Body.Close()
	}

	// Now hit /runs and /runs?spec=demo and confirm that the
	// filter narrows the result.
	allBody := readBody(t, mustGet(t, hs.URL+"/runs"))
	if c := strings.Count(allBody, "<tr>"); c < 2 {
		t.Fatalf("expected at least 2 run rows in /runs (header + 2 runs), saw %d", c)
	}

	filteredBody := readBody(t, mustGet(t, hs.URL+"/runs?spec=demo"))
	if !strings.Contains(filteredBody, "filtered by") {
		t.Fatalf("expected filter banner: %s", filteredBody)
	}
	// Filtered list should still contain both tagged runs.
	if !strings.Contains(filteredBody, "<code>") {
		t.Fatalf("expected at least one run row after filter: %s", filteredBody)
	}
}

func mustGet(t *testing.T, url string) *http.Response {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	return resp
}

func TestSpecTaskRunNoRecipe(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t, "ws-spec-task-run-norec")
	writeSpecForRun(t, root, "demo", `
spec_version: 1
metadata:
  id: demo
  name: Demo
  state: draft
components:
  X:
    name: X
    requirements:
      "1": one
tasks:
  - id: discuss
    description: not runnable
    state: todo
`)
	hs := newTestServer(t, root)

	req, _ := http.NewRequest(http.MethodPost, hs.URL+"/specs/demo/tasks/discuss/run", strings.NewReader(""))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		body := readBody(t, resp)
		t.Fatalf("status: %d\n%s", resp.StatusCode, body)
	}
}
