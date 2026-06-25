package client

import (
	"context"
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"loft-bots/internal/repository"
	"loft-bots/internal/state"
)

type MenuHandler struct {
	menuItemRepo *repository.MenuItemRepo
	menuCatRepo  *repository.MenuCategoryRepo
	settingsRepo *repository.SettingsRepo
	fsm          *state.FSM
	userRepo     *repository.UserRepo
}

func NewMenuHandler(
	menuItemRepo *repository.MenuItemRepo,
	menuCatRepo *repository.MenuCategoryRepo,
	settingsRepo *repository.SettingsRepo,
	fsm *state.FSM,
	userRepo *repository.UserRepo,
) *MenuHandler {
	return &MenuHandler{
		menuItemRepo: menuItemRepo,
		menuCatRepo:  menuCatRepo,
		settingsRepo: settingsRepo,
		fsm:          fsm,
		userRepo:     userRepo,
	}
}

func (h *MenuHandler) ShowCategories(ctx context.Context, b *bot.Bot, chatID int64) {
	categories, err := h.menuCatRepo.GetAll()
	if err != nil {
		log.Printf("failed to get categories: %v", err)
		SendErrorMessage(ctx, b, chatID)
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

	keyboard = append(keyboard, []models.InlineKeyboardButton{
		{Text: "\U0001F519 Главное меню", CallbackData: "main_menu"},
	})

	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID: chatID,
		Text: `🍷 Правила по алкоголю (BYOB)
Пробковый сбор — 300₽/бутылка. Мы предоставим лед, красивые бокалы и сервис.

🔥 Секретная акция:
Забронируйте дополнительные услуги прямо сейчас, и мы сделаем скидку 15%. Мы все подготовим к Вашему приходу!

🍽️ Дополнительные услуги:`,
		ParseMode: models.ParseModeHTML,
		ReplyMarkup: &models.InlineKeyboardMarkup{
			InlineKeyboard: keyboard,
		},
	})
}

func (h *MenuHandler) ShowCategoryItems(ctx context.Context, b *bot.Bot, chatID int64, categoryID uint) {
	items, err := h.menuItemRepo.GetByCategoryID(categoryID)
	if err != nil {
		log.Printf("failed to get items: %v", err)
		SendErrorMessage(ctx, b, chatID)
		return
	}

	cat, err := h.menuCatRepo.GetByID(categoryID)
	if err != nil {
		log.Printf("failed to get category: %v", err)
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
	keyboard = append(keyboard, []models.InlineKeyboardButton{
		{Text: "\U0001F519 Назад к категориям", CallbackData: "menu_categories"},
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
		log.Printf("failed to send category cover: %v", err)
	}
	b.SendMessage(ctx, &bot.SendMessageParams{ChatID: chatID, Text: text, ParseMode: models.ParseModeHTML, ReplyMarkup: replyMarkup})
}

func (h *MenuHandler) AddToCart(ctx context.Context, b *bot.Bot, chatID int64, userID uint, menuItemID uint, telegramID int64) {
	item, err := h.menuItemRepo.GetByID(menuItemID)
	if err != nil {
		log.Printf("failed to get menu item: %v", err)
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
		key: map[string]interface{}{"item_id": menuItemID, "name": item.Name, "price": item.Price, "qty": qty},
	}); err != nil {
		log.Printf("failed to add item to cart: %v", err)
		SendErrorMessage(ctx, b, chatID)
		return
	}

	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID: chatID,
		Text:   fmt.Sprintf("\u2714 %s добавлен в корзину!", item.Name),
	})
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
	if reservationID, ok := data["reservation_id"].(float64); ok && reservationID > 0 {
		text += "🔑 Бронирование лофта добавлено к заказу\n"
		hasItems = true
	}

	for key, val := range data {
		if len(key) > 5 && key[:5] == "cart_" {
			item := val.(map[string]interface{})
			name := item["name"].(string)
			price := item["price"].(float64)
			qty := int(item["qty"].(float64))
			subtotal := price * float64(qty)
			total += subtotal
			hasItems = true
			text += fmt.Sprintf("\u2022 %s \u00d7 %d \u2014 %.0f \u20BD\n", name, qty, subtotal)
		}
	}

	if !hasItems {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: chatID,
			Text:   "\U0001F6D2 Ваша корзина пуста",
		})
		return
	}

	text += fmt.Sprintf("\nИтого за доп. услуги: %.0f \u20BD", total)

	keyboard := [][]models.InlineKeyboardButton{
		{
			{Text: "\u2795 Добавить ещё", CallbackData: "menu_categories"},
			{Text: "\u2705 Готово", CallbackData: "menu_checkout"},
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

func SendErrorMessage(ctx context.Context, b *bot.Bot, chatID int64) {
	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID: chatID,
		Text:   "\u274C Произошла ошибка. Пожалуйста, попробуйте позже.",
	})
}
