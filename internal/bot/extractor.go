package bot

import (
	"context"
	"encoding/json"
	"log"
	"strings"
	"time"

	"youtubych/internal/database"
	"youtubych/internal/gemini"
)

type extractedInfo struct {
	HasInfo      bool   `json:"has_info"`
	ChannelName  string `json:"channel_name"`
	ChannelTheme string `json:"channel_theme"`
	Subscribers  string `json:"subscribers"`
	Notes        string `json:"notes"`
}

// MaybeExtractUserInfo asynchronously examines user message for self-reported YouTube channel details.
func MaybeExtractUserInfo(geminiClient *gemini.Client, db *database.DB, userID int64, userMessage string) {
	// Simple heuristic: only trigger if message contains channel-related keywords to avoid unnecessary API calls
	lower := strings.ToLower(userMessage)
	keywords := []string{"канал", "подписчик", "саб", "снимаю", "ниша", "ролик", "видео", "просмотр", "шортс", "монетизаци"}
	found := false
	for _, kw := range keywords {
		if strings.Contains(lower, kw) {
			found = true
			break
		}
	}
	if !found {
		return
	}

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		defer cancel()

		prompt := gemini.ExtractUserInfoPrompt(userMessage)
		// No need for search grounding for fact extraction
		rawResp, err := geminiClient.GenerateAnswer(ctx, "", prompt, false)
		if err != nil {
			return
		}

		// Clean JSON response from markdown wrappers if any
		cleaned := strings.TrimSpace(rawResp)
		cleaned = strings.TrimPrefix(cleaned, "```json")
		cleaned = strings.TrimPrefix(cleaned, "```")
		cleaned = strings.TrimSuffix(cleaned, "```")
		cleaned = strings.TrimSpace(cleaned)

		var info extractedInfo
		if err := json.Unmarshal([]byte(cleaned), &info); err != nil {
			return
		}

		if info.HasInfo {
			log.Printf("[Learner] Learned new info about user %d: Channel='%s', Theme='%s', Subs='%s', Notes='%s'",
				userID, info.ChannelName, info.ChannelTheme, info.Subscribers, info.Notes)
			_ = db.UpdateUserProfile(userID, info.ChannelName, info.ChannelTheme, info.Subscribers, info.Notes)
		}
	}()
}
