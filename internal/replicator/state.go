package replicator

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type replicationStatus string

const (
	statusQueued    replicationStatus = "queued"
	statusRunning   replicationStatus = "running"
	statusCompleted replicationStatus = "completed"
	statusFailed    replicationStatus = "failed"
	statusSkipped   replicationStatus = "skipped"
)

type JobState struct {
	CID             string            `json:"cid"`
	Filename        string            `json:"filename"`
	AnnouncedAt     string            `json:"announced_at"`
	StatusByNode    map[string]string `json:"status_by_node"`
	AttemptsByNode  map[string]int    `json:"attempts_by_node"`
	LastErrorByNode map[string]string `json:"last_error_by_node"`
}

type State struct {
	LastSeenAnnouncedAt string               `json:"last_seen_announced_at"`
	Jobs                map[string]*JobState `json:"jobs"`
}

type Store struct {
	path  string
	mu    sync.Mutex
	state State
}

func NewStore(path string) (*Store, error) {
	s := &Store{
		path: path,
		state: State{
			Jobs: make(map[string]*JobState),
		},
	}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) Snapshot() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	cloned := State{
		LastSeenAnnouncedAt: s.state.LastSeenAnnouncedAt,
		Jobs:                make(map[string]*JobState, len(s.state.Jobs)),
	}
	for cid, job := range s.state.Jobs {
		cp := *job
		cp.StatusByNode = cloneMap(job.StatusByNode)
		cp.AttemptsByNode = cloneIntMap(job.AttemptsByNode)
		cp.LastErrorByNode = cloneMap(job.LastErrorByNode)
		cloned.Jobs[cid] = &cp
	}
	return cloned
}

func (s *Store) UpsertJob(job *JobState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.Jobs[job.CID] = job
	return s.persistLocked()
}

func (s *Store) GetJob(cid string) *JobState {
	s.mu.Lock()
	defer s.mu.Unlock()
	j := s.state.Jobs[cid]
	if j == nil {
		return nil
	}
	cp := *j
	cp.StatusByNode = cloneMap(j.StatusByNode)
	cp.AttemptsByNode = cloneIntMap(j.AttemptsByNode)
	cp.LastErrorByNode = cloneMap(j.LastErrorByNode)
	return &cp
}

func (s *Store) SetWatermark(ts time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.LastSeenAnnouncedAt = ts.UTC().Format(time.RFC3339)
	return s.persistLocked()
}

func (s *Store) Watermark() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state.LastSeenAnnouncedAt == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, s.state.LastSeenAnnouncedAt)
	if err != nil {
		return time.Time{}
	}
	return t
}

func (s *Store) load() error {
	if _, err := os.Stat(s.path); os.IsNotExist(err) {
		return nil
	}
	b, err := os.ReadFile(s.path)
	if err != nil {
		return err
	}
	if len(b) == 0 {
		return nil
	}
	var parsed State
	if err := json.Unmarshal(b, &parsed); err != nil {
		return err
	}
	if parsed.Jobs == nil {
		parsed.Jobs = make(map[string]*JobState)
	}
	s.state = parsed
	return nil
}

func (s *Store) persistLocked() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s.state, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, data, 0644)
}

func cloneMap(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func cloneIntMap(in map[string]int) map[string]int {
	out := make(map[string]int, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
