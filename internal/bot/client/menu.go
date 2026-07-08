package client

import (
	"context"
	"fmt"
	"loft-bots/internal/logger"
	"net/url"
	"os"
	"path/filepath"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"loft-bots/internal/repository"
	"loft-bots/internal/state"
)

type MenuHandler struct {
	menuItemRepo    *repository.MenuItemRepo
	menuCatRepo     *repository.MenuCategoryRepo
	settingsRepo    *repository.SettingsRepo
	fsm             *state.FSM
	userRepo        *repository.UserRepo
	eventRepo       *repository.EventRepo
	reservationRepo *repository.ReservationRepo
}

func NewMenuHandler(
	menuItemRepo *repository.MenuItemRepo,
	menuCatRepo *repository.MenuCategoryRepo,
	settingsRepo *repository.SettingsRepo,
	fsm *state.FSM,
	userRepo *repository.UserRepo,
	eventRepo *repository.EventRepo,
	reservationRepo *repository.ReservationRepo,
) *MenuHandler {
	return &MenuHandler{
		menuItemRepo:    menuItemRepo,
		menuCatRepo:     menuCatRepo,
		settingsRepo:    settingsRepo,
		fsm:             fsm,
		userRepo:        userRepo,
		eventRepo:       eventRepo,
		reservationRepo: reservationRepo,
	}
}

func (h *MenuHandler) ShowCategories(ctx context.Context, b *bot.Bot, chatID int64, telegramID int64, categoryType string) {
	if categoryType != "service" {
		categoryType = "menu"
	}
	categories, err := h.menuCatRepo.GetByType(categoryType)
	if err != nil {
		logger.Printf("failed to get categories: %v", err)
		SendErrorMessage(ctx, b, chatID)
		return
	}
	if categoryType == "service" && len(categories) == 1 {
		h.ShowCategoryItems(ctx, b, chatID, telegramID, categories[0].ID)
		return
	}

	rows := make([]models.InlineKeyboardButton, 0)
	var keyboard [][]models.InlineKeyboardButton

	for i, cat := range categories {
		btnText := cat.Emoji + " " + cat.Name
		rows = append(rows, models.InlineKeyboardButton{
			Text:         btnText,
			CallbackData: "menu_cat_" + fmt.Sprint(cat.ID),
		})
		if (i+1)%2 == 0 || i == len(categories)-1 {
			keyboard = append(keyboard, rows)
			rows = make([]models.InlineKeyboardButton, 0)
		}
	}

	if h.hasActiveCheckout(telegramID) {
		keyboard = append(keyboard, []models.InlineKeyboardButton{{Text: "➡ Перейти к оплате", CallbackData: "go_to_payment"}})
	} else {
		keyboard = append(keyboard, []models.InlineKeyboardButton{{Text: "\U0001F519 Главное меню", CallbackData: "main_menu"}})
	}

	title := "🍽️ Меню:"
	if categoryType == "service" {
		title = "✨ Дополнительные услуги:"
	}
	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:    chatID,
		Text:      title,
		ParseMode: models.ParseModeHTML,
		ReplyMarkup: &models.InlineKeyboardMarkup{
			InlineKeyboard: keyboard,
		},
	})
}

func (h *MenuHandler) ShowCategoryItems(ctx context.Context, b *bot.Bot, chatID int64, telegramID int64, categoryID uint) {
	items, err := h.menuItemRepo.GetByCategoryID(categoryID)
	if err != nil {
		logger.Printf("failed to get items: %v", err)
		SendErrorMessage(ctx, b, chatID)
		return
	}

	cat, err := h.menuCatRepo.GetByID(categoryID)
	if err != nil {
		logger.Printf("failed to get category: %v", err)
		return
	}

	text := cat.Emoji + " " + cat.Name + ":\n\n"
	for _, item := range items {
		if item.IsAvailable {
			text += fmt.Sprintf("%s \u2014 %.0f \u20BD\n", item.Name, item.Price)
		}
	}

	keyboard := make([][]models.InlineKeyboardButton, 0)
	for _, item := range items {
		if item.IsAvailable {
			keyboard = append(keyboard, []models.InlineKeyboardButton{
				{Text: "\u2795 " + item.Name, CallbackData: "menu_add_" + fmt.Sprint(item.ID)},
			})
		}
	}

	keyboard = append(keyboard, []models.InlineKeyboardButton{
		{Text: "\U0001F6D2 Корзина", CallbackData: "menu_cart"},
	})
	if h.hasActiveCheckout(telegramID) {
		keyboard = append(keyboard, []models.InlineKeyboardButton{{Text: "➡ Перейти к оплате", CallbackData: "go_to_payment"}})
	}
	backCallback := "menu_categories"
	if cat.Type == "service" {
		backCallback = "service_categories"
	}
	keyboard = append(keyboard, []models.InlineKeyboardButton{
		{Text: "\U0001F519 Назад к категориям", CallbackData: backCallback},
	})

	replyMarkup := &models.InlineKeyboardMarkup{InlineKeyboard: keyboard}
	if cat.ImageURL != "" {
		var photo models.InputFile = &models.InputFileString{Data: cat.ImageURL}
		var localFile *os.File
		if imageURL, err := url.Parse(cat.ImageURL); err == nil {
			if file, err := os.Open(filepath.Join("uploads", filepath.Base(imageURL.Path))); err == nil {
				localFile = file
				photo = &models.InputFileUpload{Filename: filepath.Base(imageURL.Path), Data: file}
			}
		}
		_, err := b.SendPhoto(ctx, &bot.SendPhotoParams{
			ChatID:      chatID,
			Photo:       photo,
			Caption:     text,
			ParseMode:   models.ParseModeHTML,
			ReplyMarkup: replyMarkup,
		})
		if localFile != nil {
			localFile.Close()
		}
		if err == nil {
			return
		}
		logger.Printf("failed to send category cover: %v", err)
	}
	b.SendMessage(ctx, &bot.SendMessageParams{ChatID: chatID, Text: text, ParseMode: models.ParseModeHTML, ReplyMarkup: replyMarkup})
}

func (h *MenuHandler) AddToCart(ctx context.Context, b *bot.Bot, chatID int64, userID uint, menuItemID uint, telegramID int64) {
	item, err := h.menuItemRepo.GetByID(menuItemID)
	if err != nil {
		logger.Printf("failed to get menu item: %v", err)
		SendErrorMessage(ctx, b, chatID)
		return
	}

	key := "cart_" + fmt.Sprint(menuItemID)
	_, data, _ := h.fsm.GetState(telegramID, "client")
	qty := 1
	if existing, ok := data[key].(map[string]interface{}); ok {
		if currentQty, ok := existing["qty"].(float64); ok {
			qty = int(currentQty) + 1
		}
	}
	if err := h.fsm.UpdateData(telegramID, "client", map[string]interface{}{
		key: map[string]interface{}{"item_id": menuItemID, "name": item.Name, "price": item.Price, "qty": qty, "category_type": item.Category.Type},
	}); err != nil {
		logger.Printf("failed to add item to cart: %v", err)
		SendErrorMessage(ctx, b, chatID)
		return
	}

}

func (h *MenuHandler) RemoveFromCart(ctx context.Context, b *bot.Bot, chatID int64, menuItemID uint, telegramID int64) {
	key := "cart_" + fmt.Sprint(menuItemID)
	_, data, err := h.fsm.GetState(telegramID, "client")
	if err != nil {
		h.ShowCart(ctx, b, chatID, telegramID)
		return
	}
	item, ok := data[key].(map[string]interface{})
	if !ok {
		h.ShowCart(ctx, b, chatID, telegramID)
		return
	}
	qty, _ := item["qty"].(float64)
	if qty <= 1 {
		if err := h.fsm.DeleteData(telegramID, "client", key); err != nil {
			logger.Printf("failed to remove cart item: %v", err)
			SendErrorMessage(ctx, b, chatID)
			return
		}
	} else {
		item["qty"] = qty - 1
		if err := h.fsm.UpdateData(telegramID, "client", map[string]interface{}{key: item}); err != nil {
			logger.Printf("failed to decrease cart item: %v", err)
			SendErrorMessage(ctx, b, chatID)
			return
		}
	}
	h.ShowCart(ctx, b, chatID, telegramID)
}

func (h *MenuHandler) RemoveTicketFromCart(ctx context.Context, b *bot.Bot, chatID int64, telegramID int64) {
	if err := h.fsm.DeleteData(telegramID, "client", "event_id", "ticket_quantity", "custom_payment_phone"); err != nil {
		logger.Printf("failed to remove ticket from cart: %v", err)
	}
	h.ShowCart(ctx, b, chatID, telegramID)
}

func (h *MenuHandler) RemoveReservationFromCart(ctx context.Context, b *bot.Bot, chatID int64, telegramID int64) {
	_, data, err := h.fsm.GetState(telegramID, "client")
	if err == nil {
		if reservationID, ok := data["reservation_id"].(float64); ok && reservationID > 0 {
			if err := h.reservationRepo.Cancel(uint(reservationID)); err != nil {
				logger.Printf("failed to cancel reservation: reservation_id=%d err=%v", uint(reservationID), err)
			}
		}
	}
	if err := h.fsm.DeleteData(telegramID, "client", "reservation_id", "total_price", "reservation_full_price", "date", "time_from", "time_to", "day_type", "hours", "price_per_hour"); err != nil {
		logger.Printf("failed to remove reservation from cart: %v", err)
	}
	h.ShowCart(ctx, b, chatID, telegramID)
}

func (h *MenuHandler) ShowCart(ctx context.Context, b *bot.Bot, chatID int64, telegramID int64) {
	_, data, err := h.fsm.GetState(telegramID, "client")
	if err != nil {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: chatID,
			Text:   "\U0001F6D2 Ваша корзина пуста",
		})
		return
	}

	text := "\U0001F6D2 Ваша корзина:\n\n"
	var total float64
	hasItems := false
	removeButtons := make([][]models.InlineKeyboardButton, 0)

	if eventID, ok := data["event_id"].(float64); ok && eventID > 0 {
		if event, err := h.eventRepo.GetByID(uint(eventID)); err == nil {
			quantity, _ := data["ticket_quantity"].(float64)
			if quantity < 1 {
				quantity = 1
			}
			subtotal := event.Price * quantity
			total += subtotal
			hasItems = true
			text += fmt.Sprintf("🎟 Билет на «%s» × %d — %.0f ₽\n", event.Title, int(quantity), subtotal)
			removeButtons = append(removeButtons, []models.InlineKeyboardButton{{Text: "➖ Билет «" + event.Title + "»", CallbackData: "cart_remove_ticket"}})
		}
	}

	if reservationID, ok := data["reservation_id"].(float64); ok && reservationID > 0 {
		if reservation, err := h.reservationRepo.GetByID(uint(reservationID)); err == nil {
			total += reservation.TotalPrice
			text += fmt.Sprintf("🔑 Бронирование лофта %s, %s – %s — %.0f ₽\n", formatDateRU(reservation.Date), reservation.TimeFrom, reservation.TimeTo, reservation.TotalPrice)
		} else {
			text += "🔑 Бронирование лофта добавлено к заказу\n"
		}
		hasItems = true
		removeButtons = append(removeButtons, []models.InlineKeyboardButton{{Text: "➖ Бронирование лофта", CallbackData: "cart_remove_reservation"}})
	}

	for key, val := range data {
		if len(key) > 5 && key[:5] == "cart_" {
			item, ok := val.(map[string]interface{})
			if !ok {
				continue
			}
			name, _ := item["name"].(string)
			price, _ := item["price"].(float64)
			qtyValue, _ := item["qty"].(float64)
			qty := int(qtyValue)
			if name == "" || qty <= 0 {
				continue
			}
			subtotal := price * float64(qty)
			total += subtotal
			hasItems = true
			icon := "🍽"
			if kind, _ := item["category_type"].(string); kind == "service" {
				icon = "✨"
			}
			text += fmt.Sprintf("%s %s × %d — %.0f ₽\n", icon, name, qty, subtotal)
			removeButtons = append(removeButtons, []models.InlineKeyboardButton{{Text: "➖ " + name, CallbackData: "menu_remove_" + fmt.Sprint(item["item_id"])}})
		}
	}

	if !hasItems {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: chatID,
			Text:   "\U0001F6D2 Ваша корзина пуста",
			ReplyMarkup: &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
				{{Text: "➕ Добавить товары", CallbackData: "menu_categories"}},
				{{Text: "🏠 Главное меню", CallbackData: "main_menu"}},
			}},
		})
		return
	}

	text += fmt.Sprintf("\nИтого: %.0f \u20BD", total)

	keyboard := append(removeButtons, []models.InlineKeyboardButton{
		{Text: "\u2795 Добавить ещё", CallbackData: "menu_categories"},
		{Text: "➡ Перейти к оплате", CallbackData: "menu_checkout"},
	})

	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:    chatID,
		Text:      text,
		ParseMode: models.ParseModeHTML,
		ReplyMarkup: &models.InlineKeyboardMarkup{
			InlineKeyboard: keyboard,
		},
	})
}

func (h *MenuHandler) hasActiveCheckout(telegramID int64) bool {
	_, data, err := h.fsm.GetState(telegramID, "client")
	if err != nil {
		return false
	}
	_, hasEvent := data["event_id"]
	_, hasReservation := data["reservation_id"]
	return hasEvent || hasReservation
}

func SendErrorMessage(ctx context.Context, b *bot.Bot, chatID int64) {
	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID: chatID,
		Text:   "\u274C Произошла ошибка. Пожалуйста, попробуйте позже.",
	})
}
