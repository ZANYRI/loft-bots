package admin

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"loft-bots/internal/db"
	"loft-bots/internal/repository"
	"loft-bots/internal/state"
)

type PosterHandler struct {
	eventRepo *repository.EventRepo
	fsm       *state.FSM
}

func NewPosterHandler(eventRepo *repository.EventRepo, fsm *state.FSM) *PosterHandler {
	return &PosterHandler{
		eventRepo: eventRepo,
		fsm:       fsm,
	}
}

func (h *PosterHandler) ShowMenu(ctx context.Context, b *bot.Bot, chatID int64) {
	keyboard := [][]models.InlineKeyboardButton{
		{
			{Text: "\u2795 Добавить мероприятие", CallbackData: "admin_poster_add"},
		},
		{
			{Text: "\U0001F4CB Список мероприятий", CallbackData: "admin_poster_list"},
		},
		{
			{Text: "\U0001F519 Назад", CallbackData: "admin_main_menu"},
		},
	}

	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID: chatID,
		Text:   "\U0001F3AD Афиша",
		ReplyMarkup: &models.InlineKeyboardMarkup{
			InlineKeyboard: keyboard,
		},
	})
}

func (h *PosterHandler) StartAdd(ctx context.Context, b *bot.Bot, chatID int64, telegramID int64) {
	h.fsm.SetState(telegramID, "admin", "admin:poster:title", nil)
	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:    chatID,
		Text:      "Введите название мероприятия:",
	})
}

func (h *PosterHandler) HandleTitle(ctx context.Context, b *bot.Bot, chatID int64, telegramID int64, title string) {
	h.fsm.SetState(telegramID, "admin", "admin:poster:description", map[string]interface{}{
		"title": title,
	})
	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID: chatID,
		Text:   "Введите описание мероприятия:",
	})
}

func (h *PosterHandler) HandleDescription(ctx context.Context, b *bot.Bot, chatID int64, telegramID int64, desc string) {
	_, data, _ := h.fsm.GetState(telegramID, "admin")
	data["description"] = desc
	h.fsm.SetState(telegramID, "admin", "admin:poster:photo", data)
	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID: chatID,
		Text:   "Загрузите фото мероприятия:",
	})
}

func (h *PosterHandler) HandlePhoto(ctx context.Context, b *bot.Bot, chatID int64, telegramID int64, fileID string) {
	_, data, _ := h.fsm.GetState(telegramID, "admin")
	data["image_file_id"] = fileID
	h.fsm.SetState(telegramID, "admin", "admin:poster:date", data)
	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID: chatID,
		Text:   "Введите дату мероприятия (ДД.ММ.ГГГГ):",
	})
}

func (h *PosterHandler) HandleDate(ctx context.Context, b *bot.Bot, chatID int64, telegramID int64, dateStr string) {
	parsed, err := time.Parse("02.01.2006", dateStr)
	if err != nil {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: chatID,
			Text:   "Неверный формат даты. Используйте ДД.ММ.ГГГГ:",
		})
		return
	}
	if dateOnly(parsed).Before(dateOnly(time.Now())) {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: chatID,
			Text:   "Нельзя создать мероприятие задним числом. Введите сегодняшнюю или будущую дату:",
		})
		return
	}

	_, data, _ := h.fsm.GetState(telegramID, "admin")
	data["event_date"] = parsed.Format("2006-01-02")
	h.fsm.SetState(telegramID, "admin", "admin:poster:time_from", data)
	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID: chatID,
		Text:   "Введите время начала (ЧЧ:ММ):",
	})
}

func (h *PosterHandler) HandleTimeFrom(ctx context.Context, b *bot.Bot, chatID int64, telegramID int64, timeStr string) {
	if !isValidTime(timeStr) {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: chatID,
			Text:   "Неверный формат времени. Используйте ЧЧ:ММ:",
		})
		return
	}

	_, data, _ := h.fsm.GetState(telegramID, "admin")
	data["time_from"] = timeStr
	h.fsm.SetState(telegramID, "admin", "admin:poster:time_to", data)
	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID: chatID,
		Text:   "Введите время окончания (ЧЧ:ММ):",
	})
}

func (h *PosterHandler) HandleTimeTo(ctx context.Context, b *bot.Bot, chatID int64, telegramID int64, timeStr string) {
	if !isValidTime(timeStr) {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: chatID,
			Text:   "Неверный формат времени. Используйте ЧЧ:ММ:",
		})
		return
	}

	_, data, _ := h.fsm.GetState(telegramID, "admin")
	data["time_to"] = timeStr
	h.fsm.SetState(telegramID, "admin", "admin:poster:price", data)
	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID: chatID,
		Text:   "Введите цену билета (в рублях):",
	})
}

func (h *PosterHandler) HandlePrice(ctx context.Context, b *bot.Bot, chatID int64, telegramID int64, priceStr string) {
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
	h.fsm.SetState(telegramID, "admin", "admin:poster:confirm", data)

	title, _ := data["title"].(string)
	desc, _ := data["description"].(string)
	eventDate, _ := data["event_date"].(string)
	timeFrom, _ := data["time_from"].(string)
	timeTo, _ := data["time_to"].(string)

	text := fmt.Sprintf("Проверьте данные:\n\nНазвание: %s\nОписание: %s\nДата: %s\nВремя: %s\u2013%s\nЦена: %.0f \u20BD",
		title, desc, eventDate, timeFrom, timeTo, price)

	keyboard := [][]models.InlineKeyboardButton{
		{
			{Text: "\u2705 Сохранить", CallbackData: "admin_poster_save"},
			{Text: "\u270F\uFE0F Изменить", CallbackData: "admin_poster_add"},
			{Text: "\u274C Отмена", CallbackData: "admin_poster"},
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

func (h *PosterHandler) Save(ctx context.Context, b *bot.Bot, chatID int64, telegramID int64) {
	_, data, err := h.fsm.GetState(telegramID, "admin")
	if err != nil {
		log.Printf("no poster data: %v", err)
		return
	}

	title, _ := data["title"].(string)
	desc, _ := data["description"].(string)
	imageFileID, _ := data["image_file_id"].(string)
	dateStr, _ := data["event_date"].(string)
	timeFrom, _ := data["time_from"].(string)
	timeTo, _ := data["time_to"].(string)
	price, _ := data["price"].(float64)

	eventDate, _ := time.Parse("2006-01-02", dateStr)
	if dateOnly(eventDate).Before(dateOnly(time.Now())) {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: chatID,
			Text:   "❌ Нельзя сохранить мероприятие задним числом.",
		})
		return
	}

	event := &db.Event{
		Title:       title,
		Description: desc,
		ImageFileID: imageFileID,
		EventDate:   eventDate,
		TimeFrom:    timeFrom,
		TimeTo:      timeTo,
		Price:       price,
		IsActive:    true,
		CreatedAt:   time.Now(),
	}

	if err := h.eventRepo.Create(event); err != nil {
		log.Printf("failed to create event: %v", err)
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: chatID,
			Text:   "\u274C Ошибка при сохранении мероприятия.",
		})
		return
	}

	h.fsm.ClearState(telegramID, "admin")

	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID: chatID,
		Text:   "\u2705 Мероприятие успешно добавлено!",
	})
}

func (h *PosterHandler) ShowList(ctx context.Context, b *bot.Bot, chatID int64) {
	events, err := h.eventRepo.GetAll()
	if err != nil {
		log.Printf("failed to get events: %v", err)
		return
	}

	if len(events) == 0 {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: chatID,
			Text:   "Мероприятий пока нет.",
		})
		return
	}

	text := "\U0001F4CB Мероприятия:\n\n"
	for i, ev := range events {
		status := "\u2705 Активно"
		if !ev.IsActive {
			status = "\U0001F534 Скрыто"
		}
		text += fmt.Sprintf("%d. %s | %s, %s | %.0f \u20BD | %s\n",
			i+1, ev.Title, ev.EventDate.Format("2 January"), ev.TimeFrom, ev.Price, status)
	}

	keyboard := make([][]models.InlineKeyboardButton, len(events))
	for i, ev := range events {
		keyboard[i] = []models.InlineKeyboardButton{
			{Text: fmt.Sprintf("%d. %s", i+1, ev.Title), CallbackData: fmt.Sprintf("admin_poster_edit_%d", ev.ID)},
		}
	}
	keyboard = append(keyboard, []models.InlineKeyboardButton{
		{Text: "\U0001F519 Назад", CallbackData: "admin_poster"},
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

func (h *PosterHandler) Edit(ctx context.Context, b *bot.Bot, chatID int64, telegramID int64, eventID uint) {
	event, err := h.eventRepo.GetByID(eventID)
	if err != nil {
		log.Printf("failed to get event: %v", err)
		return
	}

	status := "\u2705 Активно"
	if !event.IsActive {
		status = "\U0001F534 Скрыто"
	}

	text := fmt.Sprintf("\u270F\uFE0F Редактировать: «%s»\n\n\U0001F4DD %s\n\U0001F4C5 %s, %s\u2013%s\n\U0001F4B0 %.0f \u20BD\n%s",
		event.Title, event.Description, event.EventDate.Format("2 January 2006"), event.TimeFrom, event.TimeTo, event.Price, status)

	toggleLabel := "\U0001F441 Скрыть из афиши"
	if !event.IsActive {
		toggleLabel = "\U0001F441 Показать в афише"
	}

	keyboard := [][]models.InlineKeyboardButton{
		{
			{Text: "\U0001F4DD Изменить название", CallbackData: fmt.Sprintf("admin_edit_ev_title_%d", eventID)},
		},
		{
			{Text: "\U0001F4C4 Изменить описание", CallbackData: fmt.Sprintf("admin_edit_ev_desc_%d", eventID)},
		},
		{
			{Text: "\U0001F5BC Заменить фото", CallbackData: fmt.Sprintf("admin_edit_ev_photo_%d", eventID)},
		},
		{
			{Text: "\U0001F4C5 Изменить дату/время", CallbackData: fmt.Sprintf("admin_edit_ev_dt_%d", eventID)},
		},
		{
			{Text: "\U0001F4B0 Изменить цену", CallbackData: fmt.Sprintf("admin_edit_ev_price_%d", eventID)},
		},
		{
			{Text: toggleLabel, CallbackData: fmt.Sprintf("admin_edit_ev_toggle_%d", eventID)},
		},
		{
			{Text: "\U0001F5D1 Удалить мероприятие", CallbackData: fmt.Sprintf("admin_edit_ev_delete_%d", eventID)},
		},
		{
			{Text: "\U0001F519 Назад", CallbackData: "admin_poster_list"},
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

func isValidTime(t string) bool {
	parts := strings.Split(t, ":")
	if len(parts) != 2 {
		return false
	}
	h, err1 := strconv.Atoi(parts[0])
	m, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil {
		return false
	}
	return h >= 0 && h <= 23 && m >= 0 && m <= 59
}

func dateOnly(value time.Time) time.Time {
	return time.Date(value.Year(), value.Month(), value.Day(), 0, 0, 0, 0, time.Local)
}
