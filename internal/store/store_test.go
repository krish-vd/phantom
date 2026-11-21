package store

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/krish-vd/phantom/internal/extract"
	"github.com/krish-vd/phantom/internal/registry"
)

func TestLifecycle(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "db.json"))
	if err != nil {
		t.Fatal(err)
	}
	day1 := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	m := extract.Mention{Ecosystem: extract.PyPI, Name: "fastapi-turbo", Source: extract.FromInstall}

	n := db.Record(m, "qwen", 3, day1)
	if ev := n.Update(registry.Info{Exists: false}, day1); ev != nil || n.Status != Unclaimed {
		t.Fatalf("want unclaimed with no event, got %v %v", n.Status, ev)
	}

	// Someone registers it a week later.
	day8 := day1.AddDate(0, 0, 7)
	ev := n.Update(registry.Info{Exists: true, Created: day8.Add(-time.Hour)}, day8)
	if ev == nil || ev.From != Unclaimed || ev.To != Claimed {
		t.Fatalf("want unclaimed→claimed, got %+v", ev)
	}

	// Stays claimed even if the creation date looked old (WasMissing wins).
	if s := Classify(n, registry.Info{Exists: true, Created: day1.AddDate(-5, 0, 0)}); s != Claimed {
		t.Fatalf("want claimed, got %s", s)
	}

	if err := db.Save(); err != nil {
		t.Fatal(err)
	}
	db2, err := Open(db.path)
	if err != nil || db2.Names["pypi/fastapi-turbo"].Status != Claimed || len(db2.Names["pypi/fastapi-turbo"].History) != 1 {
		t.Fatalf("round trip failed: %v %+v", err, db2.Names)
	}
}

func TestClassifyExisting(t *testing.T) {
	seen := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	n := &Name{FirstSeen: seen}
	if s := Classify(n, registry.Info{Exists: true, Created: seen.AddDate(-3, 0, 0)}); s != Real {
		t.Errorf("old package: got %s", s)
	}
	if s := Classify(n, registry.Info{Exists: true, Created: seen.AddDate(0, 0, -20)}); s != Young {
		t.Errorf("20-day-old package: got %s", s)
	}
	if s := Classify(n, registry.Info{Exists: true, Created: seen.AddDate(0, 0, 2)}); s != Claimed {
		t.Errorf("created after first seen: got %s", s)
	}
}
