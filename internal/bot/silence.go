package bot

import (
	"context"
	"log"
	"sync"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"youtubych/internal/database"
	"youtubych/internal/gemini"
)

// SilenceManager tracks chat inactivity and generates discussion topics.
type SilenceManager struct {
	mu                      sync.RWMutex
	chatID                  int64
	silenceDuration         time.Duration
	lastMessageTime         time.Time
	lastSilenceBreakerTime  time.Time
	bot                     *tgbotapi.BotAPI
	gemini                  *gemini.Client
	db                      *database.DB
	stopChan                chan struct{}
}

// NewSilenceManager creates and initializes the silence manager.
func NewSilenceManager(chatID int64, duration time.Duration, bot *tgbotapi.BotAPI, gemini *gemini.Client, db *database.DB) *SilenceManager {
	return &SilenceManager{
		chatID:                 chatID,
		silenceDuration:        duration,
		lastMessageTime:        time.Now(),
		lastSilenceBreakerTime: time.Time{},
		bot:                    bot,
		gemini:                 gemini,
		db:                     db,
		stopChan:               make(chan struct{}),
	}
}

// RecordActivity resets the silence timer when any message arrives in the allowed chat.
func (sm *SilenceManager) RecordActivity() {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.lastMessageTime = time.Now()
}

// GetLastActivity returns the timestamp of the last detected chat activity.
func (sm *SilenceManager) GetLastActivity() time.Time {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.lastMessageTime
}

// Start begins the background check routine.
func (sm *SilenceManager) Start() {
	go func() {
		ticker := time.NewTicker(1 * time.Minute)
		defer ticker.Stop()

		for {
			select {
			case <-sm.stopChan:
				return
			case <-ticker.C:
				sm.checkSilence()
			}
		}
	}()
}

// Stop terminates the background silence checker.
func (sm *SilenceManager) Stop() {
	close(sm.stopChan)
}

func (sm *SilenceManager) checkSilence() {
	sm.mu.Lock()
	now := time.Now()

	// Check if chat has been quiet for longer than the threshold
	isSilent := now.Sub(sm.lastMessageTime) >= sm.silenceDuration
	// Ensure we haven't already sent a silence breaker in this silence cycle
	canBreak := sm.lastSilenceBreakerTime.Before(sm.lastMessageTime) || now.Sub(sm.lastSilenceBreakerTime) >= sm.silenceDuration

	if !isSilent || !canBreak {
		sm.mu.Unlock()
		return
	}

	// Update silence breaker timestamp immediately to prevent re-entry
	sm.lastSilenceBreakerTime = now
	sm.mu.Unlock()

	log.Printf("[SilenceManager] Chat %d has been silent for %v. Generating icebreaker topic...", sm.chatID, sm.silenceDuration)
	if err := sm.SendIcebreakerTopic(); err != nil {
		log.Printf("[SilenceManager] Failed to send silence breaker: %v", err)
	}
}

// SendIcebreakerTopic generates and posts an engaging YouTube topic to the chat.
func (sm *SilenceManager) SendIcebreakerTopic() error {
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()

	// Request Gemini to generate a lively YouTube topic (with search grounding if fresh topics available)
	topic, err := sm.gemini.GenerateAnswer(ctx, gemini.BaseSystemPrompt, gemini.SilenceBreakerPrompt, true)
	if err != nil {
		// Fallback static topics if Gemini completely fails
		fallbackTopics := []string{
			"Ребята, пока в чате тишина — накину тему: кто как сейчас относится к Shorts? Дают они реальный прирост подписчиков на длинные видео или только убивают удержание?",
			"Чат затих — все, видимо, на ночном рендере! Вопрос на засыпку: сколько времени у вас обычно уходит на создание одной обложки (превью) и тестируете ли вы A/B варианты?",
			"Кстати, коллеги-ютуберы, заметили ли вы изменения в алгоритмах показов за последнюю неделю? Что сейчас лучше работает: кликбейтный заголовок или упор на первые 30 секунд удержания?",
		}
		// Pick one based on unix timestamp
		topic = fallbackTopics[time.Now().Unix()%int64(len(fallbackTopics))]
	}

	msg := tgbotapi.NewMessage(sm.chatID, topic)
	_, sendErr := sm.bot.Send(msg)
	if sendErr == nil {
		_ = sm.db.IncrementStat("silence_breakers_sent")
	}
	return sendErr
}
