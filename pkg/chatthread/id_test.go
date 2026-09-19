package chatthread

import "testing"

func TestStableID_Deterministic(t *testing.T) {
	a := StableID("user1", "")
	b := StableID("user1", "")
	if a != b || a == "" {
		t.Fatalf("expected stable id, got %q %q", a, b)
	}
	if StableID("user1", "") == StableID("user2", "") {
		t.Fatal("different users should not collide")
	}
	if StableID("u", "tokenA") == StableID("u", "tokenB") {
		t.Fatal("different tokens should not collide")
	}
	if PersonalContextTokenAddress != "0x" {
		t.Fatalf("PersonalContextTokenAddress = %q", PersonalContextTokenAddress)
	}
}
