package bot

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"youtubych/internal/config"
	"youtubych/internal/database"
	"youtubych/internal/gemini"
)

type BotHandler struct {
	bot       *tgbotapi.BotAPI
	cfg       *config.Config
	db        *database.DB
	gemini    *gemini.Client
	limiter   *RateLimiter
	silence   *SilenceManager
	startTime time.Time
}

func NewBotHandler(bot *tgbotapi.BotAPI, cfg *config.Config, db *database.DB, geminiClient *gemini.Client, limiter *RateLimiter, silence *SilenceManager) *BotHandler {
	return &BotHandler{
		bot:       bot,
		cfg:       cfg,
		db:        db,
		gemini:    geminiClient,
		limiter:   limiter,
		silence:   silence,
		startTime: time.Now(),
	}
}

// HandleUpdate routes and processes incoming Telegram updates securely.
func (h *BotHandler) HandleUpdate(update tgbotapi.Update) {
	if update.Message == nil {
		return
	}

	msg := update.Message
	chatID := msg.Chat.ID
	fromID := msg.From.ID

	// 1. SECURITY: Private Chat Isolation
	if msg.Chat.IsPrivate() {
		if fromID != h.cfg.OwnerTGID {
			log.Printf("[Security] Unauthorized DM from user %d (@%s). Ignoring.", fromID, msg.From.UserName)
			reply := tgbotapi.NewMessage(chatID, "Привет! Я Ютубыч. Я работаю только в закрытом чате YouTube-сообщества. Личные сообщения доступны только владельцу.")
			_, _ = h.bot.Send(reply)
			return
		}
		h.handleOwnerPrivateMessage(msg)
		return
	}

	// 2. SECURITY: Group Chat Isolation
	if msg.Chat.IsGroup() || msg.Chat.IsSuperGroup() {
		if chatID != h.cfg.AllowedChatID {
			log.Printf("[Security Alert] Bot received message in unauthorized group %d (%s). Leaving chat...", chatID, msg.Chat.Title)
			leaveCfg := tgbotapi.LeaveChatConfig{ChatID: chatID}
			_, _ = h.bot.Request(leaveCfg)
			return
		}
		h.handleAllowedGroupMessage(msg)
		return
	}
}

// handleAllowedGroupMessage processes messages from the authorized group chat.
func (h *BotHandler) handleAllowedGroupMessage(msg *tgbotapi.Message) {
	// Always record activity for the 6-hour silence breaker
	h.silence.RecordActivity()
	_ = h.db.IncrementStat("total_chat_messages")

	// Upsert basic user record
	user, err := h.db.SaveOrUpdateUser(msg.From.ID, msg.From.UserName, msg.From.FirstName)
	if err != nil {
		log.Printf("[DB Error] Failed to update user %d: %v", msg.From.ID, err)
	}

	text := strings.TrimSpace(msg.Text)
	if text == "" {
		return
	}

	// Async: check if user mentioned their channel info to learn
	MaybeExtractUserInfo(h.gemini, h.db, msg.From.ID, text)

	lowerText := strings.ToLower(text)
	botUsername := strings.ToLower(h.bot.Self.UserName)

	isDirectAddress := strings.Contains(lowerText, "ютубыч") ||
		strings.Contains(lowerText, "@"+botUsername) ||
		(msg.ReplyToMessage != nil && msg.ReplyToMessage.From.ID == h.bot.Self.ID)

	// CASE 1: Direct Address to YouTubych
	if isDirectAddress {
		if !h.limiter.CanRespond(msg.From.ID) {
			log.Printf("[RateLimit] Throttling response to user %d in group", msg.From.ID)
			return
		}

		h.respondToDirectAddress(msg, user, text)
		return
	}

	// CASE 2: Ambient question in the group
	if strings.HasSuffix(text, "?") || strings.Contains(lowerText, "как ") || strings.Contains(lowerText, "почему ") || strings.Contains(lowerText, "кто знает") {
		// Ambient answers only if group cooldown allows
		if !h.limiter.CanRespond(msg.From.ID) {
			return
		}
		h.tryAnswerAmbientQuestion(msg, text)
	}
}

func (h *BotHandler) respondToDirectAddress(msg *tgbotapi.Message, user *database.UserProfile, text string) {
	// Send typing action to make response feel natural
	typing := tgbotapi.NewChatAction(msg.Chat.ID, tgbotapi.ChatTyping)
	_, _ = h.bot.Send(typing)

	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()

	// Load learned facts
	facts, _ := h.db.GetFacts(5)

	prompt := gemini.BuildUserPrompt(user, text, facts)

	// Query Gemini with Google Search Grounding enabled
	replyText, err := h.gemini.GenerateAnswer(ctx, gemini.BaseSystemPrompt, prompt, true)
	if err != nil {
		log.Printf("[Gemini Error / Quota] %v", err)
		// Smooth in-character deflection without leaking API errors or quota limits
		replyText = "Слушай, прямо сейчас мои поисковые радары немного барахлят, чтобы не выдумывать — врать не буду, свежей инфы нет под рукой. Кто в чате уже сталкивался, подскажите человеку!"
	}

	replyMsg := tgbotapi.NewMessage(msg.Chat.ID, replyText)
	replyMsg.ReplyToMessageID = msg.MessageID
	if _, sendErr := h.bot.Send(replyMsg); sendErr == nil {
		h.limiter.RecordResponse(msg.From.ID)
		_ = h.db.IncrementStat("direct_replies_sent")
	}
}

func (h *BotHandler) tryAnswerAmbientQuestion(msg *tgbotapi.Message, text string) {
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()

	prompt := gemini.BuildAmbientQuestionPrompt(text)
	replyText, err := h.gemini.GenerateAnswer(ctx, gemini.BaseSystemPrompt, prompt, false)
	if err != nil {
		return // Silent on error for ambient questions
	}

	trimmed := strings.TrimSpace(replyText)
	if trimmed == "[SKIP]" || strings.HasPrefix(trimmed, "[SKIP]") || trimmed == "" {
		return // Bot decides not to intervene
	}

	// Send typing action
	typing := tgbotapi.NewChatAction(msg.Chat.ID, tgbotapi.ChatTyping)
	_, _ = h.bot.Send(typing)

	replyMsg := tgbotapi.NewMessage(msg.Chat.ID, trimmed)
	replyMsg.ReplyToMessageID = msg.MessageID
	if _, sendErr := h.bot.Send(replyMsg); sendErr == nil {
		h.limiter.RecordResponse(msg.From.ID)
		_ = h.db.IncrementStat("ambient_replies_sent")
	}
}

// handleOwnerPrivateMessage handles admin and setup commands from the bot owner in private chat.
func (h *BotHandler) handleOwnerPrivateMessage(msg *tgbotapi.Message) {
	text := strings.TrimSpace(msg.Text)
	args := strings.Fields(text)
	command := ""
	if len(args) > 0 {
		command = args[0]
	}

	switch command {
	case "/start", "/help":
		helpText := `🤖 <b>Панель управления Ютубычем</b>

Привет, шеф! Я на связи. Вот доступные команды:
• <code>/status</code> — Статус бота, время работы и статистика
• <code>/topic</code> — Принудительно сгенерировать и отправить тему в чат
• <code>/users</code> — Список запомненных ютуберов и их каналов
• <code>/learn &lt;факт&gt;</code> — Добавить факт или правило в базу знаний
• <code>/facts</code> — Список последних добавленных фактов
• <code>/search &lt;запрос&gt;</code> — Проверить поиск Google в реальном времени`
		reply := tgbotapi.NewMessage(msg.Chat.ID, helpText)
		reply.ParseMode = "HTML"
		_, _ = h.bot.Send(reply)

	case "/status":
		uptime := time.Since(h.startTime).Round(time.Second)
		lastAct := h.silence.GetLastActivity().Format("15:04:05 02.01.2006")
		totalMsgs, _ := h.db.GetStat("total_chat_messages")
		directReps, _ := h.db.GetStat("direct_replies_sent")
		ambientReps, _ := h.db.GetStat("ambient_replies_sent")
		silenceSent, _ := h.db.GetStat("silence_breakers_sent")

		statusText := fmt.Sprintf(`📊 <b>Статус Ютубыча</b>

⏱ <b>Аптайм:</b> %v
💬 <b>Разрешенный чат:</b> <code>%d</code>
🕒 <b>Последняя активность в чате:</b> %s
💤 <b>Таймаут затишья:</b> %v

📈 <b>Статистика:</b>
• Сообщений в чате обработано: %d
• Прямых ответов: %d
• Ответов на вопросы: %d
• Вброшено тем при затишье: %d`,
			uptime, h.cfg.AllowedChatID, lastAct, h.cfg.SilenceDuration,
			totalMsgs, directReps, ambientReps, silenceSent)

		reply := tgbotapi.NewMessage(msg.Chat.ID, statusText)
		reply.ParseMode = "HTML"
		_, _ = h.bot.Send(reply)

	case "/topic":
		reply := tgbotapi.NewMessage(msg.Chat.ID, "Генерирую и отправляю тему в группу...")
		_, _ = h.bot.Send(reply)
		if err := h.silence.SendIcebreakerTopic(); err != nil {
			errReply := tgbotapi.NewMessage(msg.Chat.ID, fmt.Sprintf("Ошибка отправки темы: %v", err))
			_, _ = h.bot.Send(errReply)
		} else {
			doneReply := tgbotapi.NewMessage(msg.Chat.ID, "✅ Тема успешно отправлена в чат!")
			_, _ = h.bot.Send(doneReply)
		}

	case "/users":
		users, err := h.db.GetAllUsers(25)
		if err != nil || len(users) == 0 {
			reply := tgbotapi.NewMessage(msg.Chat.ID, "Пока нет сохраненных авторов каналов в базе.")
			_, _ = h.bot.Send(reply)
			return
		}
		var sb strings.Builder
		sb.WriteString("👥 <b>Запомненные креаторы:</b>\n\n")
		for _, u := range users {
			sb.WriteString(fmt.Sprintf("• <b>%s</b> (@%s, ID: <code>%d</code>)\n", u.FirstName, u.Username, u.UserID))
			if u.ChannelName != "" {
				sb.WriteString(fmt.Sprintf("  📺 Канал: %s\n", u.ChannelName))
			}
			if u.ChannelTheme != "" {
				sb.WriteString(fmt.Sprintf("  🎯 Ниша: %s\n", u.ChannelTheme))
			}
			if u.Subscribers != "" {
				sb.WriteString(fmt.Sprintf("  👥 Сабов: %s\n", u.Subscribers))
			}
			if u.Notes != "" {
				sb.WriteString(fmt.Sprintf("  📝 Заметка: %s\n", u.Notes))
			}
			sb.WriteString("\n")
		}
		reply := tgbotapi.NewMessage(msg.Chat.ID, sb.String())
		reply.ParseMode = "HTML"
		_, _ = h.bot.Send(reply)

	case "/learn":
		factText := strings.TrimSpace(strings.TrimPrefix(text, "/learn"))
		if factText == "" {
			reply := tgbotapi.NewMessage(msg.Chat.ID, "Использование: <code>/learn &lt;правило или факт&gt;</code>\nПример: <code>/learn Для Shorts сейчас важен показатель Viewed vs Swiped away выше 70%</code>")
			reply.ParseMode = "HTML"
			_, _ = h.bot.Send(reply)
			return
		}
		if err := h.db.AddFact("YouTube", factText, msg.From.ID); err != nil {
			reply := tgbotapi.NewMessage(msg.Chat.ID, fmt.Sprintf("Ошибка сохранения: %v", err))
			_, _ = h.bot.Send(reply)
		} else {
			reply := tgbotapi.NewMessage(msg.Chat.ID, "✅ Факт успешно сохранен в базу знаний Ютубыча!")
			_, _ = h.bot.Send(reply)
		}

	case "/facts":
		facts, err := h.db.GetFacts(15)
		if err != nil || len(facts) == 0 {
			reply := tgbotapi.NewMessage(msg.Chat.ID, "База знаний пока пуста.")
			_, _ = h.bot.Send(reply)
			return
		}
		var sb strings.Builder
		sb.WriteString("📚 <b>База знаний Ютубыча:</b>\n\n")
		for _, f := range facts {
			sb.WriteString(fmt.Sprintf("• [%s] %s\n", f.CreatedAt.Format("02.01"), f.Fact))
		}
		reply := tgbotapi.NewMessage(msg.Chat.ID, sb.String())
		reply.ParseMode = "HTML"
		_, _ = h.bot.Send(reply)

	case "/search":
		query := strings.TrimSpace(strings.TrimPrefix(text, "/search"))
		if query == "" {
			reply := tgbotapi.NewMessage(msg.Chat.ID, "Использование: <code>/search &lt;запрос&gt;</code>")
			reply.ParseMode = "HTML"
			_, _ = h.bot.Send(reply)
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		ans, err := h.gemini.GenerateAnswer(ctx, "Ответь кратко по фактам, используя актуальный поиск.", query, true)
		if err != nil {
			reply := tgbotapi.NewMessage(msg.Chat.ID, fmt.Sprintf("Ошибка поиска: %v", err))
			_, _ = h.bot.Send(reply)
			return
		}
		reply := tgbotapi.NewMessage(msg.Chat.ID, ans)
		_, _ = h.bot.Send(reply)

	default:
		reply := tgbotapi.NewMessage(msg.Chat.ID, "Неизвестная команда. Введите <code>/help</code> для списка команд.")
		reply.ParseMode = "HTML"
		_, _ = h.bot.Send(reply)
	}
}
