// Package report renders the watch database as Markdown and as a JSON feed.
package report

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/krish-vd/phantom/internal/store"
)

// Public mode withholds the names of unclaimed packages. Publishing a list
// of names that models invent and nobody owns yet is a shopping list for
// attackers; those go to registry security teams instead. Names that are
// already registered are safe and useful to publish: defenders need them
// to block installs.
type Options struct {
	Public bool
	Top    int
}

func Markdown(db *store.DB, o Options) string {
	if o.Top == 0 {
		o.Top = 25
	}
	var b strings.Builder
	now := time.Now().UTC()
	fmt.Fprintf(&b, "# Phantom report\n\nGenerated %s. ", now.Format("2 Jan 2006 15:04 MST"))
	fmt.Fprintf(&b, "%d package names tracked across %d probe runs.\n\n", len(db.Names), len(db.Runs))

	if len(db.Runs) > 0 {
		b.WriteString("## Models\n\n")
		b.WriteString("| Model | Responses | Install suggestions | Invented | Rate | Unique invented names |\n")
		b.WriteString("|---|---:|---:|---:|---:|---:|\n")
		for _, r := range Aggregate(db.Runs) {
			r.Invented = 0
			for _, n := range db.Names {
				if n.Models[r.Model] > 0 && n.WasMissing && n.InstallHits > 0 {
					r.Invented++
				}
			}
			rate := 0.0
			if r.Mentions > 0 {
				rate = 100 * float64(r.Hallucinated) / float64(r.Mentions)
			}
			fmt.Fprintf(&b, "| `%s` | %d | %d | %d | %.1f%% | %d |\n", r.Model, r.Responses, r.Mentions, r.Hallucinated, rate, r.Invented)
		}
		b.WriteString("\n_Rate = install commands naming a package that did not exist at probe time, divided by all install commands._\n\n")
	}

	claimed := db.Sorted(func(n *store.Name) bool { return n.Status == store.Claimed })
	b.WriteString("## 🚨 Claimed after models invented them\n\n")
	if len(claimed) == 0 {
		b.WriteString("None so far. Phantom re-checks every invented name on each `phantom watch`.\n\n")
	} else {
		b.WriteString("These names did not exist when a model first suggested them. Someone has registered them since. Some will be legitimate; review each one before anyone installs it.\n\n")
		b.WriteString("| Package | Registered | First invented | Suggested by | Hits |\n|---|---|---|---|---:|\n")
		for _, n := range claimed {
			fmt.Fprintf(&b, "| `%s` | %s | %s | %s | %d |\n", n.Key(), day(n.Registry.Created), day(n.FirstSeen), models(n), n.Hits)
		}
		b.WriteString("\n")
	}

	young := db.Sorted(func(n *store.Name) bool { return n.Status == store.Young })
	b.WriteString("## Young packages models recommend\n\n")
	if len(young) == 0 {
		b.WriteString("None.\n\n")
	} else {
		b.WriteString("Published less than 90 days before models started suggesting them. Often legitimate, sometimes a squat that got there first.\n\n")
		b.WriteString("| Package | Published | Releases | Suggested by | Hits |\n|---|---|---:|---|---:|\n")
		for _, n := range limit(young, o.Top) {
			fmt.Fprintf(&b, "| `%s` | %s | %d | %s | %d |\n", n.Key(), day(n.Registry.Created), n.Registry.Releases, models(n), n.Hits)
		}
		b.WriteString("\n")
	}

	unclaimed := db.Sorted(func(n *store.Name) bool { return n.Status == store.Unclaimed && n.InstallHits > 0 })
	shared := 0
	for _, n := range unclaimed {
		if len(n.Models) > 1 {
			shared++
		}
	}
	b.WriteString("## Invented and still unclaimed\n\n")
	fmt.Fprintf(&b, "%d names were suggested in install commands but do not exist. %d of them were invented by more than one model, which makes them the most attractive targets.\n\n", len(unclaimed), shared)
	if o.Public {
		py, js := 0, 0
		for _, n := range unclaimed {
			if n.Ecosystem == "pypi" {
				py++
			} else {
				js++
			}
		}
		fmt.Fprintf(&b, "PyPI: %d · npm: %d. Names are withheld from public reports and shared with registry security teams instead.\n", py, js)
	} else {
		b.WriteString("> Private report. Don't publish this list: it tells attackers which names to register.\n\n")
		b.WriteString("| Package | Hits | Models | Example prompts |\n|---|---:|---|---|\n")
		for _, n := range limit(unclaimed, o.Top) {
			fmt.Fprintf(&b, "| `%s` | %d | %s | %s |\n", n.Key(), n.Hits, models(n), ints(n.Prompts, 5))
		}
	}
	return b.String()
}

// Feed is the public, machine-readable output: a blocklist of names that
// models invented and someone then registered, plus suspicious young ones.
type Feed struct {
	Generated time.Time   `json:"generated"`
	Claimed   []FeedEntry `json:"claimed"`
	Young     []FeedEntry `json:"young"`
	Stats     FeedStats   `json:"stats"`
}

type FeedEntry struct {
	Ecosystem  string    `json:"ecosystem"`
	Name       string    `json:"name"`
	Registered time.Time `json:"registered,omitempty"`
	FirstSeen  time.Time `json:"first_invented"`
	Models     []string  `json:"models"`
	Hits       int       `json:"hits"`
}

type FeedStats struct {
	Tracked           int `json:"tracked"`
	UnclaimedInvented int `json:"unclaimed_invented"`
	Runs              int `json:"runs"`
}

func JSONFeed(db *store.DB) ([]byte, error) {
	f := Feed{Generated: time.Now().UTC(), Claimed: []FeedEntry{}, Young: []FeedEntry{}}
	for _, n := range db.Sorted(nil) {
		e := FeedEntry{string(n.Ecosystem), n.Name, n.Registry.Created, n.FirstSeen, modelList(n), n.Hits}
		switch n.Status {
		case store.Claimed:
			f.Claimed = append(f.Claimed, e)
		case store.Young:
			f.Young = append(f.Young, e)
		case store.Unclaimed:
			if n.InstallHits > 0 {
				f.Stats.UnclaimedInvented++
			}
		}
	}
	f.Stats.Tracked, f.Stats.Runs = len(db.Names), len(db.Runs)
	return json.MarshalIndent(f, "", "  ")
}

// Aggregate sums runs per model, keeping the order models first appeared.
func Aggregate(runs []store.Run) []store.Run {
	by := map[string]*store.Run{}
	var order []string
	for _, r := range runs {
		a, ok := by[r.Model]
		if !ok {
			a = &store.Run{Model: r.Model}
			by[r.Model] = a
			order = append(order, r.Model)
		}
		a.Responses += r.Responses
		a.Mentions += r.Mentions
		a.Hallucinated += r.Hallucinated
	}
	out := make([]store.Run, 0, len(order))
	for _, m := range order {
		out = append(out, *by[m])
	}
	return out
}

func modelList(n *store.Name) []string {
	var ms []string
	for m := range n.Models {
		ms = append(ms, m)
	}
	sort.Strings(ms)
	return ms
}

func models(n *store.Name) string {
	ms := modelList(n)
	for i := range ms {
		ms[i] = "`" + ms[i] + "`"
	}
	return strings.Join(ms, ", ")
}

func ints(xs []int, max int) string {
	var s []string
	for i, x := range xs {
		if i == max {
			s = append(s, "…")
			break
		}
		s = append(s, fmt.Sprintf("#%d", x))
	}
	return strings.Join(s, " ")
}

func day(t time.Time) string {
	if t.IsZero() {
		return "unknown"
	}
	return t.UTC().Format("2006-01-02")
}

func limit(ns []*store.Name, n int) []*store.Name {
	if len(ns) > n {
		return ns[:n]
	}
	return ns
}
