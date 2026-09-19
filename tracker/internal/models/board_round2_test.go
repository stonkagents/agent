// Package models: round 2 body rules: the duplicate-detection hash and the link rules.
package models

import (
	"errors"
	"strings"
	"testing"
)

func TestBoardBodyHash_NormalisesSpacingAndCase(t *testing.T) {
	a := BoardBodyHash("Hello   World\n")
	if a == "" || a != BoardBodyHash("hello world") || a != BoardBodyHash("  HELLO\tworld ") {
		t.Errorf("normalised hashes differ: %q", a)
	}
	if a == BoardBodyHash("hello worlds") {
		t.Errorf("different bodies share a hash")
	}
	if BoardBodyHash("   ") != "" {
		t.Errorf("blank body has a hash")
	}
}

func TestCheckBoardLinks(t *testing.T) {
	five := strings.Repeat("see https://example.com/x ", 5)
	if err := CheckBoardLinks(five); err != nil {
		t.Errorf("five links: %v", err)
	}
	var linkErr *BoardLinkError
	if err := CheckBoardLinks(five + " and http://six.example/"); !errors.As(err, &linkErr) || linkErr.Count != 6 || !strings.Contains(err.Error(), "at most 5 links") {
		t.Errorf("six links: %v", err)
	}
	for _, bad := range []string{"click javascript:alert(1)", "JAVASCRIPT:void(0)", "data:text/html;base64,AAAA", "vbscript:msgbox", "open file:///etc/passwd"} {
		if err := CheckBoardLinks(bad); !errors.As(err, &linkErr) || linkErr.Scheme == "" {
			t.Errorf("%q: %v", bad, err)
		}
	}
	for _, ok := range []string{"note: this is prose", "time 10:30 today", "ipfs://bafybeigdyrzt and mailto:a@b.c", "chan <- x; a < b", "example.com/no-scheme"} {
		if err := CheckBoardLinks(ok); err != nil {
			t.Errorf("%q refused: %v", ok, err)
		}
	}
}
