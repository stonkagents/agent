// Package repository: Postgres community board phase 1 repositories: board reputation
// (peer_reputation), reports (board_reports), thread watches (board_watches) and token offer
// payments (token_offer_payments). Migration 024.
package repository

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stonkagents/agent/tracker/internal/models"
)

// --- BoardReputationRepository ---

// PostgresBoardReputationRepository implements BoardReputationRepository on peer_reputation.
type PostgresBoardReputationRepository struct{ pool *pgxpool.Pool }

// NewPostgresBoardReputationRepository creates the repository.
func NewPostgresBoardReputationRepository(pool *pgxpool.Pool) *PostgresBoardReputationRepository {
	return &PostgresBoardReputationRepository{pool: pool}
}

const peerReputationCols = `peer_id, score, tier, bounties_won, credits_won, answers_accepted, upvotes_received, first_replies_1h, reports_upheld, computed_at`

// Upsert replaces the peer's row.
func (r *PostgresBoardReputationRepository) Upsert(ctx context.Context, rep *models.PeerReputation) error {
	_, err := r.pool.Exec(ctx, `INSERT INTO peer_reputation (`+peerReputationCols+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		ON CONFLICT (peer_id) DO UPDATE SET score = EXCLUDED.score, tier = EXCLUDED.tier,
		  bounties_won = EXCLUDED.bounties_won, credits_won = EXCLUDED.credits_won,
		  answers_accepted = EXCLUDED.answers_accepted, upvotes_received = EXCLUDED.upvotes_received,
		  first_replies_1h = EXCLUDED.first_replies_1h, reports_upheld = EXCLUDED.reports_upheld,
		  computed_at = EXCLUDED.computed_at`,
		rep.PeerID, rep.Score, rep.Tier, rep.BountiesWon, rep.CreditsWon, rep.AnswersAccepted,
		rep.UpvotesReceived, rep.FirstReplies1h, rep.ReportsUpheld, rep.ComputedAt)
	return err
}

func scanPeerReputation(scan func(dest ...interface{}) error) (*models.PeerReputation, error) {
	var rep models.PeerReputation
	err := scan(&rep.PeerID, &rep.Score, &rep.Tier, &rep.BountiesWon, &rep.CreditsWon, &rep.AnswersAccepted,
		&rep.UpvotesReceived, &rep.FirstReplies1h, &rep.ReportsUpheld, &rep.ComputedAt)
	if err != nil {
		return nil, err
	}
	return &rep, nil
}

// Get returns the peer's row, or models.ErrNotFound.
func (r *PostgresBoardReputationRepository) Get(ctx context.Context, peerID string) (*models.PeerReputation, error) {
	rep, err := scanPeerReputation(r.pool.QueryRow(ctx, `SELECT `+peerReputationCols+` FROM peer_reputation WHERE peer_id = $1`, peerID).Scan)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, models.ErrNotFound
		}
		return nil, err
	}
	return rep, nil
}

// GetByIDs returns the rows that exist among peerIDs in one query.
func (r *PostgresBoardReputationRepository) GetByIDs(ctx context.Context, peerIDs []string) (map[string]*models.PeerReputation, error) {
	out := make(map[string]*models.PeerReputation, len(peerIDs))
	if len(peerIDs) == 0 {
		return out, nil
	}
	rows, err := r.pool.Query(ctx, `SELECT `+peerReputationCols+` FROM peer_reputation WHERE peer_id = ANY($1)`, peerIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		rep, err := scanPeerReputation(rows.Scan)
		if err != nil {
			return nil, err
		}
		out[rep.PeerID] = rep
	}
	return out, rows.Err()
}

// --- BoardReportRepository ---

// PostgresBoardReportRepository implements BoardReportRepository on board_reports.
type PostgresBoardReportRepository struct{ pool *pgxpool.Pool }

// NewPostgresBoardReportRepository creates the repository.
func NewPostgresBoardReportRepository(pool *pgxpool.Pool) *PostgresBoardReportRepository {
	return &PostgresBoardReportRepository{pool: pool}
}

const boardReportCols = `id, target_type, target_id, target_author_peer_id, reporter_peer_id, reason, note, status, created_at, resolved_at, resolution_note`

// Create inserts a report; models.ErrAlreadyExists when the reporter already reported the target.
func (r *PostgresBoardReportRepository) Create(ctx context.Context, report *models.BoardReport) error {
	if report.ID == "" {
		report.ID = uuid.New().String()
	}
	if report.Status == "" {
		report.Status = models.ReportStatusOpen
	}
	_, err := r.pool.Exec(ctx, `INSERT INTO board_reports (`+boardReportCols+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
		report.ID, report.TargetType, report.TargetID, report.TargetAuthorPeerID, report.ReporterPeerID,
		report.Reason, report.Note, report.Status, report.CreatedAt, report.ResolvedAt, report.ResolutionNote)
	if isUniqueViolation(err) {
		return models.ErrAlreadyExists
	}
	return err
}

func scanBoardReport(scan func(dest ...interface{}) error) (*models.BoardReport, error) {
	var rep models.BoardReport
	err := scan(&rep.ID, &rep.TargetType, &rep.TargetID, &rep.TargetAuthorPeerID, &rep.ReporterPeerID,
		&rep.Reason, &rep.Note, &rep.Status, &rep.CreatedAt, &rep.ResolvedAt, &rep.ResolutionNote)
	if err != nil {
		return nil, err
	}
	return &rep, nil
}

// Get returns one report, or models.ErrNotFound.
func (r *PostgresBoardReportRepository) Get(ctx context.Context, id string) (*models.BoardReport, error) {
	rep, err := scanBoardReport(r.pool.QueryRow(ctx, `SELECT `+boardReportCols+` FROM board_reports WHERE id = $1`, id).Scan)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, models.ErrNotFound
		}
		return nil, err
	}
	return rep, nil
}

// List returns reports with the given status ("" = all), newest first, at most limit.
func (r *PostgresBoardReportRepository) List(ctx context.Context, status string, limit int) ([]*models.BoardReport, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := r.pool.Query(ctx, `SELECT `+boardReportCols+` FROM board_reports
		WHERE ($1 = '' OR status = $1) ORDER BY created_at DESC, id LIMIT $2`, status, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*models.BoardReport{}
	for rows.Next() {
		rep, err := scanBoardReport(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, rep)
	}
	return out, rows.Err()
}

// ReporterPeerIDs returns the distinct reporters of a target whose report was not dismissed.
func (r *PostgresBoardReportRepository) ReporterPeerIDs(ctx context.Context, targetType, targetID string) ([]string, error) {
	rows, err := r.pool.Query(ctx, `SELECT DISTINCT reporter_peer_id FROM board_reports
		WHERE target_type = $1 AND target_id = $2 AND status <> 'dismissed' ORDER BY 1`, targetType, targetID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// SetStatus resolves a report by a platform decision (the resolution note is cleared, so an
// auto-upheld report a platform peer confirms counts like any explicit uphold).
func (r *PostgresBoardReportRepository) SetStatus(ctx context.Context, id, status string, at time.Time) error {
	cmd, err := r.pool.Exec(ctx, `UPDATE board_reports SET status = $2, resolved_at = $3, resolution_note = '' WHERE id = $1`, id, status, at)
	if err != nil {
		return err
	}
	if cmd.RowsAffected() == 0 {
		return models.ErrNotFound
	}
	return nil
}

// ResolveOpenForTarget resolves every open report on the target (auto hide).
func (r *PostgresBoardReportRepository) ResolveOpenForTarget(ctx context.Context, targetType, targetID, status, note string, at time.Time) (int, error) {
	cmd, err := r.pool.Exec(ctx, `UPDATE board_reports SET status = $3, resolved_at = $4, resolution_note = $5
		WHERE target_type = $1 AND target_id = $2 AND status = 'open'`, targetType, targetID, status, at, note)
	if err != nil {
		return 0, err
	}
	return int(cmd.RowsAffected()), nil
}

// CountUpheldAgainst counts the reports a platform peer upheld against content by
// authorPeerID; reports upheld by auto hide (resolution_note set) do not count.
func (r *PostgresBoardReportRepository) CountUpheldAgainst(ctx context.Context, authorPeerID string) (int, error) {
	var n int
	err := r.pool.QueryRow(ctx, `SELECT COUNT(*) FROM board_reports WHERE status = 'upheld' AND resolution_note = '' AND target_author_peer_id = $1`, authorPeerID).Scan(&n)
	return n, err
}

// ReportedTargetIDs returns the subset of targetIDs with an open or upheld report.
func (r *PostgresBoardReportRepository) ReportedTargetIDs(ctx context.Context, targetType string, targetIDs []string) (map[string]bool, error) {
	out := map[string]bool{}
	if len(targetIDs) == 0 {
		return out, nil
	}
	rows, err := r.pool.Query(ctx, `SELECT DISTINCT target_id FROM board_reports
		WHERE target_type = $1 AND target_id = ANY($2) AND status <> 'dismissed'`, targetType, targetIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// --- BoardWatchRepository ---

// PostgresBoardWatchRepository implements BoardWatchRepository on board_watches.
type PostgresBoardWatchRepository struct{ pool *pgxpool.Pool }

// NewPostgresBoardWatchRepository creates the repository.
func NewPostgresBoardWatchRepository(pool *pgxpool.Pool) *PostgresBoardWatchRepository {
	return &PostgresBoardWatchRepository{pool: pool}
}

// Set records an explicit watch or unwatch.
func (r *PostgresBoardWatchRepository) Set(ctx context.Context, postID, peerID string, watching bool, at time.Time) error {
	_, err := r.pool.Exec(ctx, `INSERT INTO board_watches (post_id, peer_id, watching, created_at) VALUES ($1, $2, $3, $4)
		ON CONFLICT (post_id, peer_id) DO UPDATE SET watching = EXCLUDED.watching`, postID, peerID, watching, at)
	return err
}

// AddIfAbsent records an automatic watch unless the peer already has a row.
func (r *PostgresBoardWatchRepository) AddIfAbsent(ctx context.Context, postID, peerID string, at time.Time) error {
	_, err := r.pool.Exec(ctx, `INSERT INTO board_watches (post_id, peer_id, watching, created_at) VALUES ($1, $2, TRUE, $3)
		ON CONFLICT (post_id, peer_id) DO NOTHING`, postID, peerID, at)
	return err
}

// IsWatching reports whether peerID watches postID.
func (r *PostgresBoardWatchRepository) IsWatching(ctx context.Context, postID, peerID string) (bool, error) {
	var watching bool
	err := r.pool.QueryRow(ctx, `SELECT watching FROM board_watches WHERE post_id = $1 AND peer_id = $2`, postID, peerID).Scan(&watching)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return watching, err
}

// Watchers returns the peers watching postID.
func (r *PostgresBoardWatchRepository) Watchers(ctx context.Context, postID string) ([]string, error) {
	rows, err := r.pool.Query(ctx, `SELECT peer_id FROM board_watches WHERE post_id = $1 AND watching ORDER BY peer_id`, postID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// WatchingByPostIDs returns the subset of postIDs that peerID watches.
func (r *PostgresBoardWatchRepository) WatchingByPostIDs(ctx context.Context, peerID string, postIDs []string) (map[string]bool, error) {
	out := make(map[string]bool, len(postIDs))
	if len(postIDs) == 0 || peerID == "" {
		return out, nil
	}
	rows, err := r.pool.Query(ctx, `SELECT post_id FROM board_watches WHERE peer_id = $1 AND watching AND post_id = ANY($2)`, peerID, postIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// --- TokenOfferPaymentRepository ---

// PostgresTokenOfferPaymentRepository implements TokenOfferPaymentRepository on token_offer_payments.
type PostgresTokenOfferPaymentRepository struct{ pool *pgxpool.Pool }

// NewPostgresTokenOfferPaymentRepository creates the repository.
func NewPostgresTokenOfferPaymentRepository(pool *pgxpool.Pool) *PostgresTokenOfferPaymentRepository {
	return &PostgresTokenOfferPaymentRepository{pool: pool}
}

// Create inserts a payment; models.ErrAlreadyExists when the signature or reply was already paid.
func (r *PostgresTokenOfferPaymentRepository) Create(ctx context.Context, p *models.TokenOfferPayment) error {
	_, err := r.pool.Exec(ctx, `INSERT INTO token_offer_payments (signature, post_id, reply_id, from_wallet, to_wallet, amount_raw, verified_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`, p.Signature, p.PostID, p.ReplyID, p.FromWallet, p.ToWallet, p.AmountRaw, p.VerifiedAt)
	if isUniqueViolation(err) {
		return models.ErrAlreadyExists
	}
	return err
}

// HasSignature reports whether the signature was already used.
func (r *PostgresTokenOfferPaymentRepository) HasSignature(ctx context.Context, signature string) (bool, error) {
	var n int
	err := r.pool.QueryRow(ctx, `SELECT COUNT(*) FROM token_offer_payments WHERE signature = $1`, signature).Scan(&n)
	return n > 0, err
}

// PaidReplyIDs returns the set of paid reply ids of postID.
func (r *PostgresTokenOfferPaymentRepository) PaidReplyIDs(ctx context.Context, postID string) (map[string]bool, error) {
	rows, err := r.pool.Query(ctx, `SELECT reply_id FROM token_offer_payments WHERE post_id = $1`, postID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}
