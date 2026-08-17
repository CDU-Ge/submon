// Package cache persists provider snapshots and watch state in a single JSON
// file. Writers (status --force, watch) use an exclusive flock plus atomic
// rename; readers (statusline) take a shared lock so they never see a
// half-written file.
package cache

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/ge/subscription-monitor/internal/model"
	"golang.org/x/sys/unix"
)

// AlertState is watch's threshold state machine memory, persisted per provider.
type AlertState struct {
	// Level: "ok" | "warn" | "crit".
	Level string `json:"level"`
	// UpdatedAt records the last level transition.
	UpdatedAt time.Time `json:"updated_at"`
	// LastMessage is the human text of the last pushed notification.
	LastMessage string `json:"last_message,omitempty"`
}

// Entry is one provider's cached data.
type Entry struct {
	Snapshot   *model.Snapshot `json:"snapshot,omitempty"`
	LastErr    string          `json:"last_error,omitempty"`
	FetchedAt  time.Time       `json:"fetched_at"`
	AlertState AlertState      `json:"alert_state"`
}

type File struct {
	Entries map[string]*Entry `json:"entries"`
}

// Store wraps the cache file.
type Store struct {
	path string
}

func New(path string) *Store {
	return &Store{path: path}
}

// Load reads the cache file; missing file yields an empty store.
func (s *Store) Load() (*File, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return &File{Entries: map[string]*Entry{}}, nil
		}
		return nil, err
	}
	var f File
	if err := json.Unmarshal(data, &f); err != nil {
		// Corrupt cache: start fresh rather than failing statusline.
		return &File{Entries: map[string]*Entry{}}, nil
	}
	if f.Entries == nil {
		f.Entries = map[string]*Entry{}
	}
	return &f, nil
}

// Save atomically writes the file under an exclusive lock: write temp file in
// the same directory, then rename over the target.
func (s *Store) Save(f *File) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	lock, err := lockFile(s.path)
	if err != nil {
		return err
	}
	defer lock.Close()

	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".submon-cache-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after successful rename
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return err
	}
	return nil
}

// Get returns the entry for a provider (nil when absent).
func (f *File) Get(provider model.Provider) *Entry {
	return f.Entries[string(provider)]
}

// Set stores/updates an entry.
func (f *File) Set(provider model.Provider, e *Entry) {
	f.Entries[string(provider)] = e
}

// Fresh reports whether the entry's snapshot is younger than ttl.
func (e *Entry) Fresh(ttl time.Duration) bool {
	if e == nil || e.Snapshot == nil {
		return false
	}
	return time.Since(e.FetchedAt) < ttl
}

// lockFile opens/creates <path>.lock and takes an exclusive flock.
func lockFile(path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX); err != nil {
		f.Close()
		return nil, fmt.Errorf("flock %s: %w", path+".lock", err)
	}
	return f, nil
}
