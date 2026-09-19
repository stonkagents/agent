package models

import (
	"strings"
	"testing"
)

func TestSanitizeBoardText(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"plain", "  hello board  ", "hello board"},
		{"script block", "before <script>alert(1)</script> after", "before  after"},
		{"style block and comment", "a<style>x{}</style>b<!-- c -->d", "abd"},
		{"html tags stripped", `<b>bold</b> <a href="x">link</a> <img src=x onerror=alert(1)> <br/> text`, "bold link   text"},
		{"case and spacing", "<SCRIPT >x</SCRIPT ><Div class=q>y</dIv>", "y"},
		{"space after the bracket is not a tag", "< b> and </ b>", "< b> and </ b>"},
		{"code comparisons kept", "if a < b && c > d { Vec<T> ch <- x }", "if a < b && c > d { Vec<T> ch <- x }"},
		{"unknown tag kept", "<Item> and <Foo bar>", "<Item> and <Foo bar>"},
		{"control chars dropped, newline and tab kept", "line1\r\nline2\tx\x00\x07\x1b[31m", "line1\nline2\tx[31m"},
		{"invisible format chars dropped", "pay\u200Bpal \u202Eevil\u202C name\uFEFF", "paypal evil name"},
		{"emoji and scripts kept", "\U0001F680 launch \u65e5\u672c\u8a9e \U0001F468\u200D\U0001F4BB", "\U0001F680 launch \u65e5\u672c\u8a9e \U0001F468\u200D\U0001F4BB"},
		{"invalid utf8 dropped", "ok\xffbad", "okbad"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := SanitizeBoardText(c.in, 0); got != c.want {
				t.Errorf("SanitizeBoardText(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestSanitizeBoardText_CapNeverSplitsARune(t *testing.T) {
	in := strings.Repeat("\u65e5", 12)
	got := SanitizeBoardText(in, 5)
	if got != strings.Repeat("\u65e5", 5) {
		t.Fatalf("cap = %q", got)
	}
	if !BoardTextTooLong(in, 5) || BoardTextTooLong(got, 5) {
		t.Fatal("BoardTextTooLong counts runes")
	}
}

func TestSanitizeBoardLine(t *testing.T) {
	if got := SanitizeBoardLine(" a\n\n b\t\tc <b>d</b> ", 0); got != "a b c d" {
		t.Fatalf("line = %q", got)
	}
	if got := SanitizeBoardLine("abcdefghij", 4); got != "abcd" {
		t.Fatalf("capped line = %q", got)
	}
}
