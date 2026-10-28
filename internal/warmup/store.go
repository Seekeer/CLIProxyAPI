package warmup

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// storeVersion identifies the on-disk layout of stateFile.
const storeVersion = 1

// lastRunRetention bounds how many per-slot records are kept for reporting.
// The idempotency decision only needs the newest completed slot, but keeping a
// short history makes "did it fire at 06:00?" answerable from the file.
const lastRunRetention = 50

// runRecord is one completed warmup slot, retained for operator visibility.
type runRecord struct {
	Slot        time.Time          `json:"slot"`
	CompletedAt time.Time          `json:"completed_at"`
	Model       string             `json:"model,omitempty"`
	Results     []CredentialResult `json:"results,omitempty"`
}

// stateFile is the persisted idempotency state.
type stateFile struct {
	Version int `json:"version"`
	// LastCompletedSlot is the newest slot whose warmup run finished. It is
	// what prevents a restart from repeating a warmup.
	LastCompletedSlot time.Time `json:"last_completed_slot"`
	// Runs keeps the most recent completed slots, newest last.
	Runs []runRecord `json:"runs,omitempty"`
}

// CredentialResult is the per-credential outcome recorded for a slot.
type CredentialResult struct {
	AuthID     string `json:"auth_id"`
	Account    string `json:"account,omitempty"`
	Status     string `json:"status"`
	Message    string `json:"message,omitempty"`
	HTTPStatus int    `json:"http_status,omitempty"`
	// UsedPercent and ResetAfterSeconds mirror the Codex five-hour window
	// headers when the upstream reported them.
	UsedPercent       *float64 `json:"used_percent,omitempty"`
	ResetAfterSeconds *float64 `json:"reset_after_seconds,omitempty"`
}

// store persists idempotency state so a restart never repeats a warmup.
// An empty path keeps state in memory only.
type store struct {
	mu   sync.Mutex
	path string
	data stateFile
}

func newStore(path string) (*store, error) {
	s := &store{path: strings.TrimSpace(path)}
	if s.path == "" {
		s.data = stateFile{Version: storeVersion}
		return s, nil
	}
	raw, errRead := os.ReadFile(s.path)
	if errRead != nil {
		if os.IsNotExist(errRead) {
			s.data = stateFile{Version: storeVersion}
			return s, nil
		}
		return nil, fmt.Errorf("read warmup state %s: %w", s.path, errRead)
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		s.data = stateFile{Version: storeVersion}
		return s, nil
	}
	var loaded stateFile
	if errUnmarshal := json.Unmarshal(raw, &loaded); errUnmarshal != nil {
		// A corrupt file must not silently allow a duplicate warmup, but it also
		// must not wedge the scheduler. Start clean and report through the caller.
		return nil, fmt.Errorf("parse warmup state %s: %w", s.path, errUnmarshal)
	}
	if loaded.Version == 0 {
		loaded.Version = storeVersion
	}
	s.data = loaded
	return s, nil
}

// LastCompleted reports the newest completed slot, or the zero time.
func (s *store) LastCompleted() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.data.LastCompletedSlot
}

// RecordSlot stores the outcome of a finished run and reports whether the slot
// was newly recorded. A slot already present (for example because another
// worker persisted it first) is left untouched.
func (s *store) RecordSlot(record runRecord) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.data.LastCompletedSlot.IsZero() && !record.Slot.After(s.data.LastCompletedSlot) {
		// Slots older than the newest completed one are never re-applied; the
		// only thing that can still happen is refreshing an existing record.
		for i := range s.data.Runs {
			if s.data.Runs[i].Slot.Equal(record.Slot) {
				s.data.Runs[i] = record
				return false, s.persistLocked()
			}
		}
		return false, nil
	}
	s.data.Version = storeVersion
	if record.Slot.After(s.data.LastCompletedSlot) {
		s.data.LastCompletedSlot = record.Slot
	}
	replaced := false
	for i := range s.data.Runs {
		if s.data.Runs[i].Slot.Equal(record.Slot) {
			s.data.Runs[i] = record
			replaced = true
			break
		}
	}
	if !replaced {
		s.data.Runs = append(s.data.Runs, record)
	}
	sort.Slice(s.data.Runs, func(i, j int) bool { return s.data.Runs[i].Slot.Before(s.data.Runs[j].Slot) })
	if len(s.data.Runs) > lastRunRetention {
		s.data.Runs = append([]runRecord(nil), s.data.Runs[len(s.data.Runs)-lastRunRetention:]...)
	}
	return true, s.persistLocked()
}

// persistLocked writes the state atomically (write + rename) so an interrupted
// write cannot leave a truncated file that would allow a duplicate warmup.
func (s *store) persistLocked() error {
	if s.path == "" {
		return nil
	}
	encoded, errMarshal := json.MarshalIndent(s.data, "", "  ")
	if errMarshal != nil {
		return fmt.Errorf("encode warmup state: %w", errMarshal)
	}
	encoded = append(encoded, '\n')
	dir := filepath.Dir(s.path)
	if errMkdir := os.MkdirAll(dir, 0o755); errMkdir != nil {
		return fmt.Errorf("create warmup state dir: %w", errMkdir)
	}
	tmp, errCreate := os.CreateTemp(dir, ".warmup-state-*")
	if errCreate != nil {
		return fmt.Errorf("create warmup state temp file: %w", errCreate)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if _, errWrite := tmp.Write(encoded); errWrite != nil {
		_ = tmp.Close()
		return fmt.Errorf("write warmup state: %w", errWrite)
	}
	if errSync := tmp.Sync(); errSync != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync warmup state: %w", errSync)
	}
	if errClose := tmp.Close(); errClose != nil {
		return fmt.Errorf("close warmup state: %w", errClose)
	}
	if errRename := os.Rename(tmpName, s.path); errRename != nil {
		return fmt.Errorf("replace warmup state: %w", errRename)
	}
	return nil
}
