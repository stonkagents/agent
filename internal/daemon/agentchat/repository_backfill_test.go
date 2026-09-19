package agentchat

import (
	"path/filepath"
	"testing"
)

func TestBackfillSessionUserIDs_PersonalThread(t *testing.T) {
	dir := t.TempDir()
	r, err := NewRepository(filepath.Join(dir, "ac.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	wallet := "WalletAddrPersonal"
	id, err := r.EnsureUserTokenSession("sys", wallet, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.db.Exec(`UPDATE agent_chat_sessions SET user_id = '' WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	sessions, err := r.ListSessions(wallet, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 0 {
		t.Fatalf("expected no rows before backfill, got %d", len(sessions))
	}
	if err := r.BackfillSessionUserIDs(wallet, ""); err != nil {
		t.Fatal(err)
	}
	sessions, err = r.ListSessions(wallet, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].ID != id || sessions[0].UserID != wallet {
		t.Fatalf("after backfill: %+v", sessions)
	}
}
