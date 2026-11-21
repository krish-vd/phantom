// Phantom watches the package names AI models invent, and tells you when
// someone registers one of them.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/krish-vd/phantom/internal/extract"
	"github.com/krish-vd/phantom/internal/llm"
	"github.com/krish-vd/phantom/internal/prompts"
	"github.com/krish-vd/phantom/internal/registry"
	"github.com/krish-vd/phantom/internal/report"
	"github.com/krish-vd/phantom/internal/store"
)

var version = "0.1.0"

const usage = `phantom: watch the package names AI models invent.

Usage:
  phantom probe  --model NAME [flags]   ask a local model coding questions, record invented packages
  phantom watch  [flags]                re-check invented names; alert when someone registers one
  phantom guard  FILE...                check a project's dependencies (requirements.txt, pyproject.toml, package.json)
  phantom seed   FILE [flags]           start watching names from a list (e.g. a published research dataset)
  phantom check  ECO/NAME...            look up names, e.g. pypi/fastapi-turbo npm/react-hooks-essential
  phantom report [flags]                write a Markdown report and a JSON blocklist feed
  phantom version

Run "phantom COMMAND -h" for flags. Works with Ollama or any OpenAI-compatible
local server. No API key needed.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Print(usage)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	var err error
	code := 0
	switch os.Args[1] {
	case "probe":
		err = probe(ctx, os.Args[2:])
	case "watch":
		code, err = watch(ctx, os.Args[2:])
	case "guard":
		code, err = guard(ctx, os.Args[2:])
	case "check":
		err = check(ctx, os.Args[2:])
	case "seed":
		err = seed(ctx, os.Args[2:])
	case "report":
		err = reportCmd(os.Args[2:])
	case "version", "--version", "-v":
		fmt.Println("phantom", version)
	case "help", "-h", "--help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", os.Args[1], usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "phantom:", err)
		os.Exit(1)
	}
	os.Exit(code)
}

func defaultDB() string {
	if v := os.Getenv("PHANTOM_DB"); v != "" {
		return v
	}
	return "phantom-data/phantom.json"
}

// ---------------------------------------------------------------- probe

func probe(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("probe", flag.ExitOnError)
	model := fs.String("model", "", "model name, e.g. qwen2.5-coder:7b (required)")
	provider := fs.String("provider", "ollama", "ollama or openai (any OpenAI-compatible server)")
	endpoint := fs.String("endpoint", os.Getenv("OLLAMA_HOST"), "server URL (default: Ollama on localhost:11434, OpenAI-compatible on localhost:1234/v1)")
	samples := fs.Int("samples", 1, "times to ask each prompt (repeat hallucinations are the dangerous ones)")
	limit := fs.Int("limit", 0, "only use the first N prompts (0 = all)")
	promptFile := fs.String("prompts", "", "custom prompt file (default: built-in set)")
	temp := fs.Float64("temperature", 0.7, "sampling temperature")
	dbPath := fs.String("db", defaultDB(), "watch database")
	saveDir := fs.String("save", "", "directory to save raw responses as JSONL")
	fs.Parse(args)

	client, err := llm.New(*provider, *endpoint, *model, os.Getenv("PHANTOM_API_KEY"), *temp)
	if err != nil {
		return err
	}
	ps, err := prompts.Load(*promptFile)
	if err != nil {
		return err
	}
	if *limit > 0 && *limit < len(ps) {
		ps = ps[:*limit]
	}
	db, err := store.Open(*dbPath)
	if err != nil {
		return err
	}
	var raw *json.Encoder
	if *saveDir != "" {
		if err := os.MkdirAll(*saveDir, 0o755); err != nil {
			return err
		}
		f, err := os.Create(filepath.Join(*saveDir, fmt.Sprintf("%s-%s.jsonl", safe(*model), time.Now().UTC().Format("20060102-150405"))))
		if err != nil {
			return err
		}
		defer f.Close()
		raw = json.NewEncoder(f)
	}

	reg := registry.New()
	run := store.Run{At: time.Now().UTC(), Model: *model, Prompts: len(ps), Samples: *samples}
	seen, invented := map[string]bool{}, map[string]bool{}
	total := len(ps) * *samples
	fmt.Printf("Probing %s with %d prompts × %d samples\n\n", *model, len(ps), *samples)

	i := 0
	for s := 0; s < *samples; s++ {
		for _, p := range ps {
			i++
			if ctx.Err() != nil {
				break
			}
			fmt.Printf("[%d/%d] %s\n", i, total, short(p.Task, 70))
			ans, err := client.Ask(ctx, p.Text())
			if err != nil {
				run.Errors++
				fmt.Printf("        error: %v\n", err)
				if i == 1 { // first call failing usually means no server; stop early
					return err
				}
				continue
			}
			run.Responses++
			now := time.Now().UTC()
			mentions := extract.FromText(ans)
			var bad []string
			for _, m := range mentions {
				n := db.Record(m, *model, p.ID, now)
				in, err := reg.Lookup(ctx, m.Ecosystem, m.Name)
				if err != nil {
					fmt.Printf("        lookup %s: %v\n", m.Key(), err)
					continue
				}
				n.Update(in, now)
				seen[m.Key()] = true
				if m.Source == extract.FromInstall {
					run.Mentions++
					if !in.Exists {
						run.Hallucinated++
					}
				}
				if !in.Exists && m.Source == extract.FromInstall {
					invented[m.Key()] = true
					bad = append(bad, m.Key())
				} else if !in.Exists {
					bad = append(bad, m.Key()+" (import)")
				} else if n.Status == store.Claimed {
					bad = append(bad, m.Key()+" 🚨 claimed")
				}
			}
			line := fmt.Sprintf("        %d packages", len(mentions))
			if len(bad) > 0 {
				line += "  ⚠ " + strings.Join(bad, ", ")
			}
			fmt.Println(line)
			if raw != nil {
				raw.Encode(map[string]any{"at": now, "model": *model, "prompt": p, "response": ans, "mentions": mentions})
			}
		}
	}
	run.Names, run.Invented = len(seen), len(invented)
	db.Runs = append(db.Runs, run)
	if err := db.Save(); err != nil {
		return err
	}

	rate := 0.0
	if run.Mentions > 0 {
		rate = 100 * float64(run.Hallucinated) / float64(run.Mentions)
	}
	fmt.Printf("\n%s: %d responses, %d install suggestions, %d invented (%.1f%%), %d unique invented names.\n",
		*model, run.Responses, run.Mentions, run.Hallucinated, rate, run.Invented)
	fmt.Printf("Saved to %s. Next: phantom report, and phantom watch on a schedule.\n", *dbPath)
	return ctx.Err()
}

// ---------------------------------------------------------------- watch

func watch(ctx context.Context, args []string) (int, error) {
	fs := flag.NewFlagSet("watch", flag.ExitOnError)
	dbPath := fs.String("db", defaultDB(), "watch database")
	all := fs.Bool("all", false, "re-check established packages too")
	fs.Parse(args)

	db, err := store.Open(*dbPath)
	if err != nil {
		return 0, err
	}
	targets := db.Sorted(func(n *store.Name) bool { return *all || n.Status != store.Real })
	if len(targets) == 0 {
		fmt.Println("Nothing to watch yet. Run phantom probe first.")
		return 0, nil
	}
	fmt.Printf("Re-checking %d names on PyPI and npm...\n", len(targets))

	reg := registry.New()
	type result struct {
		n  *store.Name
		in registry.Info
		e  error
	}
	results := make([]result, len(targets))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for i, n := range targets {
		wg.Add(1)
		go func(i int, n *store.Name) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			in, err := reg.Lookup(ctx, n.Ecosystem, n.Name)
			results[i] = result{n, in, err}
		}(i, n)
	}
	wg.Wait()

	now := time.Now().UTC()
	alerts, failed := 0, 0
	for _, r := range results {
		if r.e != nil {
			failed++
			continue
		}
		if ev := r.n.Update(r.in, now); ev != nil {
			mark := "  "
			if ev.To == store.Claimed {
				mark = "🚨"
				alerts++
			}
			fmt.Printf("%s %-45s %s → %s", mark, r.n.Key(), ev.From, ev.To)
			if ev.To == store.Claimed {
				fmt.Printf("  (registered %s, latest %s: %q)", r.in.Created.Format("2006-01-02"), r.in.Latest, r.in.Summary)
			}
			fmt.Println()
		}
	}
	if err := db.Save(); err != nil {
		return 0, err
	}
	counts := map[store.Status]int{}
	for _, n := range db.Names {
		counts[n.Status]++
	}
	fmt.Printf("\nunclaimed %d · claimed %d · young %d · real %d", counts[store.Unclaimed], counts[store.Claimed], counts[store.Young], counts[store.Real])
	if failed > 0 {
		fmt.Printf(" · %d lookups failed", failed)
	}
	fmt.Println()
	if alerts > 0 {
		fmt.Printf("\n%d invented name(s) were registered since the last check. Review them before anyone installs them.\n", alerts)
		return 3, nil
	}
	return 0, nil
}

// ---------------------------------------------------------------- guard

type dep struct {
	eco  extract.Ecosystem
	name string
	file string
}

func guard(ctx context.Context, args []string) (int, error) {
	fs := flag.NewFlagSet("guard", flag.ExitOnError)
	dbPath := fs.String("db", defaultDB(), "watch database (optional; adds what Phantom has seen models invent)")
	minAge := fs.Duration("min-age", 30*24*time.Hour, "warn about packages published more recently than this")
	strict := fs.Bool("strict", false, "fail on warnings too")
	fs.Parse(args)
	files := fs.Args()
	if len(files) == 0 {
		for _, f := range []string{"requirements.txt", "pyproject.toml", "package.json"} {
			if _, err := os.Stat(f); err == nil {
				files = append(files, f)
			}
		}
	}
	if len(files) == 0 {
		return 2, errors.New("no dependency files given or found (requirements.txt, pyproject.toml, package.json)")
	}

	var deps []dep
	for _, f := range files {
		ds, err := parseDeps(f)
		if err != nil {
			return 2, fmt.Errorf("%s: %w", f, err)
		}
		deps = append(deps, ds...)
	}
	db, _ := store.Open(*dbPath)
	reg := registry.New()
	fails, warns := 0, 0
	for _, d := range deps {
		in, err := reg.Lookup(ctx, d.eco, d.name)
		if err != nil {
			fmt.Printf("  ?  %-40s lookup failed: %v\n", string(d.eco)+"/"+d.name, err)
			continue
		}
		key := string(d.eco) + "/" + d.name
		known := db.Names[key]
		switch {
		case !in.Exists:
			fails++
			why := "does not exist on the registry"
			if known != nil {
				why += fmt.Sprintf(" (AI models have suggested it %d times)", known.Hits)
			}
			fmt.Printf("FAIL %-40s %s\n", key, why)
		case known != nil && known.Status == store.Claimed:
			fails++
			fmt.Printf("FAIL %-40s registered %s, after AI models were already inventing it\n", key, in.Created.Format("2006-01-02"))
		case !in.Created.IsZero() && time.Since(in.Created) < *minAge:
			warns++
			fmt.Printf("WARN %-40s first published %d days ago\n", key, int(time.Since(in.Created).Hours()/24))
		case known != nil && known.Status == store.Young:
			warns++
			fmt.Printf("WARN %-40s published shortly before models started suggesting it\n", key)
		}
	}
	fmt.Printf("\nChecked %d dependencies from %s: %d failed, %d warnings.\n", len(deps), strings.Join(files, ", "), fails, warns)
	if fails > 0 || (*strict && warns > 0) {
		return 1, nil
	}
	return 0, nil
}

var (
	reqNameRe    = regexp.MustCompile(`^([A-Za-z0-9][A-Za-z0-9._-]*)`)
	tomlStringRe = regexp.MustCompile(`["']([^"']+)["']`)
)

func parseDeps(path string) ([]dep, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	base := filepath.Base(path)
	var out []dep
	add := func(eco extract.Ecosystem, name string) {
		if eco == extract.PyPI {
			name = extract.NormalizePyPI(name)
		}
		out = append(out, dep{eco, name, path})
	}
	switch {
	case base == "package.json":
		var pj map[string]json.RawMessage
		if err := json.Unmarshal(b, &pj); err != nil {
			return nil, err
		}
		for _, k := range []string{"dependencies", "devDependencies", "peerDependencies", "optionalDependencies"} {
			var m map[string]string
			if raw, ok := pj[k]; ok && json.Unmarshal(raw, &m) == nil {
				names := make([]string, 0, len(m))
				for n, v := range m {
					if strings.HasPrefix(v, "workspace:") || strings.HasPrefix(v, "file:") || strings.HasPrefix(v, "link:") {
						continue
					}
					names = append(names, n)
				}
				sort.Strings(names)
				for _, n := range names {
					add(extract.NPM, n)
				}
			}
		}
	case strings.HasSuffix(base, ".toml"):
		// Minimal: string entries inside dependency arrays/tables of pyproject.toml.
		inDeps := false
		sc := bufio.NewScanner(strings.NewReader(string(b)))
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if strings.HasPrefix(line, "[") && !strings.Contains(line, "=") {
				inDeps = strings.Contains(line, "dependencies") || strings.Contains(line, "optional-dependencies")
				continue
			}
			if strings.HasPrefix(line, "dependencies") || strings.HasPrefix(line, "dev-dependencies") {
				inDeps = true
			}
			if !inDeps || strings.HasPrefix(line, "#") {
				continue
			}
			if k, _, ok := strings.Cut(line, "="); ok && !strings.HasPrefix(line, "dependencies") && !strings.HasPrefix(line, "\"") {
				// table style: name = "^1.0" (poetry)
				if n := strings.TrimSpace(k); n != "python" && reqNameRe.MatchString(n) {
					add(extract.PyPI, n)
				}
				continue
			}
			for _, m := range tomlStringRe.FindAllStringSubmatch(line, -1) {
				if n := reqNameRe.FindString(m[1]); n != "" {
					add(extract.PyPI, n)
				}
			}
		}
	default: // requirements.txt style
		sc := bufio.NewScanner(strings.NewReader(string(b)))
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if i := strings.Index(line, " #"); i >= 0 {
				line = line[:i]
			}
			if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "-") || strings.Contains(line, "://") {
				continue
			}
			if n := reqNameRe.FindString(line); n != "" {
				add(extract.PyPI, n)
			}
		}
	}
	return out, nil
}

// ---------------------------------------------------------------- seed

// seed imports names reported elsewhere as hallucinated, so they can be
// watched without running a model. Each line: "pypi/name [YYYY-MM-DD]".
// The date is when the source saw the name missing; anything registered
// after it is flagged as claimed.
func seed(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("seed", flag.ExitOnError)
	dbPath := fs.String("db", defaultDB(), "watch database")
	source := fs.String("source", "seed", "label for where the names came from, e.g. dataset:depscope")
	since := fs.String("since", "", "default date the names were known missing (YYYY-MM-DD)")
	fs.Parse(args)
	if fs.NArg() != 1 {
		return errors.New("usage: phantom seed [--source LABEL] [--since DATE] FILE")
	}
	b, err := os.ReadFile(fs.Arg(0))
	if err != nil {
		return err
	}
	db, err := store.Open(*dbPath)
	if err != nil {
		return err
	}
	reg := registry.New()
	now := time.Now().UTC()
	added := 0
	for i, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) == 0 || strings.HasPrefix(f[0], "#") {
			continue
		}
		ecoS, name, ok := strings.Cut(f[0], "/")
		eco := extract.Ecosystem(strings.ToLower(ecoS))
		if !ok || (eco != extract.PyPI && eco != extract.NPM) {
			return fmt.Errorf("line %d: want pypi/NAME or npm/NAME", i+1)
		}
		if eco == extract.PyPI {
			name = extract.NormalizePyPI(name)
		}
		dateS := *since
		if len(f) > 1 {
			dateS = f[1]
		}
		seen := now
		if dateS != "" {
			if seen, err = time.Parse("2006-01-02", dateS); err != nil {
				return fmt.Errorf("line %d: bad date %q", i+1, dateS)
			}
		}
		n := db.Record(extract.Mention{Ecosystem: eco, Name: name, Source: extract.FromInstall}, *source, 0, seen)
		if seen.Before(n.FirstSeen) {
			n.FirstSeen = seen
		}
		in, err := reg.Lookup(ctx, eco, name)
		if err != nil {
			return err
		}
		n.Update(in, now)
		added++
		fmt.Printf("%-9s %s\n", n.Status, n.Key())
	}
	if err := db.Save(); err != nil {
		return err
	}
	fmt.Printf("\nWatching %d names from %s. Run phantom watch on a schedule.\n", added, *source)
	return nil
}

// ---------------------------------------------------------------- check

func check(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("check", flag.ExitOnError)
	dbPath := fs.String("db", defaultDB(), "watch database")
	fs.Parse(args)
	if fs.NArg() == 0 {
		return errors.New("usage: phantom check pypi/NAME npm/NAME ...")
	}
	db, _ := store.Open(*dbPath)
	reg := registry.New()
	for _, a := range fs.Args() {
		ecoS, name, ok := strings.Cut(a, "/")
		eco := extract.Ecosystem(strings.ToLower(ecoS))
		if !ok || (eco != extract.PyPI && eco != extract.NPM) {
			return fmt.Errorf("%q: want pypi/NAME or npm/NAME", a)
		}
		if eco == extract.PyPI {
			name = extract.NormalizePyPI(name)
		}
		in, err := reg.Lookup(ctx, eco, name)
		if err != nil {
			return err
		}
		key := string(eco) + "/" + name
		if !in.Exists {
			fmt.Printf("%s: does not exist", key)
			if in.Note != "" {
				fmt.Printf(" (%s)", in.Note)
			}
		} else {
			fmt.Printf("%s: exists, first published %s, %d releases, latest %s", key, in.Created.Format("2006-01-02"), in.Releases, in.Latest)
			if in.Summary != "" {
				fmt.Printf("\n    %q", in.Summary)
			}
		}
		if n := db.Names[key]; n != nil {
			fmt.Printf("\n    Phantom: %s; suggested %d times by %d model(s) since %s", n.Status, n.Hits, len(n.Models), n.FirstSeen.Format("2006-01-02"))
		}
		fmt.Println()
	}
	return nil
}

// ---------------------------------------------------------------- report

func reportCmd(args []string) error {
	fs := flag.NewFlagSet("report", flag.ExitOnError)
	dbPath := fs.String("db", defaultDB(), "watch database")
	out := fs.String("out", "", "write Markdown here (default: stdout)")
	feed := fs.String("feed", "", "also write the public JSON blocklist feed here")
	public := fs.Bool("public", false, "withhold unclaimed names (use for anything you publish)")
	top := fs.Int("top", 25, "rows per table")
	fs.Parse(args)

	db, err := store.Open(*dbPath)
	if err != nil {
		return err
	}
	md := report.Markdown(db, report.Options{Public: *public, Top: *top})
	if *out == "" {
		fmt.Print(md)
	} else if err := os.WriteFile(*out, []byte(md), 0o644); err != nil {
		return err
	}
	if *feed != "" {
		b, err := report.JSONFeed(db)
		if err != nil {
			return err
		}
		if err := os.WriteFile(*feed, b, 0o644); err != nil {
			return err
		}
	}
	return nil
}

func short(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

func safe(s string) string {
	return regexp.MustCompile(`[^A-Za-z0-9._-]+`).ReplaceAllString(s, "_")
}
