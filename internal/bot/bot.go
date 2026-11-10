package bot

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"youtubych/internal/config"
	"youtubych/internal/database"
	"youtubych/internal/gemini"
)

// App is the main application coordinator.
type App struct {
	cfg     *config.Config
	db      *database.DB
	bot     *tgbotapi.BotAPI
	handler *BotHandler
	silence *SilenceManager
}

// NewApp creates and wires up all dependencies.
func NewApp(cfg *config.Config) (*App, error) {
	// Initialize database
	db, err := database.New(cfg.DatabasePath)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize db: %w", err)
	}

	// Initialize Telegram Bot
	tgBot, err := tgbotapi.NewBotAPI(cfg.TelegramBotToken)
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("failed to initialize telegram bot: %w", err)
	}

	log.Printf("[Bot] Authorized on account @%s (ID: %d)", tgBot.Self.UserName, tgBot.Self.ID)

	// Initialize Gemini Client
	geminiClient := gemini.NewClient(cfg.GeminiAPIKey, cfg.GeminiModel)

	// Initialize Rate Limiter
	limiter := NewRateLimiter(cfg.UserCooldown, cfg.GroupCooldown)

	// Initialize Silence Manager
	silence := NewSilenceManager(cfg.AllowedChatID, cfg.SilenceDuration, tgBot, geminiClient, db)

	// Initialize Handler
	handler := NewBotHandler(tgBot, cfg, db, geminiClient, limiter, silence)

	return &App{
		cfg:     cfg,
		db:      db,
		bot:     tgBot,
		handler: handler,
		silence: silence,
	}, nil
}

// Run starts the bot polling loop and listens for shutdown signals.
func (a *App) Run() error {
	defer a.db.Close()

	// Start 6-hour silence checker
	a.silence.Start()
	defer a.silence.Stop()

	// Configure Telegram update polling
	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60

	updates := a.bot.GetUpdatesChan(u)

	// Setup graceful shutdown on Ctrl+C / SIGTERM
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log.Printf("[Bot] YouTubych is running! Monitoring group %d for 6h inactivity and questions...", a.cfg.AllowedChatID)

	for {
		select {
		case <-ctx.Done():
			log.Println("[Bot] Received shutdown signal. Shutting down gracefully...")
			return nil
		case update, ok := <-updates:
			if !ok {
				return nil
			}
			// Process update in a separate goroutine so slow LLM calls never block telegram polling
			go a.handler.HandleUpdate(update)
		}
	}
}
