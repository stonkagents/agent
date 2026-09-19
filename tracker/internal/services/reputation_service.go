// Package services: Community board reputation (phase 1).
// Purpose: The one place the board reputation formula and tiers live. A peer's score is
//          computed from counters read straight off the forum tables (bounties won, accepted
//          answers, upvotes, first replies, upheld reports), stored in peer_reputation, refreshed
//          for one peer when an event moves it and for everyone by the nightly job.

package services

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/stonkagents/agent/tracker/internal/clock"
	"github.com/stonkagents/agent/tracker/internal/models"
	"github.com/stonkagents/agent/tracker/internal/repository"
)

// Reputation formula weights and tier thresholds (docs section 15).
const (
	ReputationWeightBountyWon      = 10
	ReputationCreditsWonDivisor    = 10
	ReputationWeightAnswerAccepted = 15
	ReputationWeightUpvote         = 2
	ReputationWeightFirstReply1h   = 5
	ReputationWeightReportUpheld   = 25

	ReputationTierActiveMin  = 20
	ReputationTierTrustedMin = 100
	ReputationTierTopMin     = 500

	// FirstReplyWindow is how soon after a post a first reply counts toward reputation.
	FirstReplyWindow = time.Hour

	// FirstAcceptedAnswerCredits is the one-time free credit grant for a peer's first accepted
	// answer, with FirstAcceptedAnswerReason as the transaction reason.
	FirstAcceptedAnswerCredits = 25
	FirstAcceptedAnswerReason  = "first_accepted_answer"
	// FirstAcceptedAnswerExpiry is the free expiry window of that grant.
	FirstAcceptedAnswerExpiry = 30 * 24 * time.Hour
	// ReputationNightlyHourUTC is when the full recompute runs.
	ReputationNightlyHourUTC = 2
)

// ReputationInputs are the counters the score is computed from.
type ReputationInputs struct {
	BountiesWon     int
	CreditsWon      int
	AnswersAccepted int
	UpvotesReceived int
	FirstReplies1h  int
	ReportsUpheld   int
}

// ComputeReputationScore applies the formula:
//
//	score = 10 * bounties_won + credits_won / 10 + 15 * answers_accepted + 2 * upvotes_received
//	        + 5 * first_reply_within_1h_count - 25 * reports_upheld, floored at 0.
func ComputeReputationScore(in ReputationInputs) int {
	score := ReputationWeightBountyWon*in.BountiesWon +
		in.CreditsWon/ReputationCreditsWonDivisor +
		ReputationWeightAnswerAccepted*in.AnswersAccepted +
		ReputationWeightUpvote*in.UpvotesReceived +
		ReputationWeightFirstReply1h*in.FirstReplies1h -
		ReputationWeightReportUpheld*in.ReportsUpheld
	if score < 0 {
		return 0
	}
	return score
}

// ReputationTier maps a score to its tier: new (< 20), active (20 to 99), trusted (100 to 499),
// top (>= 500).
func ReputationTier(score int) string {
	switch {
	case score >= ReputationTierTopMin:
		return models.ReputationTierTop
	case score >= ReputationTierTrustedMin:
		return models.ReputationTierTrusted
	case score >= ReputationTierActiveMin:
		return models.ReputationTierActive
	default:
		return models.ReputationTierNew
	}
}

// reputationTierRank orders the tiers so "tier >= active" is a comparison.
var reputationTierRank = map[string]int{
	models.ReputationTierNew:     0,
	models.ReputationTierActive:  1,
	models.ReputationTierTrusted: 2,
	models.ReputationTierTop:     3,
}

// ReputationTierAtLeast reports whether tier is min or higher (unknown tiers count as new).
func ReputationTierAtLeast(tier, min string) bool {
	return reputationTierRank[tier] >= reputationTierRank[min]
}

// ReputationService computes and serves the board reputation.
type ReputationService struct {
	repo    repository.BoardReputationRepository
	forum   repository.ForumRepository
	reports repository.BoardReportRepository // nil = reports_upheld stays 0
	clock   clock.Clock
	// MinVoterAge is how old a peer's row must be for its upvotes and accepts to count
	// (round 2 sock puppet guard; 0 = every event counts, the constructor's default). The
	// tracker sets it from REPUTATION_MIN_VOTER_AGE_DAYS (default DefaultReputationMinVoterAge).
	MinVoterAge time.Duration
}

// NewReputationService creates a ReputationService. reports may be nil.
func NewReputationService(repo repository.BoardReputationRepository, forum repository.ForumRepository, reports repository.BoardReportRepository, clk clock.Clock) *ReputationService {
	if clk == nil {
		clk = clock.RealClock{}
	}
	return &ReputationService{repo: repo, forum: forum, reports: reports, clock: clk}
}

// statsGuard is the BoardStatsGuard for the current MinVoterAge (zero when off).
func (s *ReputationService) statsGuard() repository.BoardStatsGuard {
	if s.MinVoterAge <= 0 {
		return repository.BoardStatsGuard{}
	}
	return repository.BoardStatsGuard{VoterSince: s.clock.Now().Add(-s.MinVoterAge)}
}

// Recompute reads the peer's counters, applies the formula and stores the row.
func (s *ReputationService) Recompute(ctx context.Context, peerID string) (*models.PeerReputation, error) {
	if peerID == "" {
		return nil, models.ErrInvalidInput
	}
	stats, err := s.forum.BoardStats(ctx, peerID, s.statsGuard())
	if err != nil {
		return nil, fmt.Errorf("reputation: board stats for %s: %w", peerID, err)
	}
	upheld := 0
	if s.reports != nil {
		if upheld, err = s.reports.CountUpheldAgainst(ctx, peerID); err != nil {
			return nil, fmt.Errorf("reputation: upheld reports for %s: %w", peerID, err)
		}
	}
	in := ReputationInputs{
		BountiesWon: stats.BountiesWon, CreditsWon: stats.CreditsWon, AnswersAccepted: stats.AnswersAccepted,
		UpvotesReceived: stats.UpvotesReceived, FirstReplies1h: stats.FirstReplies1h, ReportsUpheld: upheld,
	}
	score := ComputeReputationScore(in)
	rep := &models.PeerReputation{
		PeerID: peerID, Score: score, Tier: ReputationTier(score),
		BountiesWon: in.BountiesWon, CreditsWon: in.CreditsWon, AnswersAccepted: in.AnswersAccepted,
		UpvotesReceived: in.UpvotesReceived, FirstReplies1h: in.FirstReplies1h, ReportsUpheld: in.ReportsUpheld,
		ComputedAt: s.clock.Now().UTC(),
	}
	if err := s.repo.Upsert(ctx, rep); err != nil {
		return nil, fmt.Errorf("reputation: store %s: %w", peerID, err)
	}
	return rep, nil
}

// RecomputeAll recomputes every peer that has posted or replied (the nightly source of
// truth). Returns how many rows were written; the first failure is reported after the rest ran.
func (s *ReputationService) RecomputeAll(ctx context.Context) (int, error) {
	ids, err := s.forum.BoardPeerIDs(ctx)
	if err != nil {
		return 0, fmt.Errorf("reputation: list peers: %w", err)
	}
	n := 0
	var firstErr error
	for _, id := range ids {
		if _, err := s.Recompute(ctx, id); err != nil {
			firstErr = errors.Join(firstErr, err)
			continue
		}
		n++
	}
	return n, firstErr
}

// Get returns the stored reputation, or a zero-score "new" record for a peer without a row.
func (s *ReputationService) Get(ctx context.Context, peerID string) (*models.PeerReputation, error) {
	rep, err := s.repo.Get(ctx, peerID)
	if errors.Is(err, models.ErrNotFound) {
		return &models.PeerReputation{PeerID: peerID, Tier: models.ReputationTierNew}, nil
	}
	if err != nil {
		return nil, err
	}
	return rep, nil
}

// ByIDs returns the stored rows among peerIDs (one query; peers without a row are absent).
func (s *ReputationService) ByIDs(ctx context.Context, peerIDs []string) (map[string]*models.PeerReputation, error) {
	unique := make([]string, 0, len(peerIDs))
	seen := make(map[string]struct{}, len(peerIDs))
	for _, id := range peerIDs {
		if id == "" {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		unique = append(unique, id)
	}
	if len(unique) == 0 {
		return map[string]*models.PeerReputation{}, nil
	}
	return s.repo.GetByIDs(ctx, unique)
}

// TiersByIDs returns peer id -> tier for peerIDs; peers without a row are "new". A lookup
// error degrades to all "new" (the payload still renders).
func (s *ReputationService) TiersByIDs(ctx context.Context, peerIDs []string) map[string]string {
	out := make(map[string]string, len(peerIDs))
	reps, err := s.ByIDs(ctx, peerIDs)
	for _, id := range peerIDs {
		if id == "" {
			continue
		}
		tier := models.ReputationTierNew
		if err == nil {
			if rep, ok := reps[id]; ok && rep.Tier != "" {
				tier = rep.Tier
			}
		}
		out[id] = tier
	}
	return out
}

// NextNightlyRun returns the next ReputationNightlyHourUTC after now.
func NextNightlyRun(now time.Time) time.Time {
	utc := now.UTC()
	next := time.Date(utc.Year(), utc.Month(), utc.Day(), ReputationNightlyHourUTC, 0, 0, 0, time.UTC)
	if !next.After(utc) {
		next = next.Add(24 * time.Hour)
	}
	return next
}
