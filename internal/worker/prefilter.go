package worker

import (
	"strings"
	"unicode"
)

var defaultFillerWords = map[string]struct{}{
	"ok":          {},
	"oke":         {},
	"okey":        {},
	"okee":        {},
	"siap":        {},
	"siapp":       {},
	"makasih":     {},
	"terimakasih": {},
	"thanks":      {},
	"tq":          {},
	"thx":         {},
	"sip":         {},
	"sipp":        {},
	"wkwk":        {},
	"wkwkwk":      {},
	"haha":        {},
	"hahaha":      {},
	"hehe":        {},
	"hehehe":      {},
	"iya":         {},
	"ya":          {},
	"y":           {},
	"yo":          {},
	"yoi":         {},
	"mantap":      {},
	"mantul":      {},
	"p":           {},
}

type PreFilterResult struct {
	ShouldSkip bool
	Reason     string
}

// ShouldSkipMessage evaluates pre-filter rules before calling AI.
func ShouldSkipMessage(isIgnoredChat bool, body string, hasMedia int) PreFilterResult {
	if isIgnoredChat {
		return PreFilterResult{ShouldSkip: true, Reason: "chat is in ignored_chats"}
	}

	trimmed := strings.TrimSpace(body)

	// Body kosong / media tanpa teks
	if trimmed == "" {
		if hasMedia == 1 {
			return PreFilterResult{ShouldSkip: true, Reason: "media tanpa teks"}
		}
		return PreFilterResult{ShouldSkip: true, Reason: "empty body"}
	}

	// Cek apakah hanya emoji dan tanda baca/whitespace
	if isOnlyEmojiAndPunctuation(trimmed) {
		return PreFilterResult{ShouldSkip: true, Reason: "only emoji or punctuation"}
	}

	// Cek apakah hanya kata ringan / filler
	cleanWord := strings.ToLower(strings.TrimFunc(trimmed, func(r rune) bool {
		return unicode.IsPunct(r) || unicode.IsSpace(r)
	}))
	if _, ok := defaultFillerWords[cleanWord]; ok {
		return PreFilterResult{ShouldSkip: true, Reason: "filler word: " + cleanWord}
	}

	// Kurang dari 3 karakter
	if len([]rune(trimmed)) < 3 {
		return PreFilterResult{ShouldSkip: true, Reason: "body less than 3 characters"}
	}

	return PreFilterResult{ShouldSkip: false}
}

func isOnlyEmojiAndPunctuation(s string) bool {
	hasNonPunct := false
	for _, r := range s {
		if unicode.IsSpace(r) || unicode.IsPunct(r) {
			continue
		}
		if isEmoji(r) {
			hasNonPunct = true
			continue
		}
		// If there is any letter or digit, it's not purely emoji
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return false
		}
	}
	return hasNonPunct
}

func isEmoji(r rune) bool {
	// Standard emoji Unicode ranges
	return (r >= 0x1F600 && r <= 0x1F64F) || // Emoticons
		(r >= 0x1F300 && r <= 0x1F5FF) || // Misc Symbols and Pictographs
		(r >= 0x1F680 && r <= 0x1F6FF) || // Transport and Map
		(r >= 0x1F1E0 && r <= 0x1F1FF) || // Regional indicator symbol (flags)
		(r >= 0x2600 && r <= 0x26FF) || // Misc symbols
		(r >= 0x2700 && r <= 0x27BF) || // Dingbats
		(r >= 0xFE00 && r <= 0xFE0F) || // Variation Selectors
		(r >= 0x1F900 && r <= 0x1F9FF) || // Supplemental Symbols and Pictographs
		(r >= 0x1FA70 && r <= 0x1FAFF) // Symbols and Pictographs Extended-A
}
