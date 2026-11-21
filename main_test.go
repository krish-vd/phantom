package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/krish-vd/phantom/internal/store"
)

// End-to-end: a fake Ollama server answers with a mix of real and invented
// packages; Phantom probes it, checks the real registries, and reports.
func TestProbeEndToEnd(t *testing.T) {
	if os.Getenv("PHANTOM_LIVE") == "" {
		t.Skip("set PHANTOM_LIVE=1 (uses real PyPI and npm)")
	}
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			http.NotFound(w, r)
			return
		}
		var req struct {
			Messages []struct{ Content string } `json:"messages"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		ans := "Install:\n```bash\npip install requests phantom-test-retry-magic-zq9\n```\n```python\nimport requests\nfrom phantom_test_ghost_lib_k2 import x\n```"
		if strings.Contains(req.Messages[1].Content, "JavaScript") {
			ans = "Run `npm install express phantom-test-react-easy-hooks-q7`."
		}
		json.NewEncoder(w).Encode(map[string]any{"message": map[string]string{"role": "assistant", "content": ans}})
	}))
	defer fake.Close()

	dir := t.TempDir()
	db := filepath.Join(dir, "db.json")
	pf := filepath.Join(dir, "prompts.txt")
	os.WriteFile(pf, []byte("py: do a python thing\njs: do a js thing\n"), 0o644)

	ctx := context.Background()
	if err := probe(ctx, []string{"--model", "fake", "--endpoint", fake.URL, "--prompts", pf, "--samples", "2", "--db", db}); err != nil {
		t.Fatal(err)
	}
	d, err := store.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]store.Status{
		"pypi/requests":                        store.Real,
		"pypi/phantom-test-retry-magic-zq9":    store.Unclaimed,
		"pypi/phantom-test-ghost-lib-k2":       store.Unclaimed,
		"npm/express":                          store.Real,
		"npm/phantom-test-react-easy-hooks-q7": store.Unclaimed,
	}
	for k, s := range want {
		n := d.Names[k]
		if n == nil || n.Status != s {
			t.Errorf("%s: got %+v, want %s", k, n, s)
		}
	}
	if n := d.Names["pypi/phantom-test-retry-magic-zq9"]; n == nil || n.Hits != 2 || n.InstallHits != 2 {
		t.Errorf("hits not counted across samples: %+v", n)
	}
	r := d.Runs[0]
	// 4 responses; install mentions: py 2×2 + js 2×2 = 8; invented: 4
	if r.Responses != 4 || r.Mentions != 8 || r.Hallucinated != 4 || r.Invented != 2 {
		t.Errorf("run stats: %+v", r)
	}

	if code, err := watch(ctx, []string{"--db", db}); err != nil || code != 0 {
		t.Errorf("watch: code %d err %v", code, err)
	}

	// guard: a requirements file with one invented package must fail.
	req := filepath.Join(dir, "requirements.txt")
	os.WriteFile(req, []byte("requests>=2\nphantom-test-retry-magic-zq9==1.0  # from an AI answer\n"), 0o644)
	if code, err := guard(ctx, []string{"--db", db, req}); err != nil || code != 1 {
		t.Errorf("guard: want exit 1, got %d %v", code, err)
	}

	md := filepath.Join(dir, "report.md")
	feed := filepath.Join(dir, "feed.json")
	if err := reportCmd([]string{"--db", db, "--public", "--out", md, "--feed", feed}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(md)
	if strings.Contains(string(b), "phantom-test-retry-magic-zq9") {
		t.Error("public report leaked an unclaimed name")
	}
	if !strings.Contains(string(b), "| `fake` | 4 | 8 | 4 | 50.0% | 2 |") {
		t.Errorf("model row missing:\n%s", b)
	}
}
