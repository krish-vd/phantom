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
	"sync/atomic"
	"time"

	"github.com/krish-vd/phantom/internal/extract"
	"github.com/krish-vd/phantom/internal/llm"
	"github.com/krish-vd/phantom/internal/prompts"
	"github.com/krish-vd/phantom/internal/registry"
	"github.com/krish-vd/phantom/internal/report"
	"github.com/krish-vd/phantom/internal/store"
	"github.com/krish-vd/phantom/internal/ui"
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
  phantom dash   [flags]                show the watch list, claimed names and model rates
  phantom version

Run "phantom COMMAND -h" for flags. Works with Ollama or any OpenAI-compatible
local server. No API key needed.
`

func main() {
	if len(os.Args) < 2 {
		printUsage()
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
	case "dash":
		err = dash(os.Args[2:])
	case "version", "--version", "-v":
		fmt.Println("phantom", version)
	case "help", "-h", "--help":
		printUsage()
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
	if ui.On {
		fmt.Print(ui.Banner(version, tagline, credit))
		fmt.Println(ui.Box("probe", "", []string{
			kv("model", ui.Bold(*model)),
			kv("provider", *provider),
			kv("requests", fmt.Sprintf("%d prompts × %s = %d", len(ps), plural(*samples, "sample"), total)),
		}, boxWidth()))
		fmt.Println()
	} else {
		fmt.Printf("Probing %s with %d prompts × %d samples\n\n", *model, len(ps), *samples)
	}
	live := ui.NewLive("")
	defer live.Stop()
	counter := func(i int) string {
		d := len(fmt.Sprint(total))
		return ui.Gray(fmt.Sprintf("%*d/%d", d, i, total))
	}

	// A flagged name in one answer, for the per-prompt result line.
	type flag struct{ key, kind string }

	i := 0
	for s := 0; s < *samples; s++ {
		for _, p := range ps {
			i++
			if ctx.Err() != nil {
				break
			}
			if ui.On {
				live.Set(fmt.Sprintf("%s %s  asking %s  %s", ui.Bar(i-1, total, 18), counter(i), ui.Cyan(*model), ui.Gray(short(p.Task, 48))))
			} else {
				fmt.Printf("[%d/%d] %s\n", i, total, short(p.Task, 70))
			}
			ans, err := client.Ask(ctx, p.Text())
			if err != nil {
				run.Errors++
				if ui.On {
					live.Println(fmt.Sprintf("%s %s %s %s", ui.Red("✗"), counter(i), ui.Pad(short(p.Task, 46), 46), ui.Red("error: "+err.Error())))
				} else {
					fmt.Printf("        error: %v\n", err)
				}
				if i == 1 { // first call failing usually means no server; stop early
					return err
				}
				continue
			}
			run.Responses++
			now := time.Now().UTC()
			mentions := extract.FromText(ans)
			var bad []flag
			for _, m := range mentions {
				n := db.Record(m, *model, p.ID, now)
				if ui.On {
					live.Set(fmt.Sprintf("%s %s  checking %s", ui.Bar(i-1, total, 18), counter(i), m.Key()))
				}
				in, err := reg.Lookup(ctx, m.Ecosystem, m.Name)
				if err != nil {
					if ui.On {
						live.Println(ui.Gray(fmt.Sprintf("  ? lookup %s: %v", m.Key(), err)))
					} else {
						fmt.Printf("        lookup %s: %v\n", m.Key(), err)
					}
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
					bad = append(bad, flag{m.Key(), "invented"})
				} else if !in.Exists {
					bad = append(bad, flag{m.Key(), "import"})
				} else if n.Status == store.Claimed {
					bad = append(bad, flag{m.Key(), "claimed"})
				}
			}
			if ui.On {
				var names []string
				claimed := false
				for _, b := range bad {
					switch b.kind {
					case "invented":
						names = append(names, ui.Purple(b.key))
					case "import":
						names = append(names, ui.Purple(b.key)+ui.Gray(" (import)"))
					case "claimed":
						claimed = true
						names = append(names, ui.Red(b.key+" claimed"))
					}
				}
				icon := ui.Green("✓")
				switch {
				case claimed:
					icon = ui.Red("‼")
				case len(bad) > 0:
					icon = ui.Purple("◆")
				}
				line := fmt.Sprintf("%s %s %s %s", icon, counter(i), ui.Pad(short(p.Task, 46), 46), ui.Gray(fmt.Sprintf("%2d pkg", len(mentions))))
				if len(names) > 0 {
					line += "  " + strings.Join(names, ui.Gray(", "))
				}
				live.Println(line)
			} else {
				line := fmt.Sprintf("        %d packages", len(mentions))
				if len(bad) > 0 {
					var parts []string
					for _, b := range bad {
						switch b.kind {
						case "import":
							parts = append(parts, b.key+" (import)")
						case "claimed":
							parts = append(parts, b.key+" 🚨 claimed")
						default:
							parts = append(parts, b.key)
						}
					}
					line += "  ⚠ " + strings.Join(parts, ", ")
				}
				fmt.Println(line)
			}
			if raw != nil {
				raw.Encode(map[string]any{"at": now, "model": *model, "prompt": p, "response": ans, "mentions": mentions})
			}
		}
	}
	live.Stop()
	run.Names, run.Invented = len(seen), len(invented)
	db.Runs = append(db.Runs, run)
	if err := db.Save(); err != nil {
		return err
	}

	rate := 0.0
	if run.Mentions > 0 {
		rate = 100 * float64(run.Hallucinated) / float64(run.Mentions)
	}
	if ui.On {
		responses := fmt.Sprint(run.Responses)
		if run.Errors > 0 {
			responses += ui.Red(fmt.Sprintf("  (%s)", plural(run.Errors, "error")))
		}
		fmt.Println()
		fmt.Println(ui.Box(*model, ui.Gray("probe finished"), []string{
			kv("responses", responses),
			kv("install suggestions", fmt.Sprint(run.Mentions)),
			kv("invented", fmt.Sprintf("%-6d", run.Hallucinated)+ui.Meter(rate/100, 22, rateColor(rate))+" "+ui.BoldColor(rateColor(rate), fmt.Sprintf("%.1f%%", rate))),
			kv("unique invented names", ui.Purple(fmt.Sprint(run.Invented))),
		}, boxWidth()))
		fmt.Println(ui.Gray("  saved to "+*dbPath+" · next: ") + ui.Cyan("phantom dash") + ui.Gray(", then ") + ui.Cyan("phantom watch") + ui.Gray(" on a schedule"))
	} else {
		fmt.Printf("\n%s: %d responses, %d install suggestions, %d invented (%.1f%%), %d unique invented names.\n",
			*model, run.Responses, run.Mentions, run.Hallucinated, rate, run.Invented)
		fmt.Printf("Saved to %s. Next: phantom report, and phantom watch on a schedule.\n", *dbPath)
	}
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
	if ui.On {
		fmt.Print(ui.Banner(version, tagline, credit))
	} else {
		fmt.Printf("Re-checking %d names on PyPI and npm...\n", len(targets))
	}
	live := ui.NewLive("")
	defer live.Stop()
	var checked atomic.Int32
	progress := func() {
		if ui.On {
			n := int(checked.Load())
			live.Set(fmt.Sprintf("re-checking %s on PyPI and npm  %s %s", plural(len(targets), "name"), ui.Bar(n, len(targets), 24), ui.Gray(fmt.Sprintf("%d/%d", n, len(targets)))))
		}
	}
	progress()

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
			checked.Add(1)
			progress()
		}(i, n)
	}
	wg.Wait()
	live.Stop()

	now := time.Now().UTC()
	alerts, failed, changes := 0, 0, 0
	for _, r := range results {
		if r.e != nil {
			failed++
			continue
		}
		if ev := r.n.Update(r.in, now); ev != nil {
			changes++
			if ev.To == store.Claimed {
				alerts++
			}
			if ui.On {
				fmt.Printf("  %s %s  %s %s %s\n", ui.Badge(string(ev.To)), ui.Bold(r.n.Key()),
					ui.Status(string(ev.From), string(ev.From)), ui.Gray("→"), ui.Status(string(ev.To), string(ev.To)))
				if ev.To == store.Claimed {
					fmt.Println(ui.Gray(fmt.Sprintf("              registered %s · latest %s · %q", r.in.Created.Format("2006-01-02"), r.in.Latest, short(r.in.Summary, 60))))
				}
				continue
			}
			mark := "  "
			if ev.To == store.Claimed {
				mark = "🚨"
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
	if ui.On {
		if changes == 0 {
			fmt.Println(ui.Gray(fmt.Sprintf("  checked %s · no status changes since the last check", plural(len(targets)-failed, "name"))))
		}
		fmt.Println()
		rows := []string{ui.Stack(segments(counts), boxWidth()-4), legend(counts)}
		if failed > 0 {
			rows = append(rows, ui.Yellow(fmt.Sprintf("%s failed; they'll be retried next time", plural(failed, "lookup"))))
		}
		fmt.Println(ui.Box("watch list", ui.Gray(plural(len(db.Names), "name")), rows, boxWidth()))
		if alerts > 0 {
			verb := "were"
			if alerts == 1 {
				verb = "was"
			}
			fmt.Printf("\n%s %s\n", ui.Tag("claimed"), ui.Red(fmt.Sprintf("%s %s registered since the last check. Review before anyone installs them.", plural(alerts, "invented name"), verb)))
			return 3, nil
		}
		fmt.Println(ui.Green("  ✓ no invented names were registered since the last check"))
		return 0, nil
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
	w := 0
	for _, d := range deps {
		if l := len(d.eco) + 1 + len(d.name); l > w {
			w = l
		}
	}
	live := ui.NewLive("")
	defer live.Stop()
	// row prints one result: a badge-style verdict, the name and the reason.
	row := func(verdict, key, why string) {
		if !ui.On {
			fmt.Printf("%s %-*s  %s\n", verdict, w, key, why)
			return
		}
		name := ui.Pad(key, w)
		switch verdict {
		case "FAIL":
			why = ui.Red(why)
		case "WARN":
			why = ui.Yellow(why)
		default:
			name, why = ui.Gray(name), ui.Gray(why)
		}
		live.Println(fmt.Sprintf("  %s %s  %s", ui.BadgeW(verdict, 4), name, why))
	}
	for i, d := range deps {
		key := string(d.eco) + "/" + d.name
		if ui.On {
			live.Set(fmt.Sprintf("checking %s  %s %s", ui.Bar(i, len(deps), 20), ui.Gray(fmt.Sprintf("%d/%d", i+1, len(deps))), key))
		}
		in, err := reg.Lookup(ctx, d.eco, d.name)
		if err != nil {
			if ui.On {
				live.Println(fmt.Sprintf("  %s %s  %s", ui.Gray("  ?   "), ui.Pad(key, w), ui.Gray("lookup failed: "+err.Error())))
			} else {
				fmt.Printf("  ?  %-*s  lookup failed: %v\n", w, key, err)
			}
			continue
		}
		known := db.Names[key]
		switch {
		case !in.Exists:
			fails++
			why := "does not exist on the registry"
			if known != nil {
				why += fmt.Sprintf(" (AI models have suggested it %s)", plural(known.Hits, "time"))
			}
			row("FAIL", key, why)
		case known != nil && known.Status == store.Claimed:
			fails++
			row("FAIL", key, fmt.Sprintf("registered %s, after AI models were already inventing it", in.Created.Format("2006-01-02")))
		case !in.Created.IsZero() && time.Since(in.Created) < *minAge:
			warns++
			row("WARN", key, fmt.Sprintf("first published %d days ago", int(time.Since(in.Created).Hours()/24)))
		case known != nil && known.Status == store.Young:
			warns++
			row("WARN", key, "published shortly before models started suggesting it")
		case ui.On:
			row("OK", key, fmt.Sprintf("since %s · %s", in.Created.Format("2006"), plural(in.Releases, "release")))
		}
	}
	live.Stop()
	noun := "dependencies"
	if len(deps) == 1 {
		noun = "dependency"
	}
	failed := fails > 0 || (*strict && warns > 0)
	if ui.On {
		verdict := ui.BadgeW("passed", 6)
		if failed {
			verdict = ui.BadgeW("failed", 6)
		}
		f := fmt.Sprintf("%d failed", fails)
		if fails > 0 {
			f = ui.Red(f)
		}
		wn := plural(warns, "warning")
		if warns > 0 {
			wn = ui.Yellow(wn)
		}
		fmt.Printf("\n%s  checked %d %s from %s: %s, %s\n", verdict, len(deps), noun, strings.Join(files, ", "), f, wn)
	} else {
		fmt.Printf("\nChecked %d %s from %s: %d failed, %s.\n", len(deps), noun, strings.Join(files, ", "), fails, plural(warns, "warning"))
	}
	if failed {
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
	if ui.On {
		fmt.Printf("\n  %s %s\n\n", ui.Gradient("seeding"), ui.Gray("from "+*source))
	}
	live := ui.NewLive("")
	defer live.Stop()
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
		if ui.On {
			live.Set("looking up " + n.Key())
		}
		in, err := reg.Lookup(ctx, eco, name)
		if err != nil {
			return err
		}
		n.Update(in, now)
		added++
		if ui.On {
			live.Println(fmt.Sprintf("  %s %s  %s", ui.Badge(string(n.Status)), ui.Pad(ui.Bold(n.Key()), 32), seedNote(n, in)))
		} else {
			fmt.Printf("%-9s %s\n", n.Status, n.Key())
		}
	}
	live.Stop()
	if err := db.Save(); err != nil {
		return err
	}
	if ui.On {
		fmt.Printf("\n  %s %s %s\n", ui.Green("✓"), "watching "+plural(added, "name")+" from "+*source+".", ui.Gray("next: ")+ui.Cyan("phantom dash")+ui.Gray(" · ")+ui.Cyan("phantom watch"))
		return nil
	}
	fmt.Printf("\nWatching %d names from %s. Run phantom watch on a schedule.\n", added, *source)
	return nil
}

// seedNote explains a seeded name's status in a few words.
func seedNote(n *store.Name, in registry.Info) string {
	listed := n.FirstSeen.Format("2006-01-02")
	switch n.Status {
	case store.Claimed:
		if d := daysBetween(n.FirstSeen, in.Created); d > 0 {
			return ui.Red(fmt.Sprintf("registered %s, %d days after it was listed", in.Created.Format("2006-01-02"), d))
		}
		return ui.Red("registered after it was seen missing")
	case store.Young:
		return ui.Yellow(fmt.Sprintf("published %s, shortly before it was listed on %s", in.Created.Format("2006-01-02"), listed))
	case store.Unclaimed:
		return ui.Purple("not registered yet")
	default:
		return ui.Gray(fmt.Sprintf("established, since %s", in.Created.Format("2006")))
	}
}

func daysBetween(a, b time.Time) int { return int(b.Sub(a).Hours() / 24) }

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
		if ui.On {
			fmt.Println(checkCard(key, in, db.Names[key]))
			continue
		}
		if !in.Exists {
			fmt.Printf("%s: does not exist", key)
			if in.Note != "" {
				fmt.Printf(" (%s)", in.Note)
			}
		} else {
			fmt.Printf("%s: exists, first published %s, %s, latest %s", key, in.Created.Format("2006-01-02"), plural(in.Releases, "release"), in.Latest)
			if in.Summary != "" {
				fmt.Printf("\n    %q", in.Summary)
			}
		}
		if n := db.Names[key]; n != nil {
			var src []string
			for m := range n.Models {
				src = append(src, m)
			}
			sort.Strings(src)
			fmt.Printf("\n    Phantom: %s. First seen invented %s, source: %s", strings.ToUpper(string(n.Status)), n.FirstSeen.Format("2006-01-02"), strings.Join(src, ", "))
		}
		fmt.Println()
	}
	return nil
}

// checkCard draws one looked-up name as a panel.
func checkCard(key string, in registry.Info, n *store.Name) string {
	right := ui.Gray("not watched")
	if n != nil {
		right = ui.Tag(string(n.Status))
	}
	const lw = 11
	field := func(label, v string) string { return ui.Gray(fmt.Sprintf("%-*s", lw, label)) + v }
	inner := boxWidth() - 4 - lw
	var rows []string
	if !in.Exists {
		v := ui.Purple("does not exist")
		if in.Note != "" {
			v += ui.Gray(" (" + in.Note + ")")
		}
		rows = append(rows, field("registry", v))
	} else {
		rows = append(rows,
			field("published", in.Created.Format("2006-01-02")+ui.Gray(" · "+ui.Ago(in.Created))),
			field("releases", fmt.Sprintf("%s · latest %s", plural(in.Releases, "release"), in.Latest)))
		for j, l := range ui.Wrap(in.Summary, inner, 2) {
			label := ""
			if j == 0 {
				label = "summary"
			}
			rows = append(rows, field(label, ui.Gray(l)))
		}
	}
	if n != nil {
		var src []string
		for m := range n.Models {
			src = append(src, m)
		}
		sort.Strings(src)
		rows = append(rows, "",
			field("invented", n.FirstSeen.Format("2006-01-02")+ui.Gray(" · first seen missing")),
			field("source", strings.Join(src, ", ")))
		if n.Status == store.Claimed && in.Exists {
			if d := daysBetween(n.FirstSeen, in.Created); d > 0 {
				rows = append(rows, field("", ui.Red(fmt.Sprintf("registered %d days after models were seen inventing it", d))))
			}
		}
	}
	return ui.Box(key, right, rows, boxWidth())
}

// ---------------------------------------------------------------- dash

func dash(args []string) error {
	fs := flag.NewFlagSet("dash", flag.ExitOnError)
	dbPath := fs.String("db", defaultDB(), "watch database")
	showUnclaimed := fs.Bool("show-unclaimed", false, "list unclaimed names too (keep them out of screenshots and public posts)")
	top := fs.Int("top", 8, "rows per panel")
	fs.Parse(args)

	db, err := store.Open(*dbPath)
	if err != nil {
		return err
	}
	if len(db.Names) == 0 {
		fmt.Println("Nothing watched yet. Try: phantom seed examples/seeds.txt, or phantom probe --model NAME")
		return nil
	}
	W := boxWidth()
	fmt.Print(ui.Banner(version, tagline, credit))

	counts := map[store.Status]int{}
	var last time.Time
	for _, n := range db.Names {
		counts[n.Status]++
		if n.CheckedAt.After(last) {
			last = n.CheckedAt
		}
	}
	fmt.Println(ui.Box("watch list", ui.Gray("last checked "+ui.Ago(last)), []string{
		ui.Stack(segments(counts), W-4),
		legend(counts),
	}, W))

	// Claimed and young names are already public; they are safe to show.
	for _, st := range []store.Status{store.Claimed, store.Young} {
		ns := db.Sorted(func(n *store.Name) bool { return n.Status == st })
		if len(ns) == 0 {
			if st == store.Claimed {
				fmt.Println(ui.Box("claimed", "", []string{ui.Green("none yet: no invented name has been registered")}, W))
			}
			continue
		}
		sort.SliceStable(ns, func(i, j int) bool { return ns[i].Registry.Created.After(ns[j].Registry.Created) })
		kw := 0
		for _, n := range limit(ns, *top) {
			kw = max(kw, len(n.Key()))
		}
		kw = min(kw, W-4-36)
		rows := []string{ui.Gray(ui.Pad("name", kw) + "  invented     registered   gap")}
		for _, n := range limit(ns, *top) {
			gap := ""
			if d := daysBetween(n.FirstSeen, n.Registry.Created); d > 0 {
				gap = fmt.Sprintf("+%dd", d)
			} else if d < 0 {
				gap = fmt.Sprintf("%dd", d)
			}
			rows = append(rows, fmt.Sprintf("%s  %s → %s  %s",
				ui.Pad(ui.Status(string(st), short(n.Key(), kw)), kw),
				n.FirstSeen.Format("2006-01-02"), ui.Bold(n.Registry.Created.Format("2006-01-02")), ui.Status(string(st), gap)))
		}
		if len(ns) > *top {
			rows = append(rows, ui.Gray(fmt.Sprintf("… and %d more", len(ns)-*top)))
		}
		fmt.Println(ui.Box(string(st), ui.Tag(string(st))+" "+ui.Gray(fmt.Sprint(len(ns))), rows, W))
	}

	// Hallucination rate per model.
	if runs := report.Aggregate(db.Runs); len(runs) > 0 {
		mw := 0
		for _, r := range runs {
			mw = max(mw, len(r.Model))
		}
		mw = min(mw, 24)
		var rows []string
		for _, r := range runs {
			rate := 0.0
			if r.Mentions > 0 {
				rate = 100 * float64(r.Hallucinated) / float64(r.Mentions)
			}
			rows = append(rows, fmt.Sprintf("%s  %s %s  %s", ui.Pad(ui.Cyan(short(r.Model, mw)), mw), ui.Meter(rate/100, 20, rateColor(rate)),
				ui.BoldColor(rateColor(rate), fmt.Sprintf("%5.1f%%", rate)), ui.Gray(fmt.Sprintf("%d of %d installs invented", r.Hallucinated, r.Mentions))))
		}
		fmt.Println(ui.Box("models", ui.Gray("invented install suggestions"), rows, W))
	} else {
		fmt.Println(ui.Box("models", "", []string{ui.Gray("no probes yet · ") + ui.Cyan("phantom probe --model qwen2.5-coder:1.5b")}, W))
	}

	// Recent status changes, without names that are still unclaimed.
	type change struct {
		n  *store.Name
		ev store.Event
	}
	var cs []change
	for _, n := range db.Names {
		if n.Status == store.Unclaimed && !*showUnclaimed {
			continue
		}
		for _, ev := range n.History {
			cs = append(cs, change{n, ev})
		}
	}
	if len(cs) > 0 {
		sort.Slice(cs, func(i, j int) bool { return cs[i].ev.At.After(cs[j].ev.At) })
		var rows []string
		for _, c := range cs[:min(len(cs), *top)] {
			rows = append(rows, fmt.Sprintf("%s  %s  %s %s %s", ui.Gray(c.ev.At.Format("2006-01-02")), ui.Bold(c.n.Key()),
				ui.Status(string(c.ev.From), string(c.ev.From)), ui.Gray("→"), ui.Status(string(c.ev.To), string(c.ev.To))))
		}
		fmt.Println(ui.Box("recent changes", "", rows, W))
	}

	if u := counts[store.Unclaimed]; u > 0 {
		if *showUnclaimed {
			ns := db.Sorted(func(n *store.Name) bool { return n.Status == store.Unclaimed })
			var rows []string
			for _, n := range limit(ns, *top) {
				rows = append(rows, fmt.Sprintf("%s  %s", ui.Purple(n.Key()), ui.Gray(fmt.Sprintf("suggested %s", plural(n.Hits, "time")))))
			}
			if len(ns) > *top {
				rows = append(rows, ui.Gray(fmt.Sprintf("… and %d more", len(ns)-*top)))
			}
			fmt.Println(ui.Box("unclaimed", ui.Red("private: don't publish"), rows, W))
		} else {
			fmt.Println(ui.Gray(fmt.Sprintf("  %s hidden. --show-unclaimed lists them; keep that out of screenshots and public posts.", plural(u, "unclaimed name"))))
		}
	}
	return nil
}

func limit(ns []*store.Name, n int) []*store.Name {
	if len(ns) > n {
		return ns[:n]
	}
	return ns
}

// ---------------------------------------------------------------- shared output helpers

const (
	tagline = "watching the package names AI models invent"
	credit  = "engineered by Krish Desai"
)

// boxWidth fits panels to the terminal, between 60 and 88 columns.
func boxWidth() int { return max(60, min(ui.Cols()-2, 88)) }

func kv(label, value string) string { return ui.Gray(fmt.Sprintf("%-22s", label)) + value }

// rateColor goes green → yellow → red as a hallucination rate climbs.
func rateColor(rate float64) int {
	switch {
	case rate < 5:
		return 114
	case rate < 15:
		return 221
	default:
		return 203
	}
}

var statusOrder = []store.Status{store.Claimed, store.Young, store.Unclaimed, store.Real}

func segments(counts map[store.Status]int) []ui.Segment {
	var segs []ui.Segment
	for _, s := range statusOrder {
		segs = append(segs, ui.Segment{N: counts[s], Status: string(s)})
	}
	return segs
}

func legend(counts map[store.Status]int) string {
	var parts []string
	for _, s := range statusOrder {
		parts = append(parts, ui.Status(string(s), "●")+" "+string(s)+" "+ui.Bold(fmt.Sprint(counts[s])))
	}
	return strings.Join(parts, "    ")
}

func printUsage() {
	if !ui.On {
		fmt.Print(usage)
		return
	}
	fmt.Print(ui.Banner(version, tagline, credit))
	for _, l := range strings.Split(usage, "\n")[2:] {
		if rest, ok := strings.CutPrefix(l, "  phantom "); ok {
			cmd, desc, _ := strings.Cut(rest, " ")
			args, help := desc, ""
			if i := strings.Index(desc, "   "); i >= 0 {
				args, help = desc[:i], strings.TrimSpace(desc[i:])
			}
			fmt.Printf("  %s %s %s\n", ui.Gray("phantom"), ui.Cyan(fmt.Sprintf("%-6s", cmd)), ui.Pad(ui.Gray(strings.TrimSpace(args)), 28)+help)
			continue
		}
		fmt.Println(l)
	}
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

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
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
