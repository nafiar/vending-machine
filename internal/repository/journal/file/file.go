// Package file is the on-disk journal: one small JSON file per transaction,
// written before each step that can't be repeated, and deleted once the server
// has the final result.
package file

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"vending-machine/internal/model"
	"vending-machine/internal/repository/journal"
)

// File is the folder of unfinished purchases. The copy in memory is the live
// state; the files are what a restart reads back. Build one in cmd/main.
type File struct {
	dir     string
	mu      sync.Mutex
	entries map[string]model.JournalEntry
	now     func() time.Time
}

var _ journal.Repository = (*File)(nil)

// New reads the folder, creating it if needed. Files it can't read are
// renamed to *.bad and listed in problems, so one damaged file doesn't stop
// the agent.
func New(dir string) (j *File, problems []string, err error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, nil, err
	}
	j = &File{dir: dir, entries: map[string]model.JournalEntry{}, now: time.Now}
	names, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil, err
	}
	for _, f := range names {
		name := f.Name()
		path := filepath.Join(dir, name)
		switch {
		case strings.HasSuffix(name, ".tmp"):
			_ = os.Remove(path) // a write that never finished; its previous version (if any) is intact
			continue
		case !strings.HasSuffix(name, ".json"):
			continue
		}
		data, err := os.ReadFile(path)
		var e model.JournalEntry
		if err == nil {
			err = json.Unmarshal(data, &e)
		}
		if err == nil && (!model.ValidTxID(e.TxID) || e.TxID+".json" != name) {
			err = errors.New("the file name and txId don't match")
		}
		if err != nil {
			_ = os.Rename(path, path+".bad")
			problems = append(problems, fmt.Sprintf("%s: %v (renamed to %s.bad)", name, err, name))
			continue
		}
		j.entries[e.TxID] = e
	}
	return j, problems, nil
}

// Dir is the journal folder.
func (j *File) Dir() string { return j.dir }

// Put saves an entry: in memory first, then on disk with an atomic replace.
// A disk error is returned, but the memory copy stays updated.
func (j *File) Put(e model.JournalEntry) error {
	if !model.ValidTxID(e.TxID) {
		return fmt.Errorf("journal: %q is not a transaction id", e.TxID)
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	now := j.now().UTC()
	if old, ok := j.entries[e.TxID]; ok && e.CreatedAt.IsZero() {
		e.CreatedAt = old.CreatedAt
	}
	if e.CreatedAt.IsZero() {
		e.CreatedAt = now
	}
	e.UpdatedAt = now
	j.entries[e.TxID] = e
	data, err := json.MarshalIndent(e, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(j.dir, e.TxID+".json"), data)
}

// Get returns the entry for a transaction.
func (j *File) Get(id string) (model.JournalEntry, bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	e, ok := j.entries[id]
	return e, ok
}

// Delete removes a finished transaction.
func (j *File) Delete(id string) error {
	if !model.ValidTxID(id) {
		return nil
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	delete(j.entries, id)
	err := os.Remove(filepath.Join(j.dir, id+".json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// List returns the entries, oldest first.
func (j *File) List() []model.JournalEntry {
	j.mu.Lock()
	defer j.mu.Unlock()
	out := make([]model.JournalEntry, 0, len(j.entries))
	for _, e := range j.entries {
		out = append(out, e)
	}
	sort.Slice(out, func(a, b int) bool {
		if !out[a].CreatedAt.Equal(out[b].CreatedAt) {
			return out[a].CreatedAt.Before(out[b].CreatedAt)
		}
		return out[a].TxID < out[b].TxID
	})
	return out
}

// Len is the number of unfinished purchases.
func (j *File) Len() int {
	j.mu.Lock()
	defer j.mu.Unlock()
	return len(j.entries)
}

// writeFileAtomic writes to a temporary file, flushes it to the disk, then
// renames it over the old one, so a power cut leaves either version, whole.
func writeFileAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	if runtime.GOOS != "windows" { // NTFS records the rename itself; elsewhere sync the folder too
		if d, err := os.Open(filepath.Dir(path)); err == nil {
			_ = d.Sync()
			d.Close()
		}
	}
	return nil
}
