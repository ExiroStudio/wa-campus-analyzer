package worker

import (
	"testing"
)

func TestPreFilter(t *testing.T) {
	tests := []struct {
		name          string
		isIgnoredChat bool
		body          string
		hasMedia      int
		wantSkip      bool
		wantReason    string
	}{
		{
			name:          "Ignored chat",
			isIgnoredChat: true,
			body:          "Pak kumpulkan tugas",
			hasMedia:      0,
			wantSkip:      true,
			wantReason:    "chat is in ignored_chats",
		},
		{
			name:          "Empty body no media",
			isIgnoredChat: false,
			body:          "   ",
			hasMedia:      0,
			wantSkip:      true,
			wantReason:    "empty body",
		},
		{
			name:          "Media without text (sticker / image without caption)",
			isIgnoredChat: false,
			body:          "",
			hasMedia:      1,
			wantSkip:      true,
			wantReason:    "media tanpa teks",
		},
		{
			name:          "Under 3 characters",
			isIgnoredChat: false,
			body:          "hi",
			hasMedia:      0,
			wantSkip:      true,
			wantReason:    "body less than 3 characters",
		},
		{
			name:          "Filler word ok",
			isIgnoredChat: false,
			body:          "ok",
			hasMedia:      0,
			wantSkip:      true,
			wantReason:    "filler word: ok",
		},
		{
			name:          "Filler word oke!",
			isIgnoredChat: false,
			body:          "oke!",
			hasMedia:      0,
			wantSkip:      true,
			wantReason:    "filler word: oke",
		},
		{
			name:          "Filler word Siap.",
			isIgnoredChat: false,
			body:          "Siap.",
			hasMedia:      0,
			wantSkip:      true,
			wantReason:    "filler word: siap",
		},
		{
			name:          "Filler word makasih",
			isIgnoredChat: false,
			body:          "makasih",
			hasMedia:      0,
			wantSkip:      true,
			wantReason:    "filler word: makasih",
		},
		{
			name:          "Only emojis",
			isIgnoredChat: false,
			body:          "👍🔥",
			hasMedia:      0,
			wantSkip:      true,
			wantReason:    "only emoji or punctuation",
		},
		{
			name:          "Actionable assignment message",
			isIgnoredChat: false,
			body:          "Pak Budi: kumpulkan laporan jobsheet 5 besok jam 10 pagi",
			hasMedia:      0,
			wantSkip:      false,
		},
		{
			name:          "Image with assignment caption",
			isIgnoredChat: false,
			body:          "jadwal praktikum baru ruang 302",
			hasMedia:      1,
			wantSkip:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ShouldSkipMessage(tt.isIgnoredChat, tt.body, tt.hasMedia)
			if got.ShouldSkip != tt.wantSkip {
				t.Errorf("ShouldSkip = %v, want %v", got.ShouldSkip, tt.wantSkip)
			}
			if tt.wantReason != "" && got.Reason != tt.wantReason {
				t.Errorf("Reason = %q, want %q", got.Reason, tt.wantReason)
			}
		})
	}
}
