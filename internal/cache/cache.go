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

// Sample is one point of usage history (percentages of used) used by the
// predictor to estimate burn rates and the 5h↔weekly conversion.
type Sample struct {
	T         time.Time `json:"t"`
	ShortUsed float64   `json:"su,omitempty"`
	LongUsed  float64   `json:"lu,omitempty"`
}

// Entry is one provider's cached data.
type Entry struct {
	Snapshot *model.Snapshot `json:"snapshot,omitempty"`
	LastErr  string          `json:"last_error,omitempty"`
	// FetchedAt is when the snapshot data was fetched.
	FetchedAt time.Time `json:"fetched_at"`
	// RefreshedAt is when a refresh was last *attempted* (success or not).
	// statusline's self-heal uses this to avoid hammering the API when it
	// keeps failing (e.g. system just woke up, network down): it retries at
	// most once per TTL/2.
	RefreshedAt time.Time  `json:"refreshed_at,omitempty"`
	AlertState  AlertState `json:"alert_state"`
	// History of usage samples (watch appends one per run, bounded).
	History []Sample `json:"history,omitempty"`
}

// AppendSample adds a usage sample, pruning to keep at most maxSamples
// (default enough for ~7 days of 5-minute samples).
//
//	label 空 → 等同 AppendSample(provider, s, max)(老路径);非空 →
//	写到 "<provider>/<label>" 这条 Entry(多 key 场景)。
func (f *File) AppendSample(provider model.Provider, s Sample, maxSamples int) {
	f.appendSample(provider, "", s, maxSamples)
}

func (f *File) appendSample(provider model.Provider, label string, s Sample, maxSamples int) {
	key := keyFor(provider, label)
	e := f.Entries[key]
	if e == nil {
		e = &Entry{}
		f.Entries[key] = e
	}
	e.History = append(e.History, s)
	if maxSamples <= 0 {
		maxSamples = 2100
	}
	if len(e.History) > maxSamples {
		e.History = e.History[len(e.History)-maxSamples:]
	}
}

// keyFor 把 (provider, label) 编码成 Entries map 的 key。
//
//	label 空 或 "default" → 只用 provider 名(向后兼容老 cache.json 与
//	顶层 default Key);其他 label → "<provider>/<label>",保证不会与
//	默认 key 冲突。
//
//	"default" 与 "" 等价:ProviderKeys 把顶层 Token 规范化为
//	label="default",存储路径必须和老格式 single-key 用户保持一致。
func keyFor(provider model.Provider, label string) string {
	if label == "" || label == "default" {
		return string(provider)
	}
	return string(provider) + "/" + label
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

// Get returns the entry for a provider's default key (label 空)。
// 拿到 nil 时不代表"该 provider 没缓存",可能是 [[keys]] 里某个 label
// 才有缓存 — 多 key 场景用 GetLabeled。
func (f *File) Get(provider model.Provider) *Entry {
	return f.Entries[string(provider)]
}

// Set stores/updates an entry for the default key(label 空)。
func (f *File) Set(provider model.Provider, e *Entry) {
	f.Entries[string(provider)] = e
}

// GetLabeled returns the entry for a (provider, label) pair. 多 key
// 场景使用;label 为空时回落到 Get(provider)。
func (f *File) GetLabeled(provider model.Provider, label string) *Entry {
	return f.Entries[keyFor(provider, label)]
}

// SetLabeled stores/updates an entry for a (provider, label) pair。
// Label 空 → 写入 provider 名下(等同 Set)。
func (f *File) SetLabeled(provider model.Provider, label string, e *Entry) {
	f.Entries[keyFor(provider, label)] = e
}

// Entries returns the underlying map (read-only by convention; callers must
// not mutate returned *Entry pointers in place without saving back).
func (f *File) EntriesByLabel() map[string]*Entry {
	return f.Entries
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
