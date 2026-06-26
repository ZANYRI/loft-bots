package main

import (
	"context"
	"fmt"
	"log"
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
	"loft-bots/internal/notify"
	"loft-bots/internal/repository"
	"loft-bots/internal/state"
	"loft-bots/internal/web"
)

var numRx = regexp.MustCompile(`\d+`)

const (
	telegramPollTimeout = 60 * time.Second
	telegramHTTPTimeout = 70 * time.Second
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

func newTelegramBot(token string) (*bot.Bot, error) {
	return bot.New(token, bot.WithHTTPClient(telegramPollTimeout, &http.Client{Timeout: telegramHTTPTimeout}))
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
		log.Fatal("CLIENT_BOT_TOKEN and ADMIN_BOT_TOKEN must be set")
	}

	clientB, err := newTelegramBot(clientBotToken)
	if err != nil {
		log.Fatalf("failed to create client bot: %v", err)
	}

	adminB, err := newTelegramBot(adminBotToken)
	if err != nil {
		log.Fatalf("failed to create admin bot: %v", err)
	}

	botMode := strings.ToLower(strings.TrimSpace(os.Getenv("BOT_MODE")))
	if botMode == "" {
		botMode = "all"
	}
	if botMode != "client" && botMode != "admin" && botMode != "all" {
		log.Fatalf("unsupported BOT_MODE %q, expected client, admin or all", botMode)
	}

	if botMode == "client" || botMode == "all" {
		app.initClientHandlers(clientB, adminB, gormDB)
	}
	if botMode == "admin" || botMode == "all" {
		app.initAdminHandlers(adminB, clientB, gormDB)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	log.Printf("Starting in %s mode...", botMode)
	if botMode == "client" || botMode == "all" {
		go app.startReviewScheduler(ctx, clientB)
		go app.startReservationBalanceScheduler(ctx, clientB)
		go app.startReservationStartReminderScheduler(ctx, clientB)
		go clientB.Start(ctx)
	}
	if botMode == "admin" || botMode == "all" {
		go adminB.Start(ctx)

		webPort := os.Getenv("WEBAPP_PORT")
		if webPort == "" {
			webPort = "8080"
		}

		ws := web.NewServer(
			webPort, adminBotToken, clientB,
			app.userRepo, app.eventRepo, app.orderRepo,
			app.reservationRepo, app.settingsRepo,
			app.rentalPriceRepo, app.menuCatRepo, app.menuItemRepo,
			app.expenseRepo,
			app.discountRepo,
			app.reviewRepo,
		)

		go func() {
			if err := ws.Start(); err != nil {
				log.Printf("web server error: %v", err)
			}
		}()
	}

	<-ctx.Done()
	log.Println("Shutting down...")
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
	app.clientMenuHandler = client.NewMenuHandler(app.menuItemRepo, app.menuCatRepo, app.settingsRepo, app.fsm, app.userRepo)
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
	log.Printf("client message: telegram_id=%d text=%q photos=%d document=%t", update.Message.From.ID, update.Message.Text, len(update.Message.Photo), update.Message.Document != nil)
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
	log.Printf("ticket input state: telegram_id=%d state=%q err=%v data=%v", telegramID, currentState, err, data)
	if err != nil || currentState != "ticket:quantity" {
		return
	}
	eventID, _ := data["event_id"].(float64)
	event, eventErr := app.eventRepo.GetByID(uint(eventID))
	log.Printf("ticket input event: telegram_id=%d event_id=%d event_err=%v", telegramID, uint(eventID), eventErr)

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
	log.Printf("ticket input parsed: telegram_id=%d raw=%q match=%q quantity=%d err=%v", telegramID, update.Message.Text, match, quantity, err)
	if err != nil || eventErr != nil || event == nil || quantity < 1 || quantity > event.PlacesLeft {
		max := 0
		if event != nil {
			max = event.PlacesLeft
		}
		b.SendMessage(ctx, &bot.SendMessageParams{ChatID: update.Message.Chat.ID, Text: fmt.Sprintf("Введите количество билетов от 1 до %d.", max)})
		return
	}
	if err := app.fsm.UpdateData(telegramID, "client", map[string]interface{}{"ticket_quantity": quantity}); err != nil {
		log.Printf("ticket input state update failed: telegram_id=%d err=%v", telegramID, err)
		client.SendErrorMessage(ctx, b, update.Message.Chat.ID)
		return
	}
	log.Printf("ticket input accepted: telegram_id=%d quantity=%d", telegramID, quantity)
	app.clientReservationHandler.PromptMenuOrPayment(ctx, b, update.Message.Chat.ID, telegramID)
}

func (app *App) handleReviewText(ctx context.Context, b *bot.Bot, update *models.Update, data map[string]interface{}) {
	telegramID := update.Message.From.ID
	user, err := app.userRepo.FindOrCreate(telegramID, update.Message.From.Username)
	if err != nil {
		log.Printf("failed to find review user: %v", err)
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
		log.Printf("failed to save review: %v", err)
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

	app.userRepo.FindOrCreate(telegramID, username)
	app.showClientMainMenu(ctx, b, chatID)
}

func (app *App) showClientMainMenu(ctx context.Context, b *bot.Bot, chatID int64) {
	description := app.settingValue("bot_start_description", "Выберите раздел:")
	avatarURL := app.settingValue("bot_start_avatar", "")
	siteURL := strings.TrimSpace(app.settingValue("site_url", ""))
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
			{Text: "\U0001F37D Доп. Услуги и Меню", CallbackData: "menu_categories"},
		},
		{
			{Text: "\U0001F6D2 Корзина", CallbackData: "menu_cart"},
		},
		{
			{Text: "\U0001F464 Мой профиль", CallbackData: "profile_show"},
		},
	}
	if siteURL != "" {
		keyboard = append(keyboard, []models.InlineKeyboardButton{{Text: "🌐 Сайт", URL: siteURL}})
	} else {
		keyboard = append(keyboard, []models.InlineKeyboardButton{{Text: "🌐 Сайт", CallbackData: "site_open"}})
	}

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
			log.Printf("failed to send start avatar: %v", err)
		}
	}

	b.SendMessage(ctx, &bot.SendMessageParams{ChatID: chatID, Text: text, ReplyMarkup: replyMarkup})
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
		log.Printf("failed to get due review requests: %v", err)
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
			log.Printf("failed to send review request: order_id=%d telegram_id=%d err=%v", order.ID, order.User.TelegramID, sendErr)
		}
		if err := app.orderRepo.MarkReviewRequested(order.ID, time.Now()); err != nil {
			log.Printf("failed to mark review request sent: order_id=%d err=%v", order.ID, err)
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
		log.Printf("failed to get due balance reminders: %v", err)
		return
	}
	for _, reservation := range reservations {
		paid, err := app.orderRepo.PrepaidByReservationID(reservation.ID)
		if err != nil {
			log.Printf("failed to get prepaid amount: reservation_id=%d err=%v", reservation.ID, err)
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
				log.Printf("failed to send balance reminder: reservation_id=%d err=%v", reservation.ID, err)
				continue
			}
		}
		if err := app.reservationRepo.MarkBalanceReminderSent(reservation.ID, time.Now()); err != nil {
			log.Printf("failed to mark balance reminder sent: reservation_id=%d err=%v", reservation.ID, err)
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
		log.Printf("failed to get reservation reminders: minutes=%d err=%v", minutesBefore, err)
		return
	}
	for _, reservation := range reservations {
		message := app.settingValue("message_reservation_reminder_60", "⏰ До начала вашей брони остался 1 час. Ждём вас {date} в {time_from}.")
		message = strings.ReplaceAll(message, "{date}", reservation.Date.Format("02.01.2006"))
		message = strings.ReplaceAll(message, "{time_from}", reservation.TimeFrom)
		message = strings.ReplaceAll(message, "{time_to}", reservation.TimeTo)
		if _, err := b.SendMessage(ctx, &bot.SendMessageParams{ChatID: reservation.User.TelegramID, Text: message}); err != nil {
			log.Printf("failed to send reservation reminder: reservation_id=%d err=%v", reservation.ID, err)
			continue
		}
		if err := app.reservationRepo.MarkStartReminderSent(reservation.ID, minutesBefore, time.Now()); err != nil {
			log.Printf("failed to mark reservation reminder sent: reservation_id=%d err=%v", reservation.ID, err)
		}
	}
}

func (app *App) sendDueReservationEndReminders(ctx context.Context, b *bot.Bot, minutesBefore int) {
	reservations, err := app.reservationRepo.GetDueEndReminders(time.Now(), minutesBefore)
	if err != nil {
		log.Printf("failed to get reservation end reminders: minutes=%d err=%v", minutesBefore, err)
		return
	}
	for _, reservation := range reservations {
		message := app.settingValue("message_reservation_reminder_30", "⏰ До конца вашей брони осталось 30 минут. Время окончания: {time_to}.")
		message = strings.ReplaceAll(message, "{date}", reservation.Date.Format("02.01.2006"))
		message = strings.ReplaceAll(message, "{time_from}", reservation.TimeFrom)
		message = strings.ReplaceAll(message, "{time_to}", reservation.TimeTo)
		if _, err := b.SendMessage(ctx, &bot.SendMessageParams{ChatID: reservation.User.TelegramID, Text: message}); err != nil {
			log.Printf("failed to send reservation end reminder: reservation_id=%d err=%v", reservation.ID, err)
			continue
		}
		if err := app.reservationRepo.MarkEndReminderSent(reservation.ID, time.Now()); err != nil {
			log.Printf("failed to mark reservation end reminder sent: reservation_id=%d err=%v", reservation.ID, err)
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

	switch {
	case data == "main_menu":
		app.showClientMainMenu(ctx, b, chatID)

	case data == "site_open":
		b.SendMessage(ctx, &bot.SendMessageParams{ChatID: chatID, Text: "Ссылка на сайт пока не настроена."})

	case data == "poster_show":
		app.clientPosterHandler.ShowList(ctx, b, chatID)

	case strings.HasPrefix(data, "buy_ticket_"):
		parts := strings.Split(data, "_")
		if len(parts) >= 3 {
			eventID, _ := strconv.ParseUint(parts[2], 10, 64)
			app.showTicketQuantityPicker(ctx, b, chatID, telegramID, uint(eventID))
		}

	case data == "menu_categories":
		app.clientMenuHandler.ShowCategories(ctx, b, chatID)

	case strings.HasPrefix(data, "menu_cat_"):
		parts := strings.Split(data, "_")
		if len(parts) >= 3 {
			catID, _ := strconv.ParseUint(parts[2], 10, 64)
			app.clientMenuHandler.ShowCategoryItems(ctx, b, chatID, uint(catID))
		}

	case strings.HasPrefix(data, "menu_add_"):
		if !app.canUseMenu(ctx, b, chatID, telegramID) {
			return
		}
		parts := strings.Split(data, "_")
		if len(parts) >= 3 {
			itemID, _ := strconv.ParseUint(parts[2], 10, 64)
			user, _ := app.userRepo.GetByTelegramID(telegramID)
			if user != nil {
				app.clientMenuHandler.AddToCart(ctx, b, chatID, user.ID, uint(itemID), telegramID)
			}
		}

	case data == "menu_cart":
		app.clientMenuHandler.ShowCart(ctx, b, chatID, telegramID)

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
		log.Printf("failed to check event ticket: %v", err)
	}
	hasReservation, err := app.reservationRepo.HasUpcomingReservation(user.ID)
	if err != nil {
		log.Printf("failed to check reservation: %v", err)
	}
	if hasTicket || hasReservation {
		return true
	}

	b.SendMessage(ctx, &bot.SendMessageParams{ChatID: chatID, Text: "Доп. Услуги и Меню можно заказать после покупки билета на активное мероприятие или при наличии брони в ближайшее время."})
	return false
}

func (app *App) showTicketQuantityPicker(ctx context.Context, b *bot.Bot, chatID, telegramID int64, eventID uint) {
	event, err := app.eventRepo.GetByID(eventID)
	log.Printf("ticket prompt: telegram_id=%d event_id=%d err=%v", telegramID, eventID, err)
	if err != nil || !event.IsActive || event.PlacesLeft < 1 {
		b.SendMessage(ctx, &bot.SendMessageParams{ChatID: chatID, Text: "К сожалению, билеты закончились."})
		return
	}
	if err := app.fsm.SetState(telegramID, "client", "ticket:quantity", map[string]interface{}{"event_id": float64(eventID)}); err != nil {
		log.Printf("ticket prompt state setup failed: telegram_id=%d err=%v", telegramID, err)
		client.SendErrorMessage(ctx, b, chatID)
		return
	}
	b.SendMessage(ctx, &bot.SendMessageParams{ChatID: chatID, Text: fmt.Sprintf("Введите сообщением нужное количество билетов.\n🔥 Осталось мест: %d из %d", event.PlacesLeft, event.TotalPlaces)})
}

func (app *App) handleAdminStart(ctx context.Context, b *bot.Bot, update *models.Update) {
	chatID := update.Message.Chat.ID
	telegramID := update.Message.From.ID
	username := update.Message.From.Username

	if !app.isAuthorized(telegramID, username) {
		log.Printf("unauthorized access attempt: id=%d username=@%s", telegramID, username)
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
	}
}

func (app *App) handleAdminPhoto(ctx context.Context, b *bot.Bot, update *models.Update) {
	if update.Message == nil || len(update.Message.Photo) == 0 {
		return
	}

	chatID := update.Message.Chat.ID
	telegramID := update.Message.From.ID
	if !app.isAuthorized(telegramID, update.Message.From.Username) || !app.fsm.IsInState(telegramID, "admin", "admin:poster:photo") {
		return
	}

	photo := update.Message.Photo[len(update.Message.Photo)-1]
	app.adminPosterHandler.HandlePhoto(ctx, b, chatID, telegramID, photo.FileID)
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
		log.Printf("failed to get contacts: %v", err)
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
