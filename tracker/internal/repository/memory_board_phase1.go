// Package repository: In-memory community board phase 1 repositories: board reputation,
// reports, thread watches and token offer payments (tests and local runs).
package repository

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/stonkagents/agent/tracker/internal/models"
)

// --- BoardReputationRepository ---

// MemoryBoardReputationRepository implements BoardReputationRepository in memory.
type MemoryBoardReputationRepository struct {
	mu   sync.RWMutex
	rows map[string]*models.PeerReputation
}

// NewMemoryBoardReputationRepository creates an empty repository.
func NewMemoryBoardReputationRepository() *MemoryBoardReputationRepository {
	return &MemoryBoardReputationRepository{rows: map[string]*models.PeerReputation{}}
}

// Upsert replaces the peer's row.
func (r *MemoryBoardReputationRepository) Upsert(ctx context.Context, rep *models.PeerReputation) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := *rep
	r.rows[rep.PeerID] = &cp
	return nil
}

// Get returns the peer's row, or models.ErrNotFound.
func (r *MemoryBoardReputationRepository) Get(ctx context.Context, peerID string) (*models.PeerReputation, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	rep, ok := r.rows[peerID]
	if !ok {
		return nil, models.ErrNotFound
	}
	cp := *rep
	return &cp, nil
}

// GetByIDs returns the rows that exist among peerIDs.
func (r *MemoryBoardReputationRepository) GetByIDs(ctx context.Context, peerIDs []string) (map[string]*models.PeerReputation, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[string]*models.PeerReputation, len(peerIDs))
	for _, id := range peerIDs {
		if rep, ok := r.rows[id]; ok {
			cp := *rep
			out[id] = &cp
		}
	}
	return out, nil
}

// --- BoardReportRepository ---

// MemoryBoardReportRepository implements BoardReportRepository in memory.
type MemoryBoardReportRepository struct {
	mu      sync.RWMutex
	reports map[string]*models.BoardReport
}

// NewMemoryBoardReportRepository creates an empty repository.
func NewMemoryBoardReportRepository() *MemoryBoardReportRepository {
	return &MemoryBoardReportRepository{reports: map[string]*models.BoardReport{}}
}

// Create inserts a report; models.ErrAlreadyExists when the reporter already reported the target.
func (r *MemoryBoardReportRepository) Create(ctx context.Context, report *models.BoardReport) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, existing := range r.reports {
		if existing.TargetType == report.TargetType && existing.TargetID == report.TargetID && existing.ReporterPeerID == report.ReporterPeerID {
			return models.ErrAlreadyExists
		}
	}
	if report.ID == "" {
		report.ID = uuid.New().String()
	}
	if report.Status == "" {
		report.Status = models.ReportStatusOpen
	}
	cp := *report
	r.reports[report.ID] = &cp
	return nil
}

// Get returns one report, or models.ErrNotFound.
func (r *MemoryBoardReportRepository) Get(ctx context.Context, id string) (*models.BoardReport, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	rep, ok := r.reports[id]
	if !ok {
		return nil, models.ErrNotFound
	}
	cp := *rep
	return &cp, nil
}

// List returns reports with the given status ("" = all), newest first, at most limit.
func (r *MemoryBoardReportRepository) List(ctx context.Context, status string, limit int) ([]*models.BoardReport, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*models.BoardReport, 0, len(r.reports))
	for _, rep := range r.reports {
		if status != "" && rep.Status != status {
			continue
		}
		cp := *rep
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.After(out[j].CreatedAt)
		}
		return out[i].ID < out[j].ID
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// ReporterPeerIDs returns the distinct reporters of a target whose report was not dismissed.
func (r *MemoryBoardReportRepository) ReporterPeerIDs(ctx context.Context, targetType, targetID string) ([]string, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	seen := map[string]struct{}{}
	var out []string
	for _, rep := range r.reports {
		if rep.TargetType != targetType || rep.TargetID != targetID || rep.Status == models.ReportStatusDismissed {
			continue
		}
		if _, dup := seen[rep.ReporterPeerID]; dup {
			continue
		}
		seen[rep.ReporterPeerID] = struct{}{}
		out = append(out, rep.ReporterPeerID)
	}
	sort.Strings(out)
	return out, nil
}

// SetStatus resolves a report by a platform decision (clears the resolution note).
func (r *MemoryBoardReportRepository) SetStatus(ctx context.Context, id, status string, at time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	rep, ok := r.reports[id]
	if !ok {
		return models.ErrNotFound
	}
	rep.Status = status
	rep.ResolvedAt = &at
	rep.ResolutionNote = ""
	return nil
}

// ResolveOpenForTarget resolves every open report on the target (auto hide).
func (r *MemoryBoardReportRepository) ResolveOpenForTarget(ctx context.Context, targetType, targetID, status, note string, at time.Time) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, rep := range r.reports {
		if rep.TargetType != targetType || rep.TargetID != targetID || rep.Status != models.ReportStatusOpen {
			continue
		}
		resolved := at
		rep.Status = status
		rep.ResolvedAt = &resolved
		rep.ResolutionNote = note
		n++
	}
	return n, nil
}

// CountUpheldAgainst counts the reports a platform peer upheld against content by
// authorPeerID; reports upheld by auto hide (resolution_note set) do not count.
func (r *MemoryBoardReportRepository) CountUpheldAgainst(ctx context.Context, authorPeerID string) (int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	n := 0
	for _, rep := range r.reports {
		if rep.Status == models.ReportStatusUpheld && rep.ResolutionNote == "" && rep.TargetAuthorPeerID == authorPeerID {
			n++
		}
	}
	return n, nil
}

// ReportedTargetIDs returns the subset of targetIDs with an open or upheld report.
func (r *MemoryBoardReportRepository) ReportedTargetIDs(ctx context.Context, targetType string, targetIDs []string) (map[string]bool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	want := make(map[string]bool, len(targetIDs))
	for _, id := range targetIDs {
		want[id] = true
	}
	out := map[string]bool{}
	for _, rep := range r.reports {
		if rep.TargetType == targetType && want[rep.TargetID] && rep.Status != models.ReportStatusDismissed {
			out[rep.TargetID] = true
		}
	}
	return out, nil
}

// --- BoardWatchRepository ---

// MemoryBoardWatchRepository implements BoardWatchRepository in memory.
type MemoryBoardWatchRepository struct {
	mu      sync.RWMutex
	watches map[string]map[string]bool // postID -> peerID -> watching
}

// NewMemoryBoardWatchRepository creates an empty repository.
func NewMemoryBoardWatchRepository() *MemoryBoardWatchRepository {
	return &MemoryBoardWatchRepository{watches: map[string]map[string]bool{}}
}

// Set records an explicit watch or unwatch.
func (r *MemoryBoardWatchRepository) Set(ctx context.Context, postID, peerID string, watching bool, at time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.watches[postID] == nil {
		r.watches[postID] = map[string]bool{}
	}
	r.watches[postID][peerID] = watching
	return nil
}

// AddIfAbsent records an automatic watch unless the peer already has a row.
func (r *MemoryBoardWatchRepository) AddIfAbsent(ctx context.Context, postID, peerID string, at time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.watches[postID] == nil {
		r.watches[postID] = map[string]bool{}
	}
	if _, exists := r.watches[postID][peerID]; !exists {
		r.watches[postID][peerID] = true
	}
	return nil
}

// IsWatching reports whether peerID watches postID.
func (r *MemoryBoardWatchRepository) IsWatching(ctx context.Context, postID, peerID string) (bool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.watches[postID][peerID], nil
}

// Watchers returns the peers watching postID, sorted.
func (r *MemoryBoardWatchRepository) Watchers(ctx context.Context, postID string) ([]string, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []string
	for peerID, watching := range r.watches[postID] {
		if watching {
			out = append(out, peerID)
		}
	}
	sort.Strings(out)
	return out, nil
}

// WatchingByPostIDs returns the subset of postIDs that peerID watches.
func (r *MemoryBoardWatchRepository) WatchingByPostIDs(ctx context.Context, peerID string, postIDs []string) (map[string]bool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[string]bool, len(postIDs))
	for _, id := range postIDs {
		if r.watches[id][peerID] {
			out[id] = true
		}
	}
	return out, nil
}

// --- TokenOfferPaymentRepository ---

// MemoryTokenOfferPaymentRepository implements TokenOfferPaymentRepository in memory.
type MemoryTokenOfferPaymentRepository struct {
	mu      sync.RWMutex
	bySig   map[string]*models.TokenOfferPayment
	byReply map[string]string // replyID -> signature
}

// NewMemoryTokenOfferPaymentRepository creates an empty repository.
func NewMemoryTokenOfferPaymentRepository() *MemoryTokenOfferPaymentRepository {
	return &MemoryTokenOfferPaymentRepository{bySig: map[string]*models.TokenOfferPayment{}, byReply: map[string]string{}}
}

// Create inserts a payment; models.ErrAlreadyExists when the signature or reply was already paid.
func (r *MemoryTokenOfferPaymentRepository) Create(ctx context.Context, payment *models.TokenOfferPayment) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.bySig[payment.Signature]; dup {
		return models.ErrAlreadyExists
	}
	if _, dup := r.byReply[payment.ReplyID]; dup {
		return models.ErrAlreadyExists
	}
	cp := *payment
	r.bySig[payment.Signature] = &cp
	r.byReply[payment.ReplyID] = payment.Signature
	return nil
}

// HasSignature reports whether the signature was already used.
func (r *MemoryTokenOfferPaymentRepository) HasSignature(ctx context.Context, signature string) (bool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.bySig[signature]
	return ok, nil
}

// PaidReplyIDs returns the set of paid reply ids of postID.
func (r *MemoryTokenOfferPaymentRepository) PaidReplyIDs(ctx context.Context, postID string) (map[string]bool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := map[string]bool{}
	for _, p := range r.bySig {
		if p.PostID == postID {
			out[p.ReplyID] = true
		}
	}
	return out, nil
}
