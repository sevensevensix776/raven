// Package clean ports the reply-cleaning sed/tr pipeline from speak-reply.sh
// into a pure, testable function. The bash was:
//
//	sed -e '/^[[:space:]]*```/,/^[[:space:]]*```/d'   # drop fenced code blocks
//	    -e 's/`[^`]*`/ /g'                            # inline code -> space
//	    -e 's/[*_#>|]//g'                             # strip markdown punct
//	    -e 's|/[A-Za-z0-9._/-]\{12,\}| that path |g'  # long paths -> " that path "
//	| tr -s ' \n' ' '                                 # collapse space/newline runs
//	| head -c cap                                     # cap (bytes; 0 = unlimited)
package clean

import (
	"regexp"
	"strings"
)

var (
	fenceLine  = regexp.MustCompile("^[ \t]*```")
	inlineCode = regexp.MustCompile("`[^`]*`")
	mdPunct    = regexp.MustCompile(`[*_#>|]`)
	longPath   = regexp.MustCompile(`/[A-Za-z0-9._/-]{12,}`)
	spaceRuns  = regexp.MustCompile(`[ \n]+`)
	tildeNum   = regexp.MustCompile(`~(\d)`) // "~5" -> "about 5" (approximation)
)

// spokenSubst rewrites characters Kokoro reads badly into how they should sound,
// or drops them. THIS IS THE PLACE to fix speech pronunciation: whenever you
// catch the voice mangling a symbol, add a `"from", "to"` pair here (and a case
// to clean_test.go). Applied only to spoken text (Reply), never to the readable
// on-screen transcript (Display), which keeps its symbols.
var spokenSubst = strings.NewReplacer(
	// Arrows are notation, not speech — drop them.
	"←", " ", "→", " ", "↑", " ", "↓", " ", "↔", " ",
	"⟵", " ", "⟶", " ", "⇐", " ", "⇒", " ", "↝", " ", "➜", " ",
	// Standalone tilde (after the ~N "about N" pass) and bullets Kokoro fumbles.
	"~", " ", "•", " ", "·", " ",
)

// Reply cleans an assistant reply for speech. cap is a byte cap; cap <= 0 means
// unlimited (matching MAX_SPOKEN_CHARS=0). The byte cap backs off to a UTF-8
// rune boundary so we never emit an invalid partial rune (bash cut raw bytes;
// this only diverges on the rare capped-mid-multibyte case, and the default is
// uncapped).
func Reply(text string, cap int) string {
	text = dropFencedBlocks(text)
	text = inlineCode.ReplaceAllString(text, " ")
	text = mdPunct.ReplaceAllString(text, "")
	text = longPath.ReplaceAllString(text, " that path ")
	text = tildeNum.ReplaceAllString(text, "about $1") // "~5 min" -> "about 5 min"
	text = spokenSubst.Replace(text)                   // arrows/tilde/bullets Kokoro fumbles
	text = spaceRuns.ReplaceAllString(text, " ")
	if cap > 0 && len(text) > cap {
		text = text[:cap]
		for len(text) > 0 && !validLastRune(text) {
			text = text[:len(text)-1]
		}
	}
	return text
}

// Display cleans an assistant reply for reading without flattening its
// structure. Fenced code is deliberately summarized because full code blocks
// are noisy in the driving UI; all other Markdown and line breaks are retained.
func Display(text string) string {
	return replaceFencedBlocks(text, "[code]")
}

// dropFencedBlocks deletes lines from an opening ``` fence line through the next
// fence line inclusive, matching sed's `/re/,/re/d` range (an unterminated fence
// deletes to end of input).
func dropFencedBlocks(text string) string {
	return replaceFencedBlocks(text, "")
}

func replaceFencedBlocks(text, replacement string) string {
	lines := strings.Split(text, "\n")
	var kept []string
	inFence := false
	for _, ln := range lines {
		if fenceLine.MatchString(ln) {
			if !inFence && replacement != "" {
				kept = append(kept, replacement)
			}
			inFence = !inFence
			continue // the fence line itself is deleted in both states
		}
		if !inFence {
			kept = append(kept, ln)
		}
	}
	return strings.Join(kept, "\n")
}

func validLastRune(s string) bool {
	// True if s ends on a complete UTF-8 rune.
	for i := len(s) - 1; i >= 0 && i >= len(s)-4; i-- {
		b := s[i]
		if b&0xC0 != 0x80 { // start byte of a rune
			runeLen := 1
			switch {
			case b&0x80 == 0x00:
				runeLen = 1
			case b&0xE0 == 0xC0:
				runeLen = 2
			case b&0xF0 == 0xE0:
				runeLen = 3
			case b&0xF8 == 0xF0:
				runeLen = 4
			}
			return len(s)-i == runeLen
		}
	}
	return false
}

// IsBlank reports whether s is empty or all spaces — matches `[ -z "${x// }" ]`.
func IsBlank(s string) bool {
	return strings.TrimFunc(s, func(r rune) bool { return r == ' ' }) == ""
}

// Collapse mirrors registry_line: newlines->spaces, runs squeezed, byte-capped.
func Collapse(text string, cap int) string {
	text = spaceRuns.ReplaceAllString(strings.ReplaceAll(text, "\n", " "), " ")
	if cap > 0 && len(text) > cap {
		text = text[:cap]
		for len(text) > 0 && !validLastRune(text) {
			text = text[:len(text)-1]
		}
	}
	return text
}
