package main

import (
	"log"

	"youtubych/internal/bot"
	"youtubych/internal/config"
)

func main() {
	log.Println("--- Starting YouTubych Telegram Bot ---")

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("[Config Error] Failed to load configuration: %v\nCheck your .env file or environment variables.", err)
	}

	app, err := bot.NewApp(cfg)
	if err != nil {
		log.Fatalf("[Init Error] Failed to initialize application: %v", err)
	}

	if err := app.Run(); err != nil {
		log.Fatalf("[Runtime Error] Application stopped with error: %v", err)
	}

	log.Println("--- YouTubych stopped safely ---")
}
