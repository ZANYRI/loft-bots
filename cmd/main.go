package main

import (
	"context"
	"fmt"
	"loft-bots/internal/logger"
	"net/http"
	"os"
	"os/signal"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/joho/godotenv"
	"gorm.io/gorm"

	"loft-bots/internal/bot/admin"
	"loft-bots/internal/bot/client"
	"loft-bots/internal/db"
	"loft-bots/internal/metrics"
	"loft-bots/internal/notify"
	"loft-bots/internal/repository"
	"loft-bots/internal/state"
	"loft-bots/internal/web"
)

var numRx = regexp.MustCompile(`\d+`)

const (
	telegramPollTimeout = 25 * time.Second
	telegramHTTPTimeout = 35 * time.Second
)

type App struct {
	gormDB          *gorm.DB
	userRepo        *repository.UserRepo
	eventRepo       *repository.EventRepo
	orderRepo       *repository.OrderRepo
	reservationRepo *repository.ReservationRepo
	settingsRepo    *repository.SettingsRepo
	rentalPriceRepo *repository.RentalPriceRepo
	menuCatRepo     *repository.MenuCategoryRepo
	menuItemRepo    *repository.MenuItemRepo
	expenseRepo     *repository.ExpenseRepo
	discountRepo    *repository.DiscountRepo
	reviewRepo      *repository.ReviewRepo
	fsm             *state.FSM
	spamMu          sync.Mutex
	spamStates      map[int64]*clientSpamState

	clientMenuHandler        *client.MenuHandler
	clientPosterHandler      *client.PosterHandler
	clientReservationHandler *client.ReservationHandler
	clientPaymentHandler     *client.PaymentHandler
	clientProfileHandler     *client.ProfileHandler

	adminOrdersHandler   *admin.OrdersHandler
	adminPosterHandler   *admin.PosterHandler
	adminMenuHandler     *admin.MenuHandler
	adminPricingHandler  *admin.PricingHandler
	adminScheduleHandler *admin.ScheduleHandler
	adminStatsHandler    *admin.StatsHandler
	adminSettingsHandler *admin.SettingsHandler
}

type clientSpamState struct {
	Actions      []time.Time
	BlockedUntil time.Time
	LastWarning  time.Time
}

func newTelegramBot(token, webhookSecret, botName string) (*bot.Bot, error) {
	options := []bot.Option{
		bot.WithHTTPClient(telegramPollTimeout, &http.Client{Timeout: telegramHTTPTimeout}),
		bot.WithMiddlewares(observabilityMiddleware(botName)),
	}
	if strings.TrimSpace(webhookSecret) != "" {
		options = append(options, bot.WithWebhookSecretToken(strings.TrimSpace(webhookSecret)))
	}
	return bot.New(token, options...)
}

// observabilityMiddleware logs every processed Telegram update and records
// it (plus any panic) in Prometheus metrics, tagged by bot name.
func observabilityMiddleware(botName string) bot.Middleware {
	return func(next bot.HandlerFunc) bot.HandlerFunc {
		return func(ctx context.Context, b *bot.Bot, update *models.Update) {
			start := time.Now()
			defer func() {
				if r := recover(); r != nil {
					metrics.BotErrorsTotal.WithLabelValues(botName).Inc()
					logger.Error("bot handler panic", "bot", botName, "update_id", update.ID, "panic", r)
					panic(r)
				}
			}()
			metrics.BotUpdatesTotal.WithLabelValues(botName).Inc()
			next(ctx, b, update)
			logger.Debug("bot update handled", "bot", botName, "update_id", update.ID, "duration_ms", time.Since(start).Milliseconds())
		}
	}
}

func main() {
	godotenv.Load()

	gormDB := db.Connect(db.GetDSN())

	app := &App{
		gormDB:     gormDB,
		spamStates: make(map[int64]*clientSpamState),
	}

	app.initRepositories(gormDB)
	app.fsm = state.NewFSM(gormDB)

	clientBotToken := os.Getenv("CLIENT_BOT_TOKEN")
	adminBotToken := os.Getenv("ADMIN_BOT_TOKEN")

	if clientBotToken == "" || adminBotToken == "" {
		logger.Fatal("CLIENT_BOT_TOKEN and ADMIN_BOT_TOKEN must be set")
	}

	telegramMode := strings.ToLower(strings.TrimSpace(os.Getenv("TELEGRAM_MODE")))
	if telegramMode == "" {
		telegramMode = "polling"
	}
	if telegramMode != "polling" && telegramMode != "webhook" {
		logger.Fatalf("unsupported TELEGRAM_MODE %q, expected polling or webhook", telegramMode)
	}
	logger.Printf("Telegram mode: %s", telegramMode)
	logger.Printf("Telegram polling configured: poll_timeout=%s http_timeout=%s", telegramPollTimeout, telegramHTTPTimeout)

	clientWebhookSecret := os.Getenv("CLIENT_WEBHOOK_SECRET")
	adminWebhookSecret := os.Getenv("ADMIN_WEBHOOK_SECRET")

	clientB, err := newTelegramBot(clientBotToken, clientWebhookSecret, "client")
	if err != nil {
		logger.Fatalf("failed to create client bot: %v", err)
	}

	adminB, err := newTelegramBot(adminBotToken, adminWebhookSecret, "admin")
	if err != nil {
		logger.Fatalf("failed to create admin bot: %v", err)
	}

	botMode := strings.ToLower(strings.TrimSpace(os.Getenv("BOT_MODE")))
	if botMode == "" {
		botMode = "all"
	}
	if botMode != "client" && botMode != "admin" && botMode != "all" {
		logger.Fatalf("unsupported BOT_MODE %q, expected client, admin or all", botMode)
	}

	if botMode == "client" || botMode == "all" {
		app.initClientHandlers(clientB, adminB, gormDB)
	}
	if botMode == "admin" || botMode == "all" {
		app.initAdminHandlers(adminB, clientB, gormDB)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	logger.Printf("Starting in %s mode...", botMode)
	if telegramMode == "webhook" {
		if botMode != "all" {
			logger.Fatal("TELEGRAM_MODE=webhook requires BOT_MODE=all so both bot webhooks are served by one HTTP server")
		}
		webAppURL := strings.TrimRight(strings.TrimSpace(os.Getenv("WEBAPP_URL")), "/")
		if webAppURL == "" {
			logger.Fatal("WEBAPP_URL must be set for TELEGRAM_MODE=webhook")
		}
		webPort := os.Getenv("WEBAPP_PORT")
		if webPort == "" {
			webPort = "8080"
		}

		go app.startReviewScheduler(ctx, clientB)
		go app.startReservationBalanceScheduler(ctx, clientB)
		go app.startReservationStartReminderScheduler(ctx, clientB)

		ws := app.newWebServer(webPort, adminBotToken, clientB, adminB)
		clientWebhookPath := "/telegram/webhook/client"
		adminWebhookPath := "/telegram/webhook/admin"
		ws.Handle(clientWebhookPath, clientB.WebhookHandler())
		ws.Handle(adminWebhookPath, adminB.WebhookHandler())

		setupWebhook(ctx, clientB, "client", webAppURL+clientWebhookPath, clientWebhookSecret)
		setupWebhook(ctx, adminB, "admin", webAppURL+adminWebhookPath, adminWebhookSecret)
		go clientB.StartWebhook(ctx)
		go adminB.StartWebhook(ctx)
		go func() {
			if err := ws.Start(); err != nil {
				logger.Printf("web server error: %v", err)
			}
		}()
	} else {
		if botMode == "client" || botMode == "all" {
			deleteWebhook(ctx, clientB, "client")
			go app.startReviewScheduler(ctx, clientB)
			go app.startReservationBalanceScheduler(ctx, clientB)
			go app.startReservationStartReminderScheduler(ctx, clientB)
			go clientB.Start(ctx)
		}
		if botMode == "admin" || botMode == "all" {
			deleteWebhook(ctx, adminB, "admin")
			go adminB.Start(ctx)

			webPort := os.Getenv("WEBAPP_PORT")
			if webPort == "" {
				webPort = "8080"
			}

			ws := app.newWebServer(webPort, adminBotToken, clientB, adminB)
			go func() {
				if err := ws.Start(); err != nil {
					logger.Printf("web server error: %v", err)
				}
			}()
		}
	}

	<-ctx.Done()
	logger.Println("Shutting down...")
}

func (app *App) newWebServer(webPort, adminBotToken string, clientB, adminB *bot.Bot) *web.Server {
	return web.NewServer(
		webPort, adminBotToken, clientB, adminB,
		app.userRepo, app.eventRepo, app.orderRepo,
		app.reservationRepo, app.settingsRepo,
		app.rentalPriceRepo, app.menuCatRepo, app.menuItemRepo,
		app.expenseRepo,
		app.discountRepo,
		app.reviewRepo,
	)
}

func setupWebhook(ctx context.Context, b *bot.Bot, name, webhookURL, secret string) {
	params := &bot.SetWebhookParams{URL: webhookURL}
	if strings.TrimSpace(secret) != "" {
		params.SecretToken = strings.TrimSpace(secret)
	}
	if _, err := b.SetWebhook(ctx, params); err != nil {
		logger.Fatalf("failed to set %s webhook: %v", name, err)
	}
	logger.Printf("%s webhook set: %s", name, webhookURL)
}

func deleteWebhook(ctx context.Context, b *bot.Bot, name string) {
	if _, err := b.DeleteWebhook(ctx, &bot.DeleteWebhookParams{}); err != nil {
		logger.Printf("failed to delete %s webhook before polling: %v", name, err)
	}
}

func (app *App) initRepositories(gormDB *gorm.DB) {
	app.userRepo = repository.NewUserRepo(gormDB)
	app.eventRepo = repository.NewEventRepo(gormDB)
	app.orderRepo = repository.NewOrderRepo(gormDB)
	app.reservationRepo = repository.NewReservationRepo(gormDB)
	app.settingsRepo = repository.NewSettingsRepo(gormDB)
	app.rentalPriceRepo = repository.NewRentalPriceRepo(gormDB)
	app.menuCatRepo = repository.NewMenuCategoryRepo(gormDB)
	app.menuItemRepo = repository.NewMenuItemRepo(gormDB)
	app.expenseRepo = repository.NewExpenseRepo(gormDB)
	app.discountRepo = repository.NewDiscountRepo(gormDB)
	app.reviewRepo = repository.NewReviewRepo(gormDB)
}

func (app *App) initClientHandlers(b *bot.Bot, adminB *bot.Bot, gormDB *gorm.DB) {
	app.clientMenuHandler = client.NewMenuHandler(app.menuItemRepo, app.menuCatRepo, app.settingsRepo, app.fsm, app.userRepo, app.eventRepo, app.reservationRepo)
	app.clientPosterHandler = client.NewPosterHandler(app.eventRepo)
	app.clientReservationHandler = client.NewReservationHandler(app.reservationRepo, app.rentalPriceRepo, app.userRepo, app.fsm)
	app.clientPaymentHandler = client.NewPaymentHandler(app.orderRepo, app.menuItemRepo, app.settingsRepo, app.userRepo, app.eventRepo, app.reservationRepo, app.fsm, adminB)
	app.clientProfileHandler = client.NewProfileHandler(app.userRepo, app.orderRepo)

	b.RegisterHandlerMatchFunc(func(update *models.Update) bool { return update.Message != nil }, app.handleClientMessage)
	b.RegisterHandler(bot.HandlerTypeCallbackQueryData, "", bot.MatchTypePrefix, app.handleClientCallback)
	b.RegisterHandlerMatchFunc(func(update *models.Update) bool {
		return update.Message != nil && (len(update.Message.Photo) > 0 || update.Message.Document != nil)
	}, app.handleClientPhoto)
}

func (app *App) handleClientMessage(ctx context.Context, b *bot.Bot, update *models.Update) {
	if update.Message == nil {
		return
	}
	deleteRepliedBotMessage(ctx, b, update.Message)
	logger.Printf("client message: telegram_id=%d text=%q photos=%d document=%t", update.Message.From.ID, update.Message.Text, len(update.Message.Photo), update.Message.Document != nil)
	if update.Message.Text == "/start" {
		if app.isClientSpamBlocked(ctx, b, update.Message.Chat.ID, update.Message.From.ID) {
			return
		}
		app.handleClientStart(ctx, b, update)
		return
	}
	if len(update.Message.Photo) > 0 || update.Message.Document != nil {
		app.handleClientPhoto(ctx, b, update)
		return
	}
	if strings.TrimSpace(update.Message.Text) != "" {
		if app.isClientSpamBlocked(ctx, b, update.Message.Chat.ID, update.Message.From.ID) {
			return
		}
		app.handleClientText(ctx, b, update)
	}
}

func deleteRepliedBotMessage(ctx context.Context, b *bot.Bot, message *models.Message) {
	if message == nil || message.ReplyToMessage == nil || message.ReplyToMessage.From == nil || !message.ReplyToMessage.From.IsBot {
		return
	}
	if _, err := b.DeleteMessage(ctx, &bot.DeleteMessageParams{ChatID: message.Chat.ID, MessageID: message.ReplyToMessage.ID}); err != nil {
		logger.Printf("failed to delete replied bot message: chat_id=%d message_id=%d err=%v", message.Chat.ID, message.ReplyToMessage.ID, err)
	}
}

func (app *App) handleClientPhoto(ctx context.Context, b *bot.Bot, update *models.Update) {
	if update.Message == nil {
		return
	}
	telegramID := update.Message.From.ID
	if app.isClientSpamBlocked(ctx, b, update.Message.Chat.ID, telegramID) {
		return
	}
	if !app.fsm.IsInState(telegramID, "client", "payment:receipt") {
		return
	}
	if len(update.Message.Photo) > 0 {
		photo := update.Message.Photo[len(update.Message.Photo)-1]
		app.clientPaymentHandler.HandleReceipt(ctx, b, update.Message.Chat.ID, telegramID, photo.FileID, false)
		return
	}
	if update.Message.Document != nil {
		app.clientPaymentHandler.HandleReceipt(ctx, b, update.Message.Chat.ID, telegramID, update.Message.Document.FileID, true)
	}
}

func (app *App) handleClientText(ctx context.Context, b *bot.Bot, update *models.Update) {
	if update.Message == nil || update.Message.Text == "" || update.Message.Text == "/start" {
		return
	}
	telegramID := update.Message.From.ID
	currentState, data, err := app.fsm.GetState(telegramID, "client")
	if err == nil && currentState == "review:text" {
		app.handleReviewText(ctx, b, update, data)
		return
	}
	logger.Printf("ticket input state: telegram_id=%d state=%q err=%v data=%v", telegramID, currentState, err, data)

	if err == nil && currentState == "payment:phone" {
		phone := strings.TrimSpace(update.Message.Text)
		data["custom_payment_phone"] = phone
		if err := app.fsm.SetState(telegramID, "client", "payment:receipt", data); err != nil {
			logger.Printf("failed to set payment receipt state: %v", err)
		}
		if _, err := b.DeleteMessage(ctx, &bot.DeleteMessageParams{ChatID: update.Message.Chat.ID, MessageID: update.Message.ID}); err != nil {
			logger.Printf("failed to delete user phone message: %v", err)
		}
		app.clientPaymentHandler.ShowPayment(ctx, b, update.Message.Chat.ID, telegramID)
		return
	}

	if err != nil || currentState != "ticket:quantity" {
		return
	}
	eventID, _ := data["event_id"].(float64)
	event, eventErr := app.eventRepo.GetByID(uint(eventID))
	logger.Printf("ticket input event: telegram_id=%d event_id=%d event_err=%v", telegramID, uint(eventID), eventErr)

	match := numRx.FindString(update.Message.Text)
	if match == "" {
		max := 0
		if event != nil {
			max = event.PlacesLeft
		}
		b.SendMessage(ctx, &bot.SendMessageParams{ChatID: update.Message.Chat.ID, Text: fmt.Sprintf("Не удалось распознать число. Введите количество билетов от 1 до %d.", max)})
		return
	}
	quantity, err := strconv.Atoi(match)
	logger.Printf("ticket input parsed: telegram_id=%d raw=%q match=%q quantity=%d err=%v", telegramID, update.Message.Text, match, quantity, err)
	if err != nil || eventErr != nil || event == nil || quantity < 1 || quantity > event.PlacesLeft {
		max := 0
		if event != nil {
			max = event.PlacesLeft
		}
		b.SendMessage(ctx, &bot.SendMessageParams{ChatID: update.Message.Chat.ID, Text: fmt.Sprintf("Введите количество билетов от 1 до %d.", max)})
		return
	}
	if err := app.fsm.UpdateData(telegramID, "client", map[string]interface{}{"ticket_quantity": quantity}); err != nil {
		logger.Printf("ticket input state update failed: telegram_id=%d err=%v", telegramID, err)
		client.SendErrorMessage(ctx, b, update.Message.Chat.ID)
		return
	}
	logger.Printf("ticket input accepted: telegram_id=%d quantity=%d", telegramID, quantity)
	if messageID, ok := data["ui_message_id"].(float64); ok && messageID > 0 {
		_, _ = b.DeleteMessage(ctx, &bot.DeleteMessageParams{ChatID: update.Message.Chat.ID, MessageID: int(messageID)})
	}
	_, _ = b.DeleteMessage(ctx, &bot.DeleteMessageParams{ChatID: update.Message.Chat.ID, MessageID: update.Message.ID})
	_ = app.fsm.DeleteData(telegramID, "client", "ui_message_id")
	app.clientReservationHandler.PromptMenuOrPayment(ctx, b, update.Message.Chat.ID, telegramID)
}

func (app *App) handleReviewText(ctx context.Context, b *bot.Bot, update *models.Update, data map[string]interface{}) {
	telegramID := update.Message.From.ID
	user, err := app.userRepo.FindOrCreate(telegramID, update.Message.From.Username)
	if err != nil {
		logger.Printf("failed to find review user: %v", err)
		client.SendErrorMessage(ctx, b, update.Message.Chat.ID)
		return
	}
	rating, _ := data["rating"].(float64)
	orderIDFloat, hasOrder := data["order_id"].(float64)
	var orderID *uint
	if hasOrder && orderIDFloat > 0 {
		id := uint(orderIDFloat)
		orderID = &id
	}
	if int(rating) < 1 || int(rating) > 5 {
		b.SendMessage(ctx, &bot.SendMessageParams{ChatID: update.Message.Chat.ID, Text: "Оценка не найдена. Пожалуйста, выберите оценку ещё раз."})
		app.fsm.ClearState(telegramID, "client")
		return
	}
	if err := app.reviewRepo.Create(&db.Review{UserID: user.ID, OrderID: orderID, Rating: int(rating), Text: strings.TrimSpace(update.Message.Text)}); err != nil {
		logger.Printf("failed to save review: %v", err)
		client.SendErrorMessage(ctx, b, update.Message.Chat.ID)
		return
	}
	app.fsm.ClearState(telegramID, "client")
	thanksPrefix := "message_review_thanks_reservation"
	fallbackThanks := "Спасибо за честный отзыв. Мы учтём замечания и постараемся улучшить сервис."
	if orderID != nil {
		if order, err := app.orderRepo.GetByID(*orderID); err == nil && order.EventID != nil {
			thanksPrefix = "message_review_thanks_event"
			fallbackThanks = "Спасибо за честный отзыв. Мы учтём замечания и постараемся сделать следующие мероприятия лучше."
		}
	}
	thanksKey := thanksPrefix + "_low"
	if int(rating) == 5 {
		thanksKey = thanksPrefix + "_5"
		fallbackThanks = "Спасибо за отличную оценку! Очень рады, что вам понравилось."
	}
	b.SendMessage(ctx, &bot.SendMessageParams{ChatID: update.Message.Chat.ID, Text: app.settingValue(thanksKey, fallbackThanks)})
}

func (app *App) initAdminHandlers(b *bot.Bot, clientB *bot.Bot, gormDB *gorm.DB) {
	app.adminOrdersHandler = admin.NewOrdersHandler(app.orderRepo, app.eventRepo, app.reservationRepo, app.settingsRepo, clientB)
	app.adminPosterHandler = admin.NewPosterHandler(app.eventRepo, app.fsm)
	app.adminMenuHandler = admin.NewMenuHandler(app.menuItemRepo, app.menuCatRepo, app.fsm)
	app.adminPricingHandler = admin.NewPricingHandler(app.rentalPriceRepo, app.fsm)
	app.adminScheduleHandler = admin.NewScheduleHandler(app.reservationRepo, app.orderRepo, app.settingsRepo, clientB)
	app.adminStatsHandler = admin.NewStatsHandler(app.orderRepo, app.reservationRepo)
	app.adminSettingsHandler = admin.NewSettingsHandler(app.settingsRepo, app.fsm)

	b.RegisterHandler(bot.HandlerTypeMessageText, "/start", bot.MatchTypeExact, app.handleAdminStart)
	b.RegisterHandlerMatchFunc(func(update *models.Update) bool {
		return update.Message != nil && strings.TrimSpace(update.Message.Text) != ""
	}, app.handleAdminMessage)
	b.RegisterHandlerMatchFunc(func(update *models.Update) bool {
		return update.Message != nil && len(update.Message.Photo) > 0
	}, app.handleAdminPhoto)
	b.RegisterHandler(bot.HandlerTypeCallbackQueryData, "", bot.MatchTypePrefix, app.handleAdminCallback)
}

func answerCallback(ctx context.Context, b *bot.Bot, update *models.Update) {
	if update.CallbackQuery != nil {
		b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{
			CallbackQueryID: update.CallbackQuery.ID,
		})
	}
}

func (app *App) handleClientStart(ctx context.Context, b *bot.Bot, update *models.Update) {
	chatID := update.Message.Chat.ID
	telegramID := update.Message.From.ID
	username := update.Message.From.Username

	if _, err := app.userRepo.FindOrCreate(telegramID, username); err != nil {
		logger.Printf("failed to create/find client user: telegram_id=%d err=%v", telegramID, err)
	}
	app.showClientMainMenu(ctx, b, chatID)
}

func (app *App) showClientMainMenu(ctx context.Context, b *bot.Bot, chatID int64) {
	description := app.settingValue("bot_start_description", "Выберите раздел:")
	avatarURL := app.settingValue("bot_start_avatar", "")
	siteURL := strings.TrimSpace(app.settingValue("site_url", ""))
	instagramURL := strings.TrimSpace(app.settingValue("instagram_url", ""))
	text := strings.TrimSpace(description)
	if text == "" {
		text = "Выберите раздел:"
	}

	keyboard := [][]models.InlineKeyboardButton{
		{
			{Text: "\U0001F3AD Афиша", CallbackData: "poster_show"},
		},
		{
			{Text: "\U0001F511 Забронировать лофт", CallbackData: "reservation_start"},
		},
		{
			{Text: "🍽 Меню", CallbackData: "menu_categories"},
		},
		{
			{Text: "✨ Доп. услуги", CallbackData: "service_categories"},
		},
		{
			{Text: "\U0001F6D2 Корзина", CallbackData: "menu_cart"},
		},
		{
			{Text: "\U0001F464 Мой профиль", CallbackData: "profile_show"},
		},
		{
			{Text: "📣 Наш канал", URL: "https://t.me/loft_shumno"},
		},
	}
	if siteURL != "" {
		keyboard = append(keyboard, []models.InlineKeyboardButton{{Text: "🌐 Сайт", URL: siteURL}})
	} else {
		keyboard = append(keyboard, []models.InlineKeyboardButton{{Text: "🌐 Сайт", CallbackData: "site_open"}})
	}
	if instagramURL != "" {
		keyboard = append(keyboard, []models.InlineKeyboardButton{{Text: "📸 Instagram", URL: instagramURL}})
	}
	keyboard = append(keyboard, []models.InlineKeyboardButton{{Text: "📶 Wi-Fi", CallbackData: "wifi_info"}})

	replyMarkup := &models.InlineKeyboardMarkup{InlineKeyboard: keyboard}
	if strings.TrimSpace(avatarURL) != "" {
		if _, err := b.SendPhoto(ctx, &bot.SendPhotoParams{
			ChatID:      chatID,
			Photo:       &models.InputFileString{Data: avatarURL},
			Caption:     text,
			ReplyMarkup: replyMarkup,
		}); err == nil {
			return
		} else {
			logger.Printf("failed to send start avatar: %v", err)
		}
	}

	if _, err := b.SendMessage(ctx, &bot.SendMessageParams{ChatID: chatID, Text: text, ReplyMarkup: replyMarkup}); err != nil {
		logger.Printf("failed to send client main menu: chat_id=%d err=%v", chatID, err)
	}
}

func (app *App) settingValue(key, fallback string) string {
	setting, err := app.settingsRepo.Get(key)
	if err != nil || setting == nil || strings.TrimSpace(setting.Value) == "" {
		return fallback
	}
	return setting.Value
}

func (app *App) settingInt(key string, fallback int) int {
	value := strings.TrimSpace(app.settingValue(key, ""))
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < 0 {
		return fallback
	}
	return parsed
}

func (app *App) isClientSpamBlocked(ctx context.Context, b *bot.Bot, chatID int64, telegramID int64) bool {
	windowSeconds := app.settingInt("client_spam_window_seconds", 10)
	maxActions := app.settingInt("client_spam_max_actions", 8)
	blockSeconds := app.settingInt("client_spam_block_seconds", 30)
	if windowSeconds <= 0 || maxActions <= 0 || blockSeconds <= 0 {
		return false
	}

	now := time.Now()
	window := time.Duration(windowSeconds) * time.Second
	blockDuration := time.Duration(blockSeconds) * time.Second

	app.spamMu.Lock()
	state := app.spamStates[telegramID]
	if state == nil {
		state = &clientSpamState{}
		app.spamStates[telegramID] = state
	}

	blocked := now.Before(state.BlockedUntil)
	warn := false
	remainingSeconds := 0
	if blocked {
		warn = now.Sub(state.LastWarning) >= 5*time.Second
		if warn {
			state.LastWarning = now
			remainingSeconds = int(time.Until(state.BlockedUntil).Seconds()) + 1
		}
		app.spamMu.Unlock()
		if warn {
			b.SendMessage(ctx, &bot.SendMessageParams{ChatID: chatID, Text: fmt.Sprintf("Слишком много действий подряд. Попробуйте через %d сек.", remainingSeconds)})
		}
		return true
	}

	cutoff := now.Add(-window)
	kept := state.Actions[:0]
	for _, actionAt := range state.Actions {
		if actionAt.After(cutoff) {
			kept = append(kept, actionAt)
		}
	}
	state.Actions = append(kept, now)
	if len(state.Actions) > maxActions {
		state.Actions = nil
		state.BlockedUntil = now.Add(blockDuration)
		state.LastWarning = now
		app.spamMu.Unlock()
		b.SendMessage(ctx, &bot.SendMessageParams{ChatID: chatID, Text: fmt.Sprintf("Слишком много действий подряд. Бот временно ограничен на %d сек.", blockSeconds)})
		return true
	}
	app.spamMu.Unlock()
	return false
}

func (app *App) startReviewScheduler(ctx context.Context, b *bot.Bot) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()

	app.sendDueReviewRequests(ctx, b)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			app.sendDueReviewRequests(ctx, b)
		}
	}
}

func (app *App) sendDueReviewRequests(ctx context.Context, b *bot.Bot) {
	orders, err := app.orderRepo.GetDueReviewRequests(time.Now())
	if err != nil {
		logger.Printf("failed to get due review requests: %v", err)
		return
	}

	for _, order := range orders {
		messageKey := "message_review_request_reservation"
		if order.EventID != nil {
			messageKey = "message_review_request_event"
		}
		text := app.settingValue(messageKey, "Спасибо, что были у нас! Оцените, пожалуйста, ваш визит по 5-балльной шкале.")
		keyboard := [][]models.InlineKeyboardButton{{
			{Text: "1", CallbackData: fmt.Sprintf("review_rate_%d_1", order.ID)},
			{Text: "2", CallbackData: fmt.Sprintf("review_rate_%d_2", order.ID)},
			{Text: "3", CallbackData: fmt.Sprintf("review_rate_%d_3", order.ID)},
			{Text: "4", CallbackData: fmt.Sprintf("review_rate_%d_4", order.ID)},
			{Text: "5", CallbackData: fmt.Sprintf("review_rate_%d_5", order.ID)},
		}}

		_, sendErr := b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:      order.User.TelegramID,
			Text:        text,
			ReplyMarkup: &models.InlineKeyboardMarkup{InlineKeyboard: keyboard},
		})
		if sendErr != nil {
			logger.Printf("failed to send review request: order_id=%d telegram_id=%d err=%v", order.ID, order.User.TelegramID, sendErr)
		}
		if err := app.orderRepo.MarkReviewRequested(order.ID, time.Now()); err != nil {
			logger.Printf("failed to mark review request sent: order_id=%d err=%v", order.ID, err)
		}
	}
}

func (app *App) startReservationBalanceScheduler(ctx context.Context, b *bot.Bot) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()

	app.sendDueReservationBalanceMessages(ctx, b)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			app.sendDueReservationBalanceMessages(ctx, b)
		}
	}
}

func (app *App) sendDueReservationBalanceMessages(ctx context.Context, b *bot.Bot) {
	reservations, err := app.reservationRepo.GetDueBalanceReminders(time.Now())
	if err != nil {
		logger.Printf("failed to get due balance reminders: %v", err)
		return
	}
	for _, reservation := range reservations {
		paid, err := app.orderRepo.PrepaidByReservationID(reservation.ID)
		if err != nil {
			logger.Printf("failed to get prepaid amount: reservation_id=%d err=%v", reservation.ID, err)
			continue
		}
		remaining := reservation.TotalPrice - paid
		if remaining > 0 {
			message := app.settingValue("message_reservation_balance_due", "⏰ Ваша бронь началась. Осталось оплатить {amount} ₽ из общей суммы {total} ₽.")
			message = strings.ReplaceAll(message, "{amount}", fmt.Sprintf("%.0f", remaining))
			message = strings.ReplaceAll(message, "{total}", fmt.Sprintf("%.0f", reservation.TotalPrice))
			message = strings.ReplaceAll(message, "{paid}", fmt.Sprintf("%.0f", paid))
			message = strings.ReplaceAll(message, "{date}", reservation.Date.Format("02.01.2006"))
			message = strings.ReplaceAll(message, "{time_from}", reservation.TimeFrom)
			message = strings.ReplaceAll(message, "{time_to}", reservation.TimeTo)
			if _, err := b.SendMessage(ctx, &bot.SendMessageParams{ChatID: reservation.User.TelegramID, Text: message}); err != nil {
				logger.Printf("failed to send balance reminder: reservation_id=%d err=%v", reservation.ID, err)
				continue
			}
		}
		if err := app.reservationRepo.MarkBalanceReminderSent(reservation.ID, time.Now()); err != nil {
			logger.Printf("failed to mark balance reminder sent: reservation_id=%d err=%v", reservation.ID, err)
		}
	}
}

func (app *App) startReservationStartReminderScheduler(ctx context.Context, b *bot.Bot) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	app.sendDueReservationStartReminders(ctx, b, 60)
	app.sendDueReservationEndReminders(ctx, b, 30)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			app.sendDueReservationStartReminders(ctx, b, 60)
			app.sendDueReservationEndReminders(ctx, b, 30)
		}
	}
}

func (app *App) sendDueReservationStartReminders(ctx context.Context, b *bot.Bot, minutesBefore int) {
	reservations, err := app.reservationRepo.GetDueStartReminders(time.Now(), minutesBefore)
	if err != nil {
		logger.Printf("failed to get reservation reminders: minutes=%d err=%v", minutesBefore, err)
		return
	}
	for _, reservation := range reservations {
		message := app.settingValue("message_reservation_reminder_60", "⏰ До начала вашей брони остался 1 час. Ждём вас {date} в {time_from}.")
		message = strings.ReplaceAll(message, "{date}", reservation.Date.Format("02.01.2006"))
		message = strings.ReplaceAll(message, "{time_from}", reservation.TimeFrom)
		message = strings.ReplaceAll(message, "{time_to}", reservation.TimeTo)
		if _, err := b.SendMessage(ctx, &bot.SendMessageParams{ChatID: reservation.User.TelegramID, Text: message}); err != nil {
			logger.Printf("failed to send reservation reminder: reservation_id=%d err=%v", reservation.ID, err)
			continue
		}
		if err := app.reservationRepo.MarkStartReminderSent(reservation.ID, minutesBefore, time.Now()); err != nil {
			logger.Printf("failed to mark reservation reminder sent: reservation_id=%d err=%v", reservation.ID, err)
		}
	}
}

func (app *App) sendDueReservationEndReminders(ctx context.Context, b *bot.Bot, minutesBefore int) {
	reservations, err := app.reservationRepo.GetDueEndReminders(time.Now(), minutesBefore)
	if err != nil {
		logger.Printf("failed to get reservation end reminders: minutes=%d err=%v", minutesBefore, err)
		return
	}
	for _, reservation := range reservations {
		message := app.settingValue("message_reservation_reminder_30", "⏰ До конца вашей брони осталось 30 минут. Время окончания: {time_to}.")
		message = strings.ReplaceAll(message, "{date}", reservation.Date.Format("02.01.2006"))
		message = strings.ReplaceAll(message, "{time_from}", reservation.TimeFrom)
		message = strings.ReplaceAll(message, "{time_to}", reservation.TimeTo)
		if _, err := b.SendMessage(ctx, &bot.SendMessageParams{ChatID: reservation.User.TelegramID, Text: message}); err != nil {
			logger.Printf("failed to send reservation end reminder: reservation_id=%d err=%v", reservation.ID, err)
			continue
		}
		if err := app.reservationRepo.MarkEndReminderSent(reservation.ID, time.Now()); err != nil {
			logger.Printf("failed to mark reservation end reminder sent: reservation_id=%d err=%v", reservation.ID, err)
		}
	}
}

func (app *App) handleClientCallback(ctx context.Context, b *bot.Bot, update *models.Update) {
	if update.CallbackQuery == nil {
		return
	}
	defer answerCallback(ctx, b, update)

	data := update.CallbackQuery.Data
	chatID := getChatID(update)
	telegramID := update.CallbackQuery.From.ID
	if app.isClientSpamBlocked(ctx, b, chatID, telegramID) {
		return
	}
	if shouldReplaceClientMessage(data) {
		deleteClientCallbackMessage(ctx, b, update)
	}

	switch {
	case data == "main_menu":
		app.showClientMainMenu(ctx, b, chatID)

	case data == "site_open":
		b.SendMessage(ctx, &bot.SendMessageParams{ChatID: chatID, Text: "Ссылка на сайт пока не настроена."})

	case data == "wifi_info":
		wifiName := strings.TrimSpace(app.settingValue("wifi_name", ""))
		wifiPassword := strings.TrimSpace(app.settingValue("wifi_password", ""))
		if wifiName == "" && wifiPassword == "" {
			b.SendMessage(ctx, &bot.SendMessageParams{ChatID: chatID, Text: "Wi-Fi пока не настроен."})
			return
		}
		text := "📶 Wi-Fi"
		if wifiName != "" {
			text += "\nНазвание: " + wifiName
		}
		if wifiPassword != "" {
			text += "\nПароль: " + wifiPassword
		}
		kb := &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
			{{Text: "◀️ Назад", CallbackData: "main_menu"}},
		}}
		message, err := b.SendMessage(ctx, &bot.SendMessageParams{ChatID: chatID, Text: text, ReplyMarkup: kb})
		if err == nil && message != nil {
			_ = app.fsm.UpdateData(telegramID, "client", map[string]interface{}{"ui_message_id": float64(message.ID)})
		}

	case data == "poster_show":
		app.clientPosterHandler.ShowList(ctx, b, chatID)

	case strings.HasPrefix(data, "poster_"):
		parts := strings.Split(data, "_")
		if len(parts) >= 2 {
			index, _ := strconv.Atoi(parts[1])
			app.clientPosterHandler.ShowAt(ctx, b, chatID, index)
		}

	case strings.HasPrefix(data, "buy_ticket_"):
		parts := strings.Split(data, "_")
		if len(parts) >= 3 {
			eventID, _ := strconv.ParseUint(parts[2], 10, 64)
			app.showTicketQuantityPicker(ctx, b, chatID, telegramID, uint(eventID))
		}

	case data == "menu_categories":
		app.clientMenuHandler.ShowCategories(ctx, b, chatID, telegramID, "menu")

	case data == "service_categories":
		app.clientMenuHandler.ShowCategories(ctx, b, chatID, telegramID, "service")

	case strings.HasPrefix(data, "menu_cat_"):
		parts := strings.Split(data, "_")
		if len(parts) >= 3 {
			catID, _ := strconv.ParseUint(parts[2], 10, 64)
			app.clientMenuHandler.ShowCategoryItems(ctx, b, chatID, telegramID, uint(catID))
		}

	case strings.HasPrefix(data, "menu_add_"):
		parts := strings.Split(data, "_")
		if len(parts) >= 3 {
			itemID, _ := strconv.ParseUint(parts[2], 10, 64)
			item, err := app.menuItemRepo.GetByID(uint(itemID))
			if err != nil || item == nil || !app.canUseMenuType(ctx, b, chatID, telegramID, item.Category.Type) {
				return
			}
			user, _ := app.userRepo.GetByTelegramID(telegramID)
			if user != nil {
				app.clientMenuHandler.AddToCart(ctx, b, chatID, user.ID, uint(itemID), telegramID)
				b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: update.CallbackQuery.ID, Text: "Добавлено в корзину"})
			}
		}

	case strings.HasPrefix(data, "menu_remove_"):
		parts := strings.Split(data, "_")
		if len(parts) >= 3 {
			itemID, _ := strconv.ParseUint(parts[2], 10, 64)
			app.clientMenuHandler.RemoveFromCart(ctx, b, chatID, uint(itemID), telegramID)
		}

	case data == "menu_cart":
		app.clientMenuHandler.ShowCart(ctx, b, chatID, telegramID)

	case data == "cart_remove_ticket":
		app.clientMenuHandler.RemoveTicketFromCart(ctx, b, chatID, telegramID)

	case data == "cart_remove_reservation":
		app.clientMenuHandler.RemoveReservationFromCart(ctx, b, chatID, telegramID)

	case data == "menu_checkout":
		if !app.canUseMenu(ctx, b, chatID, telegramID) {
			return
		}
		app.clientPaymentHandler.ShowPayment(ctx, b, chatID, telegramID)

	case data == "reservation_start":
		app.clientReservationHandler.Start(ctx, b, chatID, telegramID)

	case strings.HasPrefix(data, "res_"):
		app.clientReservationHandler.HandleCallback(ctx, b, chatID, telegramID, data)

	case data == "reservation_confirm":
		if app.clientReservationHandler.Confirm(ctx, b, chatID, telegramID) {
			app.clientPaymentHandler.ShowPayment(ctx, b, chatID, telegramID)
		}

	case data == "go_to_payment":
		app.clientPaymentHandler.ShowPayment(ctx, b, chatID, telegramID)

	case data == "payment_custom_phone":
		_, data, _ := app.fsm.GetState(telegramID, "client")
		app.fsm.SetState(telegramID, "client", "payment:phone", data)
		b.SendMessage(ctx, &bot.SendMessageParams{ChatID: chatID, Text: "Отправьте номер телефона или реквизиты для оплаты одним сообщением."})

	case strings.HasPrefix(data, "payment_done_"):
		app.clientPaymentHandler.RequestReceipt(ctx, b, chatID, telegramID)

	case strings.HasPrefix(data, "review_rate_"):
		parts := strings.Split(data, "_")
		if len(parts) >= 4 {
			orderID, _ := strconv.ParseUint(parts[2], 10, 64)
			rating, _ := strconv.Atoi(parts[3])
			if rating >= 1 && rating <= 5 {
				app.fsm.SetState(telegramID, "client", "review:text", map[string]interface{}{"order_id": float64(orderID), "rating": float64(rating)})
				b.SendMessage(ctx, &bot.SendMessageParams{ChatID: chatID, Text: fmt.Sprintf("Спасибо! Ваша оценка: %d/5. Напишите, пожалуйста, текстовый отзыв одним сообщением.", rating)})
			}
		}

	case data == "profile_show":
		app.clientProfileHandler.Show(ctx, b, chatID, telegramID)

	case data == "noop":
	}
}

func (app *App) canUseMenu(ctx context.Context, b *bot.Bot, chatID int64, telegramID int64) bool {
	_, data, err := app.fsm.GetState(telegramID, "client")
	if err == nil {
		if _, ok := data["event_id"]; ok {
			return true
		}
		if _, ok := data["reservation_id"]; ok {
			return true
		}
	}

	user, err := app.userRepo.GetByTelegramID(telegramID)
	if err != nil || user == nil {
		b.SendMessage(ctx, &bot.SendMessageParams{ChatID: chatID, Text: "Чтобы заказать дополнительные услуги, сначала купите билет на активное мероприятие или забронируйте лофт."})
		return false
	}

	hasTicket, err := app.orderRepo.HasActiveEventTicket(user.ID)
	if err != nil {
		logger.Printf("failed to check event ticket: %v", err)
	}
	hasReservation, err := app.reservationRepo.HasUpcomingReservation(user.ID)
	if err != nil {
		logger.Printf("failed to check reservation: %v", err)
	}
	if hasTicket || hasReservation {
		return true
	}

	b.SendMessage(ctx, &bot.SendMessageParams{ChatID: chatID, Text: "Доп. Услуги и Меню можно заказать после покупки билета на активное мероприятие или при наличии брони в ближайшее время."})
	return false
}

func (app *App) canUseMenuType(ctx context.Context, b *bot.Bot, chatID int64, telegramID int64, categoryType string) bool {
	if categoryType != "service" {
		categoryType = "menu"
	}
	_, data, err := app.fsm.GetState(telegramID, "client")
	if err == nil {
		if _, hasEvent := data["event_id"]; hasEvent {
			if categoryType == "menu" {
				return true
			}
			b.SendMessage(ctx, &bot.SendMessageParams{ChatID: chatID, Text: "К билетам из афиши можно добавить только позиции из меню."})
			return false
		}
		if _, hasReservation := data["reservation_id"]; hasReservation {
			if categoryType == "service" {
				return true
			}
			b.SendMessage(ctx, &bot.SendMessageParams{ChatID: chatID, Text: "К бронированию лофта можно добавить только дополнительные услуги."})
			return false
		}
	}
	user, err := app.userRepo.GetByTelegramID(telegramID)
	if err != nil || user == nil {
		b.SendMessage(ctx, &bot.SendMessageParams{ChatID: chatID, Text: "Сначала купите билет или забронируйте лофт."})
		return false
	}
	if categoryType == "menu" {
		hasTicket, _ := app.orderRepo.HasActiveEventTicket(user.ID)
		if hasTicket {
			return true
		}
		b.SendMessage(ctx, &bot.SendMessageParams{ChatID: chatID, Text: "Меню доступно при покупке билета на мероприятие."})
		return false
	}
	hasReservation, _ := app.reservationRepo.HasUpcomingReservation(user.ID)
	if hasReservation {
		return true
	}
	b.SendMessage(ctx, &bot.SendMessageParams{ChatID: chatID, Text: "Дополнительные услуги доступны при бронировании лофта."})
	return false
}

func (app *App) showTicketQuantityPicker(ctx context.Context, b *bot.Bot, chatID, telegramID int64, eventID uint) {
	event, err := app.eventRepo.GetByID(eventID)
	logger.Printf("ticket prompt: telegram_id=%d event_id=%d err=%v", telegramID, eventID, err)
	if err != nil || !event.IsActive || event.PlacesLeft < 1 {
		b.SendMessage(ctx, &bot.SendMessageParams{ChatID: chatID, Text: "К сожалению, билеты закончились."})
		return
	}
	_, existingData, _ := app.fsm.GetState(telegramID, "client")
	if existingData == nil {
		existingData = make(map[string]interface{})
	}
	for key, value := range existingData {
		if !strings.HasPrefix(key, "cart_") {
			continue
		}
		item, ok := value.(map[string]interface{})
		if !ok {
			continue
		}
		if categoryType, _ := item["category_type"].(string); categoryType == "service" {
			delete(existingData, key)
		}
	}
	existingData["event_id"] = float64(eventID)
	delete(existingData, "ticket_quantity")
	delete(existingData, "custom_payment_phone")
	if err := app.fsm.SetState(telegramID, "client", "ticket:quantity", existingData); err != nil {
		logger.Printf("ticket prompt state setup failed: telegram_id=%d err=%v", telegramID, err)
		client.SendErrorMessage(ctx, b, chatID)
		return
	}
	message, err := b.SendMessage(ctx, &bot.SendMessageParams{ChatID: chatID, Text: fmt.Sprintf("Введите сообщением нужное количество билетов.\n🔥 Осталось мест: %d из %d", event.PlacesLeft, event.TotalPlaces)})
	if err == nil && message != nil {
		_ = app.fsm.UpdateData(telegramID, "client", map[string]interface{}{"ui_message_id": float64(message.ID)})
	}
}

func shouldReplaceClientMessage(data string) bool {
	return data == "main_menu" || data == "poster_show" || strings.HasPrefix(data, "poster_") ||
		strings.HasPrefix(data, "buy_ticket_") || data == "menu_categories" || data == "service_categories" ||
		strings.HasPrefix(data, "menu_cat_") || strings.HasPrefix(data, "menu_remove_") || data == "menu_cart" ||
		data == "cart_remove_ticket" || data == "cart_remove_reservation" ||
		data == "menu_checkout" || data == "reservation_start" || strings.HasPrefix(data, "res_") ||
		data == "reservation_confirm" || data == "go_to_payment" || data == "profile_show" || data == "wifi_info" ||
		data == "payment_custom_phone"
}

func deleteClientCallbackMessage(ctx context.Context, b *bot.Bot, update *models.Update) {
	if update.CallbackQuery == nil || update.CallbackQuery.Message.Message == nil {
		return
	}
	message := update.CallbackQuery.Message.Message
	if _, err := b.DeleteMessage(ctx, &bot.DeleteMessageParams{ChatID: message.Chat.ID, MessageID: message.ID}); err != nil {
		logger.Printf("failed to replace client message: chat_id=%d message_id=%d err=%v", message.Chat.ID, message.ID, err)
	}
}

// shouldReplaceAdminMessage mirrors shouldReplaceClientMessage: nearly every
// admin navigation callback should replace the tapped message with the next
// screen instead of piling up new messages. admin_confirm_/admin_reject_ are
// excluded because HandleConfirm/HandleReject already clean up the order
// notification across every admin chat via notify.DeleteOrderMessages.
func shouldReplaceAdminMessage(data string) bool {
	if data == "noop" {
		return false
	}
	if strings.HasPrefix(data, "admin_confirm_") || strings.HasPrefix(data, "admin_reject_") {
		return false
	}
	return strings.HasPrefix(data, "admin_")
}

// lastUintParam extracts the trailing numeric ID from a callback data
// string, regardless of how many underscore-separated words precede it
// (e.g. "admin_edit_ev_delete_yes_12" -> 12).
func lastUintParam(data string) uint {
	idx := strings.LastIndex(data, "_")
	if idx == -1 {
		return 0
	}
	v, _ := strconv.ParseUint(data[idx+1:], 10, 64)
	return uint(v)
}

func deleteAdminCallbackMessage(ctx context.Context, b *bot.Bot, update *models.Update) {
	if update.CallbackQuery == nil || update.CallbackQuery.Message.Message == nil {
		return
	}
	message := update.CallbackQuery.Message.Message
	if _, err := b.DeleteMessage(ctx, &bot.DeleteMessageParams{ChatID: message.Chat.ID, MessageID: message.ID}); err != nil {
		logger.Printf("failed to replace admin message: chat_id=%d message_id=%d err=%v", message.Chat.ID, message.ID, err)
	}
}

func (app *App) handleAdminStart(ctx context.Context, b *bot.Bot, update *models.Update) {
	chatID := update.Message.Chat.ID
	telegramID := update.Message.From.ID
	username := update.Message.From.Username

	if !app.isAuthorized(telegramID, username) {
		logger.Printf("unauthorized access attempt: id=%d username=@%s", telegramID, username)
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: chatID,
			Text:   "\u26D4 У вас нет доступа к этому боту.",
		})
		return
	}

	app.showAdminMainMenu(ctx, b, chatID)
}

func (app *App) handleAdminMessage(ctx context.Context, b *bot.Bot, update *models.Update) {
	if update.Message == nil || update.Message.Text == "" {
		return
	}

	chatID := update.Message.Chat.ID
	telegramID := update.Message.From.ID
	if !app.isAuthorized(telegramID, update.Message.From.Username) {
		return
	}

	currentState, _, err := app.fsm.GetState(telegramID, "admin")
	if err != nil {
		return
	}

	switch currentState {
	case "admin:poster:title":
		app.adminPosterHandler.HandleTitle(ctx, b, chatID, telegramID, update.Message.Text)
	case "admin:poster:description":
		app.adminPosterHandler.HandleDescription(ctx, b, chatID, telegramID, update.Message.Text)
	case "admin:poster:photo":
		b.SendMessage(ctx, &bot.SendMessageParams{ChatID: chatID, Text: "Пожалуйста, отправьте изображение как фото."})
	case "admin:poster:date":
		app.adminPosterHandler.HandleDate(ctx, b, chatID, telegramID, update.Message.Text)
	case "admin:poster:time_from":
		app.adminPosterHandler.HandleTimeFrom(ctx, b, chatID, telegramID, update.Message.Text)
	case "admin:poster:time_to":
		app.adminPosterHandler.HandleTimeTo(ctx, b, chatID, telegramID, update.Message.Text)
	case "admin:poster:price":
		app.adminPosterHandler.HandlePrice(ctx, b, chatID, telegramID, update.Message.Text)
	case "admin:poster:payment_phone":
		app.adminPosterHandler.HandlePaymentPhone(ctx, b, chatID, telegramID, update.Message.Text)
	case "admin:menu:name":
		app.adminMenuHandler.HandleName(ctx, b, chatID, telegramID, update.Message.Text)
	case "admin:menu:price":
		app.adminMenuHandler.HandlePrice(ctx, b, chatID, telegramID, update.Message.Text)
	case "admin:menu:cat_add":
		app.adminMenuHandler.HandleAddCategory(ctx, b, chatID, telegramID, update.Message.Text)
	case "admin:price:weekday", "admin:price:weekend":
		app.adminPricingHandler.HandleNewPrice(ctx, b, chatID, telegramID, update.Message.Text)
	case "admin:settings:phone":
		app.adminSettingsHandler.HandleNewPhone(ctx, b, chatID, telegramID, update.Message.Text)
	case "admin:settings:name":
		app.adminSettingsHandler.HandleNewName(ctx, b, chatID, telegramID, update.Message.Text)
	case "admin:editev:title":
		app.adminPosterHandler.HandleEditTitle(ctx, b, chatID, telegramID, update.Message.Text)
	case "admin:editev:desc":
		app.adminPosterHandler.HandleEditDescription(ctx, b, chatID, telegramID, update.Message.Text)
	case "admin:editev:photo":
		b.SendMessage(ctx, &bot.SendMessageParams{ChatID: chatID, Text: "Пожалуйста, отправьте изображение как фото."})
	case "admin:editev:dt":
		app.adminPosterHandler.HandleEditDateTime(ctx, b, chatID, telegramID, update.Message.Text)
	case "admin:editev:price":
		app.adminPosterHandler.HandleEditPrice(ctx, b, chatID, telegramID, update.Message.Text)
	case "admin:editev:places":
		app.adminPosterHandler.HandleEditPlaces(ctx, b, chatID, telegramID, update.Message.Text)
	}
}

func (app *App) handleAdminPhoto(ctx context.Context, b *bot.Bot, update *models.Update) {
	if update.Message == nil || len(update.Message.Photo) == 0 {
		return
	}

	chatID := update.Message.Chat.ID
	telegramID := update.Message.From.ID
	if !app.isAuthorized(telegramID, update.Message.From.Username) {
		return
	}

	photo := update.Message.Photo[len(update.Message.Photo)-1]
	switch {
	case app.fsm.IsInState(telegramID, "admin", "admin:poster:photo"):
		app.adminPosterHandler.HandlePhoto(ctx, b, chatID, telegramID, photo.FileID)
	case app.fsm.IsInState(telegramID, "admin", "admin:editev:photo"):
		app.adminPosterHandler.HandleEditPhoto(ctx, b, chatID, telegramID, photo.FileID)
	}
}

func (app *App) isAuthorized(telegramID int64, username string) bool {
	ids := notify.GetAdminIDs()
	usernames := notify.GetAdminUsernames()

	minLen := len(ids)
	if len(usernames) < minLen {
		minLen = len(usernames)
	}

	for i := 0; i < minLen; i++ {
		if ids[i] == telegramID {
			expected := strings.TrimPrefix(strings.TrimSpace(usernames[i]), "@")
			if username != "" && strings.EqualFold(expected, username) {
				return true
			}
		}
	}
	return false
}

func (app *App) showAdminMainMenu(ctx context.Context, b *bot.Bot, chatID int64) {
	pendingOrders, _ := app.orderRepo.GetPending()
	pendingCount := len(pendingOrders)
	webAppURL := strings.TrimRight(strings.TrimSpace(os.Getenv("WEBAPP_URL")), "/")

	notificationText := "\U0001F514 Новые заказы"
	if pendingCount > 0 {
		notificationText = fmt.Sprintf("%s (%d)", notificationText, pendingCount)
	}

	keyboard := [][]models.InlineKeyboardButton{}

	keyboard = append(keyboard,
		[]models.InlineKeyboardButton{
			{Text: notificationText, CallbackData: "admin_orders"},
		},
		[]models.InlineKeyboardButton{
			{Text: "\U0001F4C5 Расписание бронирований", CallbackData: "admin_schedule"},
		},
		[]models.InlineKeyboardButton{
			{Text: "\U0001F3AD Управление афишей", CallbackData: "admin_poster"},
		},
		[]models.InlineKeyboardButton{
			{Text: "\U0001F37D Доп. Услуги и Меню", CallbackData: "admin_menu"},
		},
		[]models.InlineKeyboardButton{
			{Text: "\U0001F4B0 Цены на аренду", CallbackData: "admin_pricing"},
		},
		[]models.InlineKeyboardButton{
			{Text: "\u2699\uFE0F Настройки", CallbackData: "admin_settings"},
		},
		[]models.InlineKeyboardButton{
			{Text: "\U0001F4CA Статистика", CallbackData: "admin_stats"},
		},
		[]models.InlineKeyboardButton{
			{Text: "👥 Контакты участников", CallbackData: "admin_contacts"},
		},
	)
	if webAppURL != "" {
		keyboard = append(keyboard, []models.InlineKeyboardButton{
			{Text: "\U0001F310 Открыть панель в браузере", URL: webAppURL},
		})
	}

	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID: chatID,
		Text:   "\U0001F3E0 Панель управления лофтом",
		ReplyMarkup: &models.InlineKeyboardMarkup{
			InlineKeyboard: keyboard,
		},
	})
}

func (app *App) handleAdminCallback(ctx context.Context, b *bot.Bot, update *models.Update) {
	if update.CallbackQuery == nil {
		return
	}
	defer answerCallback(ctx, b, update)

	data := update.CallbackQuery.Data
	chatID := getChatID(update)
	telegramID := update.CallbackQuery.From.ID
	username := update.CallbackQuery.From.Username

	if !app.isAuthorized(telegramID, username) {
		return
	}

	if shouldReplaceAdminMessage(data) {
		deleteAdminCallbackMessage(ctx, b, update)
	}

	switch {
	case data == "admin_main_menu":
		app.showAdminMainMenu(ctx, b, chatID)

	case data == "admin_orders":
		app.adminOrdersHandler.ShowNewOrders(ctx, b, chatID)

	case strings.HasPrefix(data, "admin_confirm_"):
		parts := strings.Split(data, "_")
		if len(parts) >= 3 {
			orderID, _ := strconv.ParseUint(parts[2], 10, 64)
			app.adminOrdersHandler.HandleConfirm(ctx, b, chatID, uint(orderID))
		}

	case strings.HasPrefix(data, "admin_reject_"):
		parts := strings.Split(data, "_")
		if len(parts) >= 3 {
			orderID, _ := strconv.ParseUint(parts[2], 10, 64)
			app.adminOrdersHandler.HandleReject(ctx, b, chatID, uint(orderID))
		}

	case data == "admin_poster":
		app.adminPosterHandler.ShowMenu(ctx, b, chatID)

	case data == "admin_poster_add":
		app.adminPosterHandler.StartAdd(ctx, b, chatID, telegramID)

	case data == "admin_poster_list":
		app.adminPosterHandler.ShowList(ctx, b, chatID)

	case data == "admin_poster_save":
		app.adminPosterHandler.Save(ctx, b, chatID, telegramID)

	case data == "admin_poster_phone_yes":
		app.adminPosterHandler.HandlePaymentPhoneChoice(ctx, b, chatID, telegramID, true)

	case data == "admin_poster_phone_no":
		app.adminPosterHandler.HandlePaymentPhoneChoice(ctx, b, chatID, telegramID, false)

	case strings.HasPrefix(data, "admin_poster_edit_"):
		app.adminPosterHandler.Edit(ctx, b, chatID, telegramID, lastUintParam(data))

	case strings.HasPrefix(data, "admin_edit_ev_delete_yes_"):
		app.adminPosterHandler.Delete(ctx, b, chatID, telegramID, lastUintParam(data))

	case strings.HasPrefix(data, "admin_edit_ev_delete_no_"):
		app.adminPosterHandler.Edit(ctx, b, chatID, telegramID, lastUintParam(data))

	case strings.HasPrefix(data, "admin_edit_ev_delete_"):
		app.adminPosterHandler.ConfirmDelete(ctx, b, chatID, lastUintParam(data))

	case strings.HasPrefix(data, "admin_edit_ev_toggle_"):
		app.adminPosterHandler.ToggleActive(ctx, b, chatID, telegramID, lastUintParam(data))

	case strings.HasPrefix(data, "admin_edit_ev_title_"):
		app.adminPosterHandler.StartEditField(ctx, b, chatID, telegramID, lastUintParam(data), "title")

	case strings.HasPrefix(data, "admin_edit_ev_desc_"):
		app.adminPosterHandler.StartEditField(ctx, b, chatID, telegramID, lastUintParam(data), "desc")

	case strings.HasPrefix(data, "admin_edit_ev_photo_"):
		app.adminPosterHandler.StartEditField(ctx, b, chatID, telegramID, lastUintParam(data), "photo")

	case strings.HasPrefix(data, "admin_edit_ev_dt_"):
		app.adminPosterHandler.StartEditField(ctx, b, chatID, telegramID, lastUintParam(data), "dt")

	case strings.HasPrefix(data, "admin_edit_ev_price_"):
		app.adminPosterHandler.StartEditField(ctx, b, chatID, telegramID, lastUintParam(data), "price")

	case strings.HasPrefix(data, "admin_edit_ev_places_"):
		app.adminPosterHandler.StartEditField(ctx, b, chatID, telegramID, lastUintParam(data), "places")

	case data == "admin_menu":
		app.adminMenuHandler.ShowMenu(ctx, b, chatID)

	case data == "admin_menu_add":
		app.adminMenuHandler.StartAdd(ctx, b, chatID, telegramID)

	case strings.HasPrefix(data, "admin_menu_cat_"):
		parts := strings.Split(data, "_")
		if len(parts) >= 4 {
			catID, _ := strconv.ParseUint(parts[3], 10, 64)
			app.adminMenuHandler.HandleCategoryPick(ctx, b, chatID, telegramID, uint(catID))
		}

	case data == "admin_menu_save":
		app.adminMenuHandler.Save(ctx, b, chatID, telegramID)

	case data == "admin_menu_list":
		app.adminMenuHandler.ShowList(ctx, b, chatID)

	case data == "admin_menu_categories":
		app.adminMenuHandler.ShowCategories(ctx, b, chatID)

	case data == "admin_menu_cat_add":
		app.adminMenuHandler.StartAddCategory(ctx, b, chatID, telegramID)

	case data == "admin_pricing":
		app.adminPricingHandler.Show(ctx, b, chatID)

	case data == "admin_price_weekday":
		app.adminPricingHandler.StartEdit(ctx, b, chatID, telegramID, "weekday")

	case data == "admin_price_weekend":
		app.adminPricingHandler.StartEdit(ctx, b, chatID, telegramID, "weekend")

	case data == "admin_price_save":
		app.adminPricingHandler.Save(ctx, b, chatID, telegramID)

	case data == "admin_schedule":
		app.adminScheduleHandler.Show(ctx, b, chatID)

	case strings.HasPrefix(data, "admin_res_cancel_"):
		parts := strings.Split(data, "_")
		if len(parts) >= 4 {
			reservationID, _ := strconv.ParseUint(parts[3], 10, 64)
			app.adminScheduleHandler.HandleCancel(ctx, b, chatID, uint(reservationID))
		}

	case strings.HasPrefix(data, "admin_schedule_"):
		parts := strings.Split(data, "_")
		if len(parts) >= 3 {
			app.adminScheduleHandler.ShowFiltered(ctx, b, chatID, parts[2])
		}

	case data == "admin_stats":
		app.adminStatsHandler.Show(ctx, b, chatID)

	case data == "admin_contacts":
		app.showAdminContacts(ctx, b, chatID)

	case strings.HasPrefix(data, "admin_stats_"):
		parts := strings.Split(data, "_")
		if len(parts) >= 3 {
			app.adminStatsHandler.ShowPeriod(ctx, b, chatID, parts[2])
		}

	case data == "admin_settings":
		app.adminSettingsHandler.Show(ctx, b, chatID)

	case data == "admin_settings_phone":
		app.adminSettingsHandler.StartEditPhone(ctx, b, chatID, telegramID)

	case data == "admin_settings_name":
		app.adminSettingsHandler.StartEditName(ctx, b, chatID, telegramID)

	case data == "noop":
	}
}

func (app *App) showAdminContacts(ctx context.Context, b *bot.Bot, chatID int64) {
	users, err := app.userRepo.GetAll()
	if err != nil {
		logger.Printf("failed to get contacts: %v", err)
		b.SendMessage(ctx, &bot.SendMessageParams{ChatID: chatID, Text: "❌ Не удалось загрузить контакты."})
		return
	}

	var lines []string
	for _, user := range users {
		if strings.TrimSpace(user.Username) == "" {
			continue
		}
		lines = append(lines, "@"+user.Username)
	}
	if len(lines) == 0 {
		b.SendMessage(ctx, &bot.SendMessageParams{ChatID: chatID, Text: "Контактов с username пока нет."})
		return
	}
	text := "👥 Контакты участников:\n\n" + strings.Join(lines, "\n")
	if len(text) > 3900 {
		text = text[:3900] + "\n..."
	}
	b.SendMessage(ctx, &bot.SendMessageParams{ChatID: chatID, Text: text})
}

func getChatID(update *models.Update) int64 {
	if update.CallbackQuery.Message.Message != nil {
		return update.CallbackQuery.Message.Message.Chat.ID
	}
	return 0
}
