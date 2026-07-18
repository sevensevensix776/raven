package clean

import "testing"

func TestReply(t *testing.T) {
	cases := []struct {
		name string
		in   string
		cap  int
		want string
	}{
		{"plain", "Tests pass and the deploy is green.", 0, "Tests pass and the deploy is green."},
		{"inline code", "Run `yarn build` now.", 0, "Run now."},
		{"markdown punct", "**Bold** and _italic_ and # head", 0, "Bold and italic and head"},
		{"long path", "Edit /Users/asif/code/experiments/thing.go please", 0, "Edit that path please"},
		{"short path kept", "see /a/b here", 0, "see /a/b here"},
		{"newlines collapse", "line one\n\nline two", 0, "line one line two"},
		{
			"fenced block dropped",
			"Here is code:\n```go\nfmt.Println(1)\n```\nDone.",
			0,
			"Here is code: Done.",
		},
		{"cap bytes", "abcdefghij", 5, "abcde"},
		{"cap zero unlimited", "abcdefghij", 0, "abcdefghij"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Reply(c.in, c.cap); got != c.want {
				t.Errorf("Reply(%q, %d)\n got %q\nwant %q", c.in, c.cap, got, c.want)
			}
		})
	}
}

func TestDisplay(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			"preserves markdown and line breaks",
			"## Result\n\n- **Bold** and _italic_\n- Run `go test ./...`",
			"## Result\n\n- **Bold** and _italic_\n- Run `go test ./...`",
		},
		{
			"fenced block becomes marker",
			"Here is the change:\n```go\nfmt.Println(1)\n```\n\nDone.",
			"Here is the change:\n[code]\n\nDone.",
		},
		{
			"multiple fenced blocks",
			"First\n```\none\n```\nMiddle\n```swift\ntwo\n```\nLast",
			"First\n[code]\nMiddle\n[code]\nLast",
		},
		{
			"unterminated fenced block",
			"Before\n```json\n{\"unfinished\":true}",
			"Before\n[code]",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Display(c.in); got != c.want {
				t.Errorf("Display(%q)\n got %q\nwant %q", c.in, got, c.want)
			}
		})
	}
}

func TestIsBlank(t *testing.T) {
	for _, c := range []struct {
		in   string
		want bool
	}{{"", true}, {"   ", true}, {" x ", false}, {"x", false}} {
		if got := IsBlank(c.in); got != c.want {
			t.Errorf("IsBlank(%q)=%v want %v", c.in, got, c.want)
		}
	}
}

func TestCollapse(t *testing.T) {
	if got := Collapse("a\n\n  b   c", 0); got != "a b c" {
		t.Errorf("Collapse got %q", got)
	}
	if got := Collapse("abcdefghij", 4); got != "abcd" {
		t.Errorf("Collapse cap got %q", got)
	}
}
