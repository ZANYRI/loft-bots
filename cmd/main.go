package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"

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

type App struct {
	gormDB              *gorm.DB
	userRepo            *repository.UserRepo
	eventRepo           *repository.EventRepo
	orderRepo           *repository.OrderRepo
	reservationRepo     *repository.ReservationRepo
	settingsRepo        *repository.SettingsRepo
	rentalPriceRepo     *repository.RentalPriceRepo
	menuCatRepo         *repository.MenuCategoryRepo
	menuItemRepo        *repository.MenuItemRepo
	fsm                 *state.FSM

	clientMenuHandler       *client.MenuHandler
	clientPosterHandler     *client.PosterHandler
	clientReservationHandler *client.ReservationHandler
	clientPaymentHandler    *client.PaymentHandler
	clientProfileHandler    *client.ProfileHandler

	adminOrdersHandler   *admin.OrdersHandler
	adminPosterHandler   *admin.PosterHandler
	adminMenuHandler     *admin.MenuHandler
	adminPricingHandler  *admin.PricingHandler
	adminScheduleHandler *admin.ScheduleHandler
	adminStatsHandler    *admin.StatsHandler
	adminSettingsHandler *admin.SettingsHandler
}

func main() {
	godotenv.Load()

	gormDB := db.Connect(db.GetDSN())

	app := &App{
		gormDB: gormDB,
	}

	app.initRepositories(gormDB)
	app.fsm = state.NewFSM(gormDB)

	clientBotToken := os.Getenv("CLIENT_BOT_TOKEN")
	adminBotToken := os.Getenv("ADMIN_BOT_TOKEN")

	if clientBotToken == "" || adminBotToken == "" {
		log.Fatal("CLIENT_BOT_TOKEN and ADMIN_BOT_TOKEN must be set")
	}

	clientB, err := bot.New(clientBotToken)
	if err != nil {
		log.Fatalf("failed to create client bot: %v", err)
	}

	adminB, err := bot.New(adminBotToken)
	if err != nil {
		log.Fatalf("failed to create admin bot: %v", err)
	}

	app.initClientHandlers(clientB, gormDB)
	app.initAdminHandlers(adminB, clientB, gormDB)

	webPort := os.Getenv("WEBAPP_PORT")
	if webPort == "" {
		webPort = "8080"
	}

	ws := web.NewServer(
		webPort, adminBotToken,
		app.userRepo, app.eventRepo, app.orderRepo,
		app.reservationRepo, app.settingsRepo,
		app.rentalPriceRepo, app.menuCatRepo, app.menuItemRepo,
	)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	log.Println("Starting bots...")
	go clientB.Start(ctx)
	go adminB.Start(ctx)

	go func() {
		if err := ws.Start(); err != nil {
			log.Printf("web server error: %v", err)
		}
	}()

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
}

func (app *App) initClientHandlers(b *bot.Bot, gormDB *gorm.DB) {
	app.clientMenuHandler = client.NewMenuHandler(app.menuItemRepo, app.menuCatRepo, app.settingsRepo, app.fsm, app.userRepo)
	app.clientPosterHandler = client.NewPosterHandler(app.eventRepo)
	app.clientReservationHandler = client.NewReservationHandler(app.reservationRepo, app.rentalPriceRepo, app.userRepo, app.fsm)
	app.clientPaymentHandler = client.NewPaymentHandler(app.orderRepo, app.menuItemRepo, app.settingsRepo, app.userRepo, app.eventRepo, app.reservationRepo, app.fsm, b)
	app.clientProfileHandler = client.NewProfileHandler(app.userRepo, app.orderRepo)

	b.RegisterHandler(bot.HandlerTypeMessageText, "/start", bot.MatchTypeExact, app.handleClientStart)
	b.RegisterHandler(bot.HandlerTypeCallbackQueryData, "", bot.MatchTypePrefix, app.handleClientCallback)
}

func (app *App) initAdminHandlers(b *bot.Bot, clientB *bot.Bot, gormDB *gorm.DB) {
	app.adminOrdersHandler = admin.NewOrdersHandler(app.orderRepo, clientB)
	app.adminPosterHandler = admin.NewPosterHandler(app.eventRepo, app.fsm)
	app.adminMenuHandler = admin.NewMenuHandler(app.menuItemRepo, app.menuCatRepo, app.fsm)
	app.adminPricingHandler = admin.NewPricingHandler(app.rentalPriceRepo, app.fsm)
	app.adminScheduleHandler = admin.NewScheduleHandler(app.reservationRepo)
	app.adminStatsHandler = admin.NewStatsHandler(app.orderRepo, app.reservationRepo)
	app.adminSettingsHandler = admin.NewSettingsHandler(app.settingsRepo, app.fsm)

	b.RegisterHandler(bot.HandlerTypeMessageText, "/start", bot.MatchTypeExact, app.handleAdminStart)
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
	settings, _ := app.settingsRepo.Get("loft_name")
	loftName := "Название лофта"
	if settings != nil {
		loftName = settings.Value
	}

	text := "\U0001F3E0 Добро пожаловать в [" + loftName + "]!\n\nВыберите раздел:"

	keyboard := [][]models.InlineKeyboardButton{
		{
			{Text: "\U0001F3AD Афиша", CallbackData: "poster_show"},
		},
		{
			{Text: "\U0001F511 Зарезервировать лофт", CallbackData: "reservation_start"},
		},
		{
			{Text: "\U0001F37D Меню", CallbackData: "menu_categories"},
		},
		{
			{Text: "\U0001F464 Мой профиль", CallbackData: "profile_show"},
		},
	}

	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:    chatID,
		Text:      text,
		ParseMode: models.ParseModeHTML,
		ReplyMarkup: &models.InlineKeyboardMarkup{
			InlineKeyboard: keyboard,
		},
	})
}

func (app *App) handleClientCallback(ctx context.Context, b *bot.Bot, update *models.Update) {
	if update.CallbackQuery == nil {
		return
	}
	defer answerCallback(ctx, b, update)

	data := update.CallbackQuery.Data
	chatID := getChatID(update)
	telegramID := update.CallbackQuery.From.ID

	switch {
	case data == "main_menu":
		app.showClientMainMenu(ctx, b, chatID)

	case data == "poster_show":
		app.clientPosterHandler.ShowList(ctx, b, chatID)

	case strings.HasPrefix(data, "buy_ticket_"):
		parts := strings.Split(data, "_")
		if len(parts) >= 3 {
			eventID, _ := strconv.ParseUint(parts[2], 10, 64)
			app.fsm.UpdateData(telegramID, "client", map[string]interface{}{
				"event_id": float64(eventID),
			})
			app.clientReservationHandler.PromptMenuOrPayment(ctx, b, chatID, telegramID)
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
		app.clientPaymentHandler.ShowPayment(ctx, b, chatID, telegramID)

	case data == "reservation_start":
		app.clientReservationHandler.Start(ctx, b, chatID, telegramID)

	case strings.HasPrefix(data, "res_"):
		app.clientReservationHandler.HandleCallback(ctx, b, chatID, telegramID, data)

	case data == "reservation_confirm":
		app.clientReservationHandler.Confirm(ctx, b, chatID, telegramID)

	case data == "go_to_payment":
		app.clientPaymentHandler.ShowPayment(ctx, b, chatID, telegramID)

	case strings.HasPrefix(data, "payment_done_"):
		app.clientPaymentHandler.HandlePaymentDone(ctx, b, chatID, telegramID)

	case data == "profile_show":
		app.clientProfileHandler.Show(ctx, b, chatID, telegramID)

	case data == "noop":
	}
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

	notificationText := "\U0001F514 Новые заказы"
	if pendingCount > 0 {
		notificationText = fmt.Sprintf("%s (%d)", notificationText, pendingCount)
	}

	webAppURL := os.Getenv("WEBAPP_URL")

	keyboard := [][]models.InlineKeyboardButton{}

	if webAppURL != "" {
		keyboard = append(keyboard, []models.InlineKeyboardButton{
			{
				Text:    "\U0001F310 Открыть панель управления",
				WebApp:  &models.WebAppInfo{URL: webAppURL},
			},
		})
	}

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
			{Text: "\U0001F37D Управление меню", CallbackData: "admin_menu"},
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
	)

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

	case strings.HasPrefix(data, "admin_schedule_"):
		parts := strings.Split(data, "_")
		if len(parts) >= 3 {
			app.adminScheduleHandler.ShowFiltered(ctx, b, chatID, parts[2])
		}

	case data == "admin_stats":
		app.adminStatsHandler.Show(ctx, b, chatID)

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

func getChatID(update *models.Update) int64 {
	if update.CallbackQuery.Message.Message != nil {
		return update.CallbackQuery.Message.Message.Chat.ID
	}
	return 0
}
