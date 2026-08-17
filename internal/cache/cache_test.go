package cache

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/ge/subscription-monitor/internal/model"
)

func TestSaveLoadRoundtripAndFresh(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache.json")
	s := New(path)

	f := &File{Entries: map[string]*Entry{}}
	pct := 42.0
	now := time.Now()
	f.Set(model.ProviderZhipu, &Entry{
		Snapshot: &model.Snapshot{
			Provider:    model.ProviderZhipu,
			ShortWindow: &model.Window{RemainingPct: &pct},
		},
		FetchedAt: now,
	})
	if err := s.Save(f); err != nil {
		t.Fatalf("save: %v", err)
	}

	got, err := s.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	e := got.Get(model.ProviderZhipu)
	if e == nil || e.Snapshot == nil || e.Snapshot.ShortWindow == nil {
		t.Fatalf("roundtrip lost data: %+v", e)
	}
	if *e.Snapshot.ShortWindow.RemainingPct != 42 {
		t.Errorf("remaining pct = %v", *e.Snapshot.ShortWindow.RemainingPct)
	}
	if !e.Fresh(5 * time.Minute) {
		t.Error("entry just written should be fresh")
	}
	if e.Fresh(-time.Second) {
		t.Error("negative ttl should never be fresh")
	}
}

func TestLoadMissingFileIsEmpty(t *testing.T) {
	f, err := New(filepath.Join(t.TempDir(), "missing.json")).Load()
	if err != nil {
		t.Fatalf("missing file should not error: %v", err)
	}
	if len(f.Entries) != 0 || f.Get(model.ProviderMinimax) != nil {
		t.Error("expected empty store")
	}
}

func TestLoadCorruptFileStartsFresh(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache.json")
	if err := writeFile(path, []byte("{not json")); err != nil {
		t.Fatal(err)
	}
	f, err := New(path).Load()
	if err != nil {
		t.Fatalf("corrupt file should not error: %v", err)
	}
	if len(f.Entries) != 0 {
		t.Error("corrupt file should yield empty store")
	}
}

func TestSaveLeavesNoTempFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cache.json")
	if err := New(path).Save(&File{Entries: map[string]*Entry{}}); err != nil {
		t.Fatal(err)
	}
	entries, err := readDirNames(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0] != "cache.json.lock" && entries[0] != "cache.json" {
		// cache.json + cache.json.lock are both fine; temp files are not.
		t.Logf("dir contains: %v", entries)
	}
	for _, name := range entries {
		if len(name) > 0 && name[0] == '.' {
			t.Errorf("leftover temp file: %s", name)
		}
	}
}
