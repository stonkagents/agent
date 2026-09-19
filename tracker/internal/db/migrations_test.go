package db

import (
	"io/fs"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

var migrationName = regexp.MustCompile(`^(\d{3})_([a-z0-9_]+)\.(up|down)\.sql$`)

// readMigrations returns the embedded migration files keyed by name.
func readMigrations(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		t.Fatalf("read embedded migrations: %v", err)
	}
	for _, e := range entries {
		b, err := fs.ReadFile(migrationsFS, "migrations/"+e.Name())
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		out[e.Name()] = string(b)
	}
	return out
}

// TestMigrations_PairedAndContiguous guards the embedded set golang-migrate applies at tracker
// startup (RunMigrations): every version has an up and a down, and versions have no gaps.
func TestMigrations_PairedAndContiguous(t *testing.T) {
	files := readMigrations(t)
	versions := map[int]map[string]string{} // version -> direction -> stem
	for name := range files {
		m := migrationName.FindStringSubmatch(name)
		if m == nil {
			t.Errorf("%s does not follow NNN_name.up|down.sql", name)
			continue
		}
		v, _ := strconv.Atoi(m[1])
		if versions[v] == nil {
			versions[v] = map[string]string{}
		}
		if prev, dup := versions[v][m[3]]; dup {
			t.Errorf("version %d has two %s files: %s and %s", v, m[3], prev, m[2])
		}
		versions[v][m[3]] = m[2]
	}
	nums := make([]int, 0, len(versions))
	for v := range versions {
		nums = append(nums, v)
	}
	sort.Ints(nums)
	for i, v := range nums {
		if v != i+1 {
			t.Errorf("migration versions are not contiguous at %d (got %v)", v, nums)
			break
		}
		if versions[v]["up"] == "" || versions[v]["down"] == "" || versions[v]["up"] != versions[v]["down"] {
			t.Errorf("version %d must have matching up and down files, got %v", v, versions[v])
		}
	}
	if len(nums) < 19 {
		t.Errorf("expected at least 19 migration versions, got %d", len(nums))
	}
}

// TestMigration019_LaunchImageThumb pins the nullable thumbnail column the launch
// repositories read and write, and that down removes it.
func TestMigration019_LaunchImageThumb(t *testing.T) {
	files := readMigrations(t)
	up, down := files["019_launch_image_thumb.up.sql"], files["019_launch_image_thumb.down.sql"]
	if up == "" || down == "" {
		t.Fatal("migration 019 up/down missing")
	}
	if !strings.Contains(up, "ALTER TABLE token_launches ADD COLUMN IF NOT EXISTS image_thumb_url TEXT") {
		t.Errorf("019 up must add a nullable image_thumb_url column:\n%s", up)
	}
	if strings.Contains(up, "NOT NULL") {
		t.Errorf("019 up must keep image_thumb_url nullable for launches recorded before thumbnails:\n%s", up)
	}
	if !strings.Contains(down, "ALTER TABLE token_launches DROP COLUMN IF EXISTS image_thumb_url") {
		t.Errorf("019 down must drop image_thumb_url:\n%s", down)
	}
}

// TestMigration015_DevnetSTONKStandIn pins the devnet $STONK stand-in row (Raydium devnet USDC
// under the on-chain GlobalConfig 4wHb…) and that the SOL row is demoted, not deleted or disabled.
func TestMigration015_DevnetSTONKStandIn(t *testing.T) {
	files := readMigrations(t)
	up, down := files["015_devnet_stonk_standin_quote.up.sql"], files["015_devnet_stonk_standin_quote.down.sql"]
	if up == "" || down == "" {
		t.Fatal("migration 015 up/down missing")
	}
	const mint, config = "USDCoctVLVnvTXBEuP9s8hntucdJokbo17RwHuNXemT", "4wHbNkobu7iARU9MbCEqDSAq6JuQreGupG2Jsf2R3DFP"
	row := regexp.MustCompile(`\('devnet',\s*'` + mint + `',\s*'STONK',\s*'\$STONK \(devnet stand-in\)',\s*6,\s*'TokenkegQfeZyiNwAJbNbGKPFXCWuBvf9Ss623VQ5DA',\s*'stonk',\s*'` + config + `',\s*1,\s*TRUE,\s*0\)`)
	if !row.MatchString(up) {
		t.Errorf("015 up does not insert the expected stand-in row:\n%s", up)
	}
	if !strings.Contains(up, "ON CONFLICT (cluster, quote_mint) DO NOTHING") {
		t.Errorf("015 up must be idempotent on (cluster, quote_mint)")
	}
	if !strings.Contains(up, "SET sort_order = 1") || !strings.Contains(up, "cluster = 'devnet'") {
		t.Errorf("015 up must move the devnet SOL row to sort_order 1")
	}
	if strings.Contains(strings.ToLower(up), "enabled = false") || strings.Contains(up, "DELETE") || strings.Contains(up, "mainnet") && strings.Contains(up, "UPDATE launch_quotes SET") && !strings.Contains(up, "cluster = 'devnet'") {
		t.Errorf("015 up must not disable rows or touch mainnet")
	}
	if !strings.Contains(down, "DELETE FROM launch_quotes WHERE cluster = 'devnet' AND quote_mint = '"+mint+"'") {
		t.Errorf("015 down must delete the stand-in row:\n%s", down)
	}
	if !strings.Contains(down, "SET sort_order = 0") {
		t.Errorf("015 down must restore the devnet SOL sort_order")
	}
}

// TestMigration016_Feedback pins the feedback table shape the repository writes and reads.
func TestMigration016_Feedback(t *testing.T) {
	files := readMigrations(t)
	up, down := files["016_feedback.up.sql"], files["016_feedback.down.sql"]
	if up == "" || down == "" {
		t.Fatal("migration 016 up/down missing")
	}
	for _, col := range []string{"id ", "kind ", "message ", "path ", "contact ", "contact_via ", "wallet_address ", "user_agent ", "ip_hash ", "created_at "} {
		if !strings.Contains(up, col) {
			t.Errorf("016 up is missing column %q", strings.TrimSpace(col))
		}
	}
	if !strings.Contains(up, "CREATE TABLE IF NOT EXISTS feedback") || !strings.Contains(up, "kind IN ('bug', 'idea', 'other', 'wanted')") {
		t.Errorf("016 up must create feedback with the kind check:\n%s", up)
	}
	if !strings.Contains(down, "DROP TABLE IF EXISTS feedback") {
		t.Errorf("016 down must drop feedback:\n%s", down)
	}
}

// TestMigration017_AgentInterest pins the agent_interest table shape the repository writes and reads:
// the capability allow-list, the priority check, the 600-char description cap and the GIN index
// the per-capability counts lean on.
func TestMigration017_AgentInterest(t *testing.T) {
	files := readMigrations(t)
	up, down := files["017_agent_interest.up.sql"], files["017_agent_interest.down.sql"]
	if up == "" || down == "" {
		t.Fatal("migration 017 up/down missing")
	}
	for _, col := range []string{"id ", "capabilities ", "description ", "priority ", "contact ", "contact_via ", "wallet_address ", "user_agent ", "ip_hash ", "path ", "created_at "} {
		if !strings.Contains(up, col) {
			t.Errorf("017 up is missing column %q", strings.TrimSpace(col))
		}
	}
	if !strings.Contains(up, "CREATE TABLE IF NOT EXISTS agent_interest") {
		t.Errorf("017 up must create agent_interest:\n%s", up)
	}
	if !strings.Contains(up, "ARRAY['trade', 'knowledge', 'learn', 'community', 'alerts', 'token', 'automate', 'other']::text[]") ||
		!strings.Contains(up, "cardinality(capabilities) BETWEEN 1 AND 8") {
		t.Errorf("017 up must check capabilities against the allowed keys:\n%s", up)
	}
	if !strings.Contains(up, "priority IN ('nice', 'important', 'pay')") || !strings.Contains(up, "char_length(description) <= 600") {
		t.Errorf("017 up must check priority and cap description at 600:\n%s", up)
	}
	for _, idx := range []string{"idx_agent_interest_created_at", "idx_agent_interest_priority", "idx_agent_interest_capabilities ON agent_interest USING GIN (capabilities)"} {
		if !strings.Contains(up, idx) {
			t.Errorf("017 up must create index %s", idx)
		}
		if !strings.Contains(down, strings.Fields(idx)[0]) {
			t.Errorf("017 down must drop index %s", strings.Fields(idx)[0])
		}
	}
	if !strings.Contains(down, "DROP TABLE IF EXISTS agent_interest") {
		t.Errorf("017 down must drop agent_interest:\n%s", down)
	}
}

// TestMigration020_DevDrips pins the devnet drip table: one row per wallet (the primary key
// is what makes "once per 24 h per wallet" an upsert) and an index for the per-IP hourly cap.
func TestMigration020_DevDrips(t *testing.T) {
	files := readMigrations(t)
	up, down := files["020_dev_drips.up.sql"], files["020_dev_drips.down.sql"]
	if up == "" || down == "" {
		t.Fatal("migration 020 up/down missing")
	}
	if !strings.Contains(up, "CREATE TABLE IF NOT EXISTS dev_drips") || !strings.Contains(up, "wallet        TEXT PRIMARY KEY") {
		t.Errorf("020 up must create dev_drips keyed by wallet:\n%s", up)
	}
	for _, col := range []string{"ip ", "signature ", "amount_sol ", "amount_stonk ", "dripped_at "} {
		if !strings.Contains(up, col) {
			t.Errorf("020 up must define column %q", strings.TrimSpace(col))
		}
	}
	if !strings.Contains(up, "ON dev_drips(ip, dripped_at") {
		t.Errorf("020 up must index (ip, dripped_at) for the per-IP cap:\n%s", up)
	}
	if !strings.Contains(down, "DROP TABLE IF EXISTS dev_drips") {
		t.Errorf("020 down must drop dev_drips:\n%s", down)
	}
}

// TestMigration021_AutopilotReplies pins the autopilot flag on forum_replies: a NOT NULL
// boolean defaulting to FALSE (existing manual replies stay manual) and a partial index over
// the auto rows per author for the caps and the outcomes query.
func TestMigration021_AutopilotReplies(t *testing.T) {
	files := readMigrations(t)
	up, down := files["021_autopilot_replies.up.sql"], files["021_autopilot_replies.down.sql"]
	if up == "" || down == "" {
		t.Fatal("migration 021 up/down missing")
	}
	if !strings.Contains(up, "ALTER TABLE forum_replies ADD COLUMN IF NOT EXISTS auto BOOLEAN NOT NULL DEFAULT FALSE") {
		t.Errorf("021 up must add forum_replies.auto BOOLEAN NOT NULL DEFAULT FALSE:\n%s", up)
	}
	if !strings.Contains(up, "ON forum_replies(author_peer_id, created_at DESC) WHERE auto") {
		t.Errorf("021 up must index auto replies by (author_peer_id, created_at):\n%s", up)
	}
	if !strings.Contains(down, "DROP COLUMN IF EXISTS auto") || !strings.Contains(down, "DROP INDEX IF EXISTS idx_forum_replies_auto_author_created") {
		t.Errorf("021 down must drop the auto column and its index:\n%s", down)
	}
}

// TestMigration022_BoardPhase0 pins community board phase 0: the bounty extension and refund
// columns, the generated search vector with its GIN index, and the board_activity table with
// its per-peer newest-first index.
func TestMigration022_BoardPhase0(t *testing.T) {
	files := readMigrations(t)
	up, down := files["022_board_phase0.up.sql"], files["022_board_phase0.down.sql"]
	if up == "" || down == "" {
		t.Fatal("migration 022 up/down missing")
	}
	for _, want := range []string{
		"ADD COLUMN IF NOT EXISTS bounty_extended BOOLEAN NOT NULL DEFAULT FALSE",
		"ADD COLUMN IF NOT EXISTS bounty_refunded_at TIMESTAMPTZ",
		"ADD COLUMN IF NOT EXISTS search_tsv TSVECTOR GENERATED ALWAYS AS",
		"CREATE INDEX IF NOT EXISTS idx_forum_posts_search_tsv ON forum_posts USING GIN (search_tsv)",
		"CREATE TABLE IF NOT EXISTS board_activity",
		"ON board_activity(peer_id, created_at DESC)",
	} {
		if !strings.Contains(up, want) {
			t.Errorf("022 up must contain %q:\n%s", want, up)
		}
	}
	for _, want := range []string{
		"DROP TABLE IF EXISTS board_activity",
		"DROP INDEX IF EXISTS idx_forum_posts_search_tsv",
		"DROP COLUMN IF EXISTS search_tsv",
		"DROP COLUMN IF EXISTS bounty_refunded_at",
		"DROP COLUMN IF EXISTS bounty_extended",
	} {
		if !strings.Contains(down, want) {
			t.Errorf("022 down must contain %q:\n%s", want, down)
		}
	}
}

// TestMigration023_BountyEscrowSplit pins the escrow split column: how much of a bounty escrow
// left the paid bucket, so an expiry refund returns each part to the bucket it came from.
func TestMigration023_BountyEscrowSplit(t *testing.T) {
	files := readMigrations(t)
	up, down := files["023_bounty_escrow_split.up.sql"], files["023_bounty_escrow_split.down.sql"]
	if up == "" || down == "" {
		t.Fatal("migration 023 up/down missing")
	}
	if !strings.Contains(up, "ALTER TABLE forum_posts ADD COLUMN IF NOT EXISTS bounty_escrow_paid INT NOT NULL DEFAULT 0") {
		t.Errorf("023 up must add forum_posts.bounty_escrow_paid INT NOT NULL DEFAULT 0:\n%s", up)
	}
	if !strings.Contains(down, "DROP COLUMN IF EXISTS bounty_escrow_paid") {
		t.Errorf("023 down must drop bounty_escrow_paid:\n%s", down)
	}
}

// TestMigration024_BoardPhase1 pins community board phase 1: the peer_reputation table, the
// accepted answer, hidden, pinned, token offer and mention columns on posts, hidden and mentions
// on replies, the token_offer_payments, board_reports and board_watches tables, and the symbol
// column plus BIGINT amount on board_activity.
func TestMigration024_BoardPhase1(t *testing.T) {
	files := readMigrations(t)
	up, down := files["024_board_phase1.up.sql"], files["024_board_phase1.down.sql"]
	if up == "" || down == "" {
		t.Fatal("migration 024 up/down missing")
	}
	for _, want := range []string{
		"CREATE TABLE IF NOT EXISTS peer_reputation",
		"ADD COLUMN IF NOT EXISTS accepted_reply_id TEXT",
		"ADD COLUMN IF NOT EXISTS hidden BOOLEAN NOT NULL DEFAULT FALSE",
		"ADD COLUMN IF NOT EXISTS pinned BOOLEAN NOT NULL DEFAULT FALSE",
		"ADD COLUMN IF NOT EXISTS token_offer_mint TEXT",
		"ADD COLUMN IF NOT EXISTS token_offer_paid INT NOT NULL DEFAULT 0",
		"ADD COLUMN IF NOT EXISTS mention_peer_ids TEXT[] NOT NULL DEFAULT '{}'",
		"ALTER TABLE forum_posts ALTER COLUMN token_offer_amount TYPE BIGINT",
		"CREATE TABLE IF NOT EXISTS token_offer_payments",
		"reply_id      TEXT NOT NULL UNIQUE",
		"CREATE TABLE IF NOT EXISTS board_reports",
		"UNIQUE (target_type, target_id, reporter_peer_id)",
		"CREATE TABLE IF NOT EXISTS board_watches",
		"ADD COLUMN IF NOT EXISTS symbol TEXT NOT NULL DEFAULT ''",
		"ALTER TABLE board_activity ALTER COLUMN amount TYPE BIGINT",
	} {
		if !strings.Contains(up, want) {
			t.Errorf("024 up must contain %q:\n%s", want, up)
		}
	}
	for _, want := range []string{
		"DROP TABLE IF EXISTS board_watches",
		"DROP TABLE IF EXISTS board_reports",
		"DROP TABLE IF EXISTS token_offer_payments",
		"DROP TABLE IF EXISTS peer_reputation",
		"DROP COLUMN IF EXISTS accepted_reply_id",
		"DROP COLUMN IF EXISTS mention_peer_ids",
		"DROP COLUMN IF EXISTS symbol",
		"ALTER TABLE forum_posts ALTER COLUMN token_offer_amount TYPE INT",
	} {
		if !strings.Contains(down, want) {
			t.Errorf("024 down must contain %q:\n%s", want, down)
		}
	}
}

// TestMigration025_BoardPhase2 pins community board phase 2: room_mint, room_pinned (one
// pinned announcement per room) and routed_to on posts, the peer_autopilot and
// room_holder_snapshots tables and the unread activity index.
func TestMigration025_BoardPhase2(t *testing.T) {
	files := readMigrations(t)
	up, down := files["025_board_phase2.up.sql"], files["025_board_phase2.down.sql"]
	if up == "" || down == "" {
		t.Fatal("migration 025 up/down missing")
	}
	for _, want := range []string{
		"ADD COLUMN IF NOT EXISTS room_mint TEXT",
		"ADD COLUMN IF NOT EXISTS room_pinned BOOLEAN NOT NULL DEFAULT FALSE",
		"ADD COLUMN IF NOT EXISTS routed_to TEXT[] NOT NULL DEFAULT '{}'",
		"CREATE UNIQUE INDEX IF NOT EXISTS idx_forum_posts_room_announcement ON forum_posts(room_mint) WHERE room_pinned",
		"CREATE TABLE IF NOT EXISTS peer_autopilot",
		"CREATE TABLE IF NOT EXISTS room_holder_snapshots",
		"PRIMARY KEY (mint, taken_on)",
		"idx_board_activity_peer_unread",
	} {
		if !strings.Contains(up, want) {
			t.Errorf("025 up must contain %q:\n%s", want, up)
		}
	}
	for _, want := range []string{
		"DROP TABLE IF EXISTS room_holder_snapshots",
		"DROP TABLE IF EXISTS peer_autopilot",
		"DROP INDEX IF EXISTS idx_forum_posts_room_announcement",
		"DROP COLUMN IF EXISTS routed_to",
		"DROP COLUMN IF EXISTS room_pinned",
		"DROP COLUMN IF EXISTS room_mint",
	} {
		if !strings.Contains(down, want) {
			t.Errorf("025 down must contain %q:\n%s", want, down)
		}
	}
}

// TestMigration026_BoardAutoPosts pins the auto flag on posts (Agent Autopilot posts, the digest).
func TestMigration026_BoardAutoPosts(t *testing.T) {
	files := readMigrations(t)
	up, down := files["026_board_auto_posts.up.sql"], files["026_board_auto_posts.down.sql"]
	if up == "" || down == "" {
		t.Fatal("migration 026 up/down missing")
	}
	if !strings.Contains(up, "ALTER TABLE forum_posts ADD COLUMN IF NOT EXISTS auto BOOLEAN NOT NULL DEFAULT FALSE") {
		t.Errorf("026 up must add forum_posts.auto:\n%s", up)
	}
	if !strings.Contains(down, "ALTER TABLE forum_posts DROP COLUMN IF EXISTS auto") {
		t.Errorf("026 down must drop forum_posts.auto:\n%s", down)
	}
}

// TestMigration027_BoardPhase3 pins the reply ask column (bounty negotiation). 027 was applied
// on dev in this shape; anything more belongs in a later migration (see 028).
func TestMigration027_BoardPhase3(t *testing.T) {
	files := readMigrations(t)
	up, down := files["027_board_phase3.up.sql"], files["027_board_phase3.down.sql"]
	if up == "" || down == "" {
		t.Fatal("migration 027 up/down missing")
	}
	if !strings.Contains(up, "ALTER TABLE forum_replies ADD COLUMN IF NOT EXISTS ask INT NOT NULL DEFAULT 0 CHECK (ask >= 0)") {
		t.Errorf("027 up must add forum_replies.ask:\n%s", up)
	}
	if !strings.Contains(up, "CREATE INDEX IF NOT EXISTS idx_forum_replies_post_ask ON forum_replies(post_id) WHERE ask > 0") {
		t.Errorf("027 up must add the askers index:\n%s", up)
	}
	if strings.Contains(up, "relevance") {
		t.Errorf("027 must not carry the relevance columns (already applied on dev without them; they are 028):\n%s", up)
	}
	if !strings.Contains(down, "DROP INDEX IF EXISTS idx_forum_replies_post_ask") || !strings.Contains(down, "ALTER TABLE forum_replies DROP COLUMN IF EXISTS ask") {
		t.Errorf("027 down must drop the index and forum_replies.ask:\n%s", down)
	}
}

// TestMigration028_ReplyRelevance pins the daemon's relevance score and signals on replies.
func TestMigration028_ReplyRelevance(t *testing.T) {
	files := readMigrations(t)
	up, down := files["028_reply_relevance.up.sql"], files["028_reply_relevance.down.sql"]
	if up == "" || down == "" {
		t.Fatal("migration 028 up/down missing")
	}
	if !strings.Contains(up, "ALTER TABLE forum_replies ADD COLUMN IF NOT EXISTS relevance DOUBLE PRECISION") {
		t.Errorf("028 up must add forum_replies.relevance:\n%s", up)
	}
	if !strings.Contains(up, "ALTER TABLE forum_replies ADD COLUMN IF NOT EXISTS relevance_signals JSONB") {
		t.Errorf("028 up must add forum_replies.relevance_signals:\n%s", up)
	}
	if !strings.Contains(down, "ALTER TABLE forum_replies DROP COLUMN IF EXISTS relevance_signals") || !strings.Contains(down, "ALTER TABLE forum_replies DROP COLUMN IF EXISTS relevance") {
		t.Errorf("028 down must drop both columns:\n%s", down)
	}
}

// TestMigration030_BoardRound2 pins the round 2 columns, tables and indexes.
func TestMigration030_BoardRound2(t *testing.T) {
	files := readMigrations(t)
	up, down := files["030_board_round2.up.sql"], files["030_board_round2.down.sql"]
	if up == "" || down == "" {
		t.Fatal("migration 030 up/down missing")
	}
	for _, want := range []string{
		"ALTER TABLE forum_posts ADD COLUMN IF NOT EXISTS deleted_at TIMESTAMPTZ",
		"ALTER TABLE forum_posts ADD COLUMN IF NOT EXISTS body_hash TEXT NOT NULL DEFAULT ''",
		"ALTER TABLE forum_posts ADD COLUMN IF NOT EXISTS bounty_dispute_status TEXT NOT NULL DEFAULT ''",
		"ALTER TABLE forum_posts ADD COLUMN IF NOT EXISTS routed_reasons JSONB",
		"ALTER TABLE forum_replies ADD COLUMN IF NOT EXISTS deleted_at TIMESTAMPTZ",
		"ALTER TABLE forum_upvotes ADD COLUMN IF NOT EXISTS created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()",
		"CREATE TABLE IF NOT EXISTS board_edit_history",
		"CREATE TABLE IF NOT EXISTS board_room_settings",
		"CREATE TABLE IF NOT EXISTS board_room_mutes",
		"CREATE TABLE IF NOT EXISTS board_visits",
		"CREATE TABLE IF NOT EXISTS board_notification_prefs",
		"CREATE INDEX IF NOT EXISTS idx_forum_posts_main_recent",
		"CREATE INDEX IF NOT EXISTS idx_forum_upvotes_peer_created",
		"CREATE INDEX IF NOT EXISTS idx_peer_reputation_score",
	} {
		if !strings.Contains(up, want) {
			t.Errorf("030 up lacks %q", want)
		}
	}
	for _, want := range []string{
		"DROP TABLE IF EXISTS board_notification_prefs", "DROP TABLE IF EXISTS board_visits", "DROP TABLE IF EXISTS board_room_mutes",
		"DROP TABLE IF EXISTS board_room_settings", "DROP TABLE IF EXISTS board_edit_history",
		"ALTER TABLE forum_upvotes DROP COLUMN IF EXISTS created_at", "ALTER TABLE forum_posts DROP COLUMN IF EXISTS deleted_at",
		"ALTER TABLE forum_replies DROP COLUMN IF EXISTS deleted_at", "DROP INDEX IF EXISTS idx_forum_posts_main_recent",
	} {
		if !strings.Contains(down, want) {
			t.Errorf("030 down lacks %q", want)
		}
	}
}
