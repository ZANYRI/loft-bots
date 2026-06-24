package admin

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"loft-bots/internal/db"
	"loft-bots/internal/repository"
	"loft-bots/internal/state"
)

type MenuHandler struct {
	menuItemRepo *repository.MenuItemRepo
	menuCatRepo  *repository.MenuCategoryRepo
	fsm          *state.FSM
}

func NewMenuHandler(menuItemRepo *repository.MenuItemRepo, menuCatRepo *repository.MenuCategoryRepo, fsm *state.FSM) *MenuHandler {
	return &MenuHandler{
		menuItemRepo: menuItemRepo,
		menuCatRepo:  menuCatRepo,
		fsm:          fsm,
	}
}

func (h *MenuHandler) ShowMenu(ctx context.Context, b *bot.Bot, chatID int64) {
	keyboard := [][]models.InlineKeyboardButton{
		{
			{Text: "\u2795 Добавить позицию", CallbackData: "admin_menu_add"},
		},
		{
			{Text: "\U0001F4CB Список позиций", CallbackData: "admin_menu_list"},
		},
		{
			{Text: "\U0001F4C2 Управление категориями", CallbackData: "admin_menu_categories"},
		},
		{
			{Text: "\U0001F519 Назад", CallbackData: "admin_main_menu"},
		},
	}

	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID: chatID,
		Text:   "\U0001F37D Управление меню",
		ReplyMarkup: &models.InlineKeyboardMarkup{
			InlineKeyboard: keyboard,
		},
	})
}

func (h *MenuHandler) StartAdd(ctx context.Context, b *bot.Bot, chatID int64, telegramID int64) {
	categories, err := h.menuCatRepo.GetAll()
	if err != nil || len(categories) == 0 {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: chatID,
			Text:   "Сначала создайте хотя бы одну категорию.",
		})
		return
	}

	keyboard := make([][]models.InlineKeyboardButton, 0)
	for _, cat := range categories {
		keyboard = append(keyboard, []models.InlineKeyboardButton{
			{Text: cat.Emoji + " " + cat.Name, CallbackData: fmt.Sprintf("admin_menu_cat_%d", cat.ID)},
		})
	}

	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID: chatID,
		Text:   "Выберите категорию:",
		ReplyMarkup: &models.InlineKeyboardMarkup{
			InlineKeyboard: keyboard,
		},
	})
}

func (h *MenuHandler) HandleCategoryPick(ctx context.Context, b *bot.Bot, chatID int64, telegramID int64, categoryID uint) {
	h.fsm.SetState(telegramID, "admin", "admin:menu:name", map[string]interface{}{
		"category_id": categoryID,
	})
	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID: chatID,
		Text:   "Введите название блюда/напитка:",
	})
}

func (h *MenuHandler) HandleName(ctx context.Context, b *bot.Bot, chatID int64, telegramID int64, name string) {
	_, data, _ := h.fsm.GetState(telegramID, "admin")
	data["name"] = name
	h.fsm.SetState(telegramID, "admin", "admin:menu:price", data)
	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID: chatID,
		Text:   "Введите цену (в рублях):",
	})
}

func (h *MenuHandler) HandlePrice(ctx context.Context, b *bot.Bot, chatID int64, telegramID int64, priceStr string) {
	price, err := strconv.ParseFloat(priceStr, 64)
	if err != nil {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: chatID,
			Text:   "Неверный формат цены. Введите число:",
		})
		return
	}

	_, data, _ := h.fsm.GetState(telegramID, "admin")
	data["price"] = price
	h.fsm.SetState(telegramID, "admin", "admin:menu:confirm", data)

	cat, _ := h.menuCatRepo.GetByID(uint(data["category_id"].(float64)))
	name, _ := data["name"].(string)

	text := fmt.Sprintf("Проверьте данные:\n\nКатегория: %s %s\nНазвание: %s\nЦена: %.0f \u20BD",
		cat.Emoji, cat.Name, name, price)

	keyboard := [][]models.InlineKeyboardButton{
		{
			{Text: "\u2705 Сохранить", CallbackData: "admin_menu_save"},
			{Text: "\u274C Отмена", CallbackData: "admin_menu"},
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

func (h *MenuHandler) Save(ctx context.Context, b *bot.Bot, chatID int64, telegramID int64) {
	_, data, err := h.fsm.GetState(telegramID, "admin")
	if err != nil {
		log.Printf("no menu data: %v", err)
		return
	}

	categoryID := uint(data["category_id"].(float64))
	name, _ := data["name"].(string)
	price, _ := data["price"].(float64)

	item := &db.MenuItem{
		CategoryID:  categoryID,
		Name:        name,
		Price:       price,
		IsAvailable: true,
	}

	if err := h.menuItemRepo.Create(item); err != nil {
		log.Printf("failed to create menu item: %v", err)
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: chatID,
			Text:   "\u274C Ошибка при сохранении.",
		})
		return
	}

	h.fsm.ClearState(telegramID, "admin")
	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID: chatID,
		Text:   "\u2705 Позиция добавлена!",
	})
}

func (h *MenuHandler) ShowList(ctx context.Context, b *bot.Bot, chatID int64) {
	items, err := h.menuItemRepo.GetAll()
	if err != nil {
		log.Printf("failed to get menu items: %v", err)
		return
	}

	categories, _ := h.menuCatRepo.GetAll()
	catMap := make(map[uint]db.MenuCategory)
	for _, c := range categories {
		catMap[c.ID] = c
	}

	text := "\U0001F4CB Меню \u2014 все позиции:\n\n"
	currentCat := uint(0)
	for _, item := range items {
		if item.CategoryID != currentCat {
			if cat, ok := catMap[item.CategoryID]; ok {
				text += fmt.Sprintf("\n%s %s\n", cat.Emoji, cat.Name)
			}
			currentCat = item.CategoryID
		}
		status := "\u2705"
		if !item.IsAvailable {
			status = "\U0001F534 (скрыто)"
		}
		text += fmt.Sprintf("  \u2022 %s \u2014 %.0f \u20BD %s\n", item.Name, item.Price, status)
	}

	keyboard := make([][]models.InlineKeyboardButton, 0)
	for _, item := range items {
		keyboard = append(keyboard, []models.InlineKeyboardButton{
			{Text: fmt.Sprintf("%s \u2014 %.0f \u20BD", item.Name, item.Price),
				CallbackData: fmt.Sprintf("admin_menu_edit_%d", item.ID)},
		})
	}
	keyboard = append(keyboard, []models.InlineKeyboardButton{
		{Text: "\U0001F519 Назад", CallbackData: "admin_menu"},
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

func (h *MenuHandler) ShowCategories(ctx context.Context, b *bot.Bot, chatID int64) {
	categories, err := h.menuCatRepo.GetAll()
	if err != nil {
		log.Printf("failed to get categories: %v", err)
		return
	}

	text := "\U0001F4C2 Категории меню:\n\n"
	for i, cat := range categories {
		text += fmt.Sprintf("%d. %s %s\n", i+1, cat.Emoji, cat.Name)
	}

	keyboard := [][]models.InlineKeyboardButton{
		{
			{Text: "\u2795 Добавить категорию", CallbackData: "admin_menu_cat_add"},
		},
		{
			{Text: "\U0001F519 Назад", CallbackData: "admin_menu"},
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

func (h *MenuHandler) StartAddCategory(ctx context.Context, b *bot.Bot, chatID int64, telegramID int64) {
	h.fsm.SetState(telegramID, "admin", "admin:menu:cat_add", nil)
	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID: chatID,
		Text:   "Введите название новой категории (с эмодзи, например: \U0001F9CA Закуски):",
	})
}

func (h *MenuHandler) HandleAddCategory(ctx context.Context, b *bot.Bot, chatID int64, telegramID int64, name string) {
	emoji := extractEmoji(name)
	cleanName := name
	if emoji != "" {
		cleanName = name[len(emoji):]
		cleanName = strings.TrimSpace(cleanName)
	}

	categories, _ := h.menuCatRepo.GetAll()
	sortOrder := len(categories)

	cat := &db.MenuCategory{
		Name:      cleanName,
		Emoji:     emoji,
		SortOrder: sortOrder,
	}

	if err := h.menuCatRepo.Create(cat); err != nil {
		log.Printf("failed to create category: %v", err)
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: chatID,
			Text:   "\u274C Ошибка при создании категории.",
		})
		return
	}

	h.fsm.ClearState(telegramID, "admin")
	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID: chatID,
		Text:   fmt.Sprintf("\u2705 Категория «%s» создана!", name),
	})
}

func (h *MenuHandler) Edit(ctx context.Context, b *bot.Bot, chatID int64, telegramID int64, itemID uint) {
	item, err := h.menuItemRepo.GetByID(itemID)
	if err != nil {
		log.Printf("failed to get menu item: %v", err)
		return
	}

	status := "\u2705 Доступно"
	if !item.IsAvailable {
		status = "\U0001F534 Скрыто"
	}

	toggleLabel := "\U0001F441 Скрыть из меню"
	if !item.IsAvailable {
		toggleLabel = "\U0001F441 Показать в меню"
	}

	text := fmt.Sprintf("\u270F\uFE0F %s\n\nКатегория: %s %s\nЦена: %.0f \u20BD\nСтатус: %s",
		item.Name, item.Category.Emoji, item.Category.Name, item.Price, status)

	keyboard := [][]models.InlineKeyboardButton{
		{
			{Text: "\U0001F4DD Изменить название", CallbackData: fmt.Sprintf("admin_edit_menu_name_%d", itemID)},
		},
		{
			{Text: "\U0001F4B0 Изменить цену", CallbackData: fmt.Sprintf("admin_edit_menu_price_%d", itemID)},
		},
		{
			{Text: "\U0001F4C2 Изменить категорию", CallbackData: fmt.Sprintf("admin_edit_menu_cat_%d", itemID)},
		},
		{
			{Text: toggleLabel, CallbackData: fmt.Sprintf("admin_edit_menu_toggle_%d", itemID)},
		},
		{
			{Text: "\U0001F5D1 Удалить позицию", CallbackData: fmt.Sprintf("admin_edit_menu_del_%d", itemID)},
		},
		{
			{Text: "\U0001F519 Назад", CallbackData: "admin_menu_list"},
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

func extractEmoji(s string) string {
	for _, r := range s {
		if r > 0x1000 {
			return string(r)
		}
	}
	return ""
}
