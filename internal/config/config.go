package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

// Config holds all configuration parameters for YouTubych bot.
type Config struct {
	TelegramBotToken string
	GeminiAPIKey     string
	GeminiModel      string
	GeminiBaseURL    string
	ProxyURL         string
	AllowedChatID    int64
	OwnerTGID        int64
	SilenceDuration  time.Duration
	UserCooldown     time.Duration
	GroupCooldown    time.Duration
	DatabasePath     string
}

// Load loads configuration from environment variables and an optional .env file.
func Load() (*Config, error) {
	// Attempt to load .env file if it exists, ignore error if missing (standard env vars can be used)
	_ = godotenv.Load()

	botToken := strings.TrimSpace(os.Getenv("TELEGRAM_BOT_TOKEN"))
	if botToken == "" {
		return nil, errors.New("TELEGRAM_BOT_TOKEN is required in environment or .env")
	}

	geminiKey := strings.TrimSpace(os.Getenv("GEMINI_API_KEY"))
	if geminiKey == "" {
		return nil, errors.New("GEMINI_API_KEY is required in environment or .env")
	}

	geminiModel := strings.TrimSpace(os.Getenv("GEMINI_MODEL"))
	if geminiModel == "" {
		geminiModel = "gemini-3.7-flash"
	}

	geminiBaseURL := strings.TrimSpace(os.Getenv("GEMINI_BASE_URL"))
	if geminiBaseURL == "" {
		geminiBaseURL = "https://generativelanguage.googleapis.com"
	}
	geminiBaseURL = strings.TrimRight(geminiBaseURL, "/")

	proxyURL := strings.TrimSpace(os.Getenv("PROXY_URL"))

	allowedChatStr := strings.TrimSpace(os.Getenv("ALLOWED_CHAT_ID"))
	if allowedChatStr == "" {
		return nil, errors.New("ALLOWED_CHAT_ID is required (the Telegram group chat ID where YouTubych is allowed to speak)")
	}
	allowedChatID, err := strconv.ParseInt(allowedChatStr, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid ALLOWED_CHAT_ID '%s': %w", allowedChatStr, err)
	}

	ownerTGStr := strings.TrimSpace(os.Getenv("OWNER_TG_ID"))
	if ownerTGStr == "" {
		return nil, errors.New("OWNER_TG_ID is required (your Telegram user ID for private administration)")
	}
	ownerTGID, err := strconv.ParseInt(ownerTGStr, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid OWNER_TG_ID '%s': %w", ownerTGStr, err)
	}

	// Silence duration (default: 6h)
	silenceDuration := 6 * time.Hour
	if s := os.Getenv("SILENCE_DURATION"); s != "" {
		if d, err := time.ParseDuration(s); err == nil && d > 0 {
			silenceDuration = d
		}
	}

	// User cooldown (default: 15s)
	userCooldown := 15 * time.Second
	if s := os.Getenv("USER_COOLDOWN"); s != "" {
		if d, err := time.ParseDuration(s); err == nil && d > 0 {
			userCooldown = d
		}
	}

	// Group cooldown (default: 5s)
	groupCooldown := 5 * time.Second
	if s := os.Getenv("GROUP_COOLDOWN"); s != "" {
		if d, err := time.ParseDuration(s); err == nil && d > 0 {
			groupCooldown = d
		}
	}

	dbPath := strings.TrimSpace(os.Getenv("DATABASE_PATH"))
	if dbPath == "" {
		dbPath = "youtubych.db"
	}

	return &Config{
		TelegramBotToken: botToken,
		GeminiAPIKey:     geminiKey,
		GeminiModel:      geminiModel,
		GeminiBaseURL:    geminiBaseURL,
		ProxyURL:         proxyURL,
		AllowedChatID:    allowedChatID,
		OwnerTGID:        ownerTGID,
		SilenceDuration:  silenceDuration,
		UserCooldown:     userCooldown,
		GroupCooldown:    groupCooldown,
		DatabasePath:     dbPath,
	}, nil
}
