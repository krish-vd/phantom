// Package store keeps Phantom's watch database: every package name models
// have suggested, how often, by which model, and what the registry said.
package store

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/krish-vd/phantom/internal/extract"
	"github.com/krish-vd/phantom/internal/registry"
)

// Status of a name, from the defender's point of view.
type Status string

const (
	// Real: an established package that existed before any model suggested it.
	Real Status = "real"
	// Unclaimed: models suggest it, but nobody has registered it. Anyone can.
	Unclaimed Status = "unclaimed"
	// Young: it exists, but was first published shortly before models started
	// suggesting it. Worth a look: it may be a squat that got there first.
	Young Status = "young"
	// Claimed: it did not exist when Phantom first saw a model invent it, and
	// someone has registered it since. This is what slopsquatting looks like.
	Claimed Status = "claimed"
)

// YoungWindow is how recent a package must be, relative to when models
// started suggesting it, to count as Young.
const YoungWindow = 90 * 24 * time.Hour

type Event struct {
	At   time.Time `json:"at"`
	From Status    `json:"from"`
	To   Status    `json:"to"`
	Note string    `json:"note,omitempty"`
}

type Name struct {
	Ecosystem   extract.Ecosystem `json:"ecosystem"`
	Name        string            `json:"name"`
	FirstSeen   time.Time         `json:"first_seen"`
	LastSeen    time.Time         `json:"last_seen"`
	Hits        int               `json:"hits"`
	InstallHits int               `json:"install_hits"`
	Models      map[string]int    `json:"models"`
	Prompts     []int             `json:"prompts,omitempty"`
	// WasMissing records that the name did not exist on at least one check.
	WasMissing bool          `json:"was_missing,omitempty"`
	Status     Status        `json:"status"`
	Registry   registry.Info `json:"registry"`
	CheckedAt  time.Time     `json:"checked_at"`
	History    []Event       `json:"history,omitempty"`
}

func (n *Name) Key() string { return string(n.Ecosystem) + "/" + n.Name }

type Run struct {
	At           time.Time `json:"at"`
	Model        string    `json:"model"`
	Prompts      int       `json:"prompts"`
	Samples      int       `json:"samples"`
	Responses    int       `json:"responses"`
	Errors       int       `json:"errors"`
	Mentions     int       `json:"mentions"`     // install-command mentions
	Hallucinated int       `json:"hallucinated"` // install mentions of names that don't exist
	Names        int       `json:"unique_names"` // unique names suggested
	Invented     int       `json:"unique_invented"`
}

type DB struct {
	Version int              `json:"version"`
	Names   map[string]*Name `json:"names"`
	Runs    []Run            `json:"runs"`
	path    string
}

func Open(path string) (*DB, error) {
	db := &DB{Version: 1, Names: map[string]*Name{}, path: path}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return db, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, db); err != nil {
		return nil, err
	}
	if db.Names == nil {
		db.Names = map[string]*Name{}
	}
	db.path = path
	return db, nil
}

// Save writes atomically so a crash never leaves a half-written database.
func (db *DB) Save() error {
	b, err := json.MarshalIndent(db, "", " ")
	if err != nil {
		return err
	}
	if dir := filepath.Dir(db.path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	tmp := db.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, db.path)
}

// Record notes that a model suggested a name.
func (db *DB) Record(m extract.Mention, model string, promptID int, at time.Time) *Name {
	n, ok := db.Names[m.Key()]
	if !ok {
		n = &Name{Ecosystem: m.Ecosystem, Name: m.Name, FirstSeen: at, Models: map[string]int{}}
		db.Names[m.Key()] = n
	}
	n.LastSeen = at
	n.Hits++
	if m.Source == extract.FromInstall {
		n.InstallHits++
	}
	n.Models[model]++
	if promptID > 0 && !containsInt(n.Prompts, promptID) && len(n.Prompts) < 50 {
		n.Prompts = append(n.Prompts, promptID)
		sort.Ints(n.Prompts)
	}
	return n
}

// Update applies a fresh registry lookup and returns the transition, if any.
func (n *Name) Update(in registry.Info, at time.Time) *Event {
	if !in.Exists {
		n.WasMissing = true
	}
	next := Classify(n, in)
	prev := n.Status
	n.Registry, n.CheckedAt, n.Status = in, at, next
	if prev == "" || prev == next {
		return nil
	}
	ev := Event{At: at, From: prev, To: next}
	n.History = append(n.History, ev)
	return &ev
}

// Classify decides a name's status from its history and a registry lookup.
func Classify(n *Name, in registry.Info) Status {
	switch {
	case !in.Exists:
		return Unclaimed
	case n.WasMissing || (!in.Created.IsZero() && in.Created.After(n.FirstSeen)):
		return Claimed
	case in.Created.IsZero() || n.FirstSeen.Sub(in.Created) < YoungWindow:
		return Young
	default:
		return Real
	}
}

// Sorted returns names ordered by hits, most suggested first.
func (db *DB) Sorted(filter func(*Name) bool) []*Name {
	var out []*Name
	for _, n := range db.Names {
		if filter == nil || filter(n) {
			out = append(out, n)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Hits != out[j].Hits {
			return out[i].Hits > out[j].Hits
		}
		return out[i].Key() < out[j].Key()
	})
	return out
}

func containsInt(xs []int, x int) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
