package admin

import (
	"context"
	"fmt"
	"loft-bots/internal/logger"
	"strings"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"loft-bots/internal/db"
	"loft-bots/internal/notify"
	"loft-bots/internal/repository"
)

type ScheduleHandler struct {
	reservationRepo *repository.ReservationRepo
	orderRepo       *repository.OrderRepo
	settingsRepo    *repository.SettingsRepo
	clientBot       *bot.Bot
}

func NewScheduleHandler(reservationRepo *repository.ReservationRepo, orderRepo *repository.OrderRepo, settingsRepo *repository.SettingsRepo, clientBot *bot.Bot) *ScheduleHandler {
	return &ScheduleHandler{
		reservationRepo: reservationRepo,
		orderRepo:       orderRepo,
		settingsRepo:    settingsRepo,
		clientBot:       clientBot,
	}
}

func (h *ScheduleHandler) Show(ctx context.Context, b *bot.Bot, chatID int64) {
	keyboard := [][]models.InlineKeyboardButton{
		{
			{Text: "Сегодня", CallbackData: "admin_schedule_today"},
			{Text: "Эта неделя", CallbackData: "admin_schedule_week"},
		},
		{
			{Text: "Этот месяц", CallbackData: "admin_schedule_month"},
			{Text: "Все", CallbackData: "admin_schedule_all"},
		},
		{
			{Text: "\U0001F519 Назад", CallbackData: "admin_main_menu"},
		},
	}

	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID: chatID,
		Text:   "\U0001F4C5 Расписание бронирований",
		ReplyMarkup: &models.InlineKeyboardMarkup{
			InlineKeyboard: keyboard,
		},
	})
}

func (h *ScheduleHandler) ShowFiltered(ctx context.Context, b *bot.Bot, chatID int64, period string) {
	var reservations []db.Reservation
	var err error

	switch period {
	case "today":
		reservations, err = h.reservationRepo.GetFiltered("today")
	case "week":
		reservations, err = h.reservationRepo.GetFiltered("week")
	case "month":
		reservations, err = h.reservationRepo.GetFiltered("month")
	default:
		reservations, err = h.reservationRepo.GetAll()
	}

	if err != nil {
		logger.Printf("failed to get reservations: %v", err)
		return
	}

	if len(reservations) == 0 {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: chatID,
			Text:   "Бронирований нет.",
		})
		return
	}

	text := "\U0001F4C5 Расписание бронирований\n\n"

	statusIcons := map[string]string{
		"pending":   "\u23F3",
		"confirmed": "\u2705",
		"cancelled": "\u274C",
	}
	statusLabels := map[string]string{
		"pending":   "ожидает подтверждения",
		"confirmed": "подтверждено",
		"cancelled": "отменено",
	}

	for i, r := range reservations {
		dayTypeStr := "будний"
		if r.DayType == "weekend" {
			dayTypeStr = "выходной"
		}
		icon := statusIcons[r.Status]
		if icon == "" {
			icon = "\u2753"
		}
		statusLabel := statusLabels[r.Status]
		if statusLabel == "" {
			statusLabel = r.Status
		}

		text += fmt.Sprintf("\U0001F4C5 %s (%s — %s)\n\U0001F550 %s – %s\n\U0001F464 @%s | #%05d\n\U0001F4B0 %.0f \u20BD | %s %s\n",
			formatDateRU(r.Date), weekdayRU(r.Date), dayTypeStr,
			r.TimeFrom, r.TimeTo, r.User.Username, r.ID, r.TotalPrice, icon, statusLabel)

		if i < len(reservations)-1 {
			text += "\n"
		}
	}

	keyboard := [][]models.InlineKeyboardButton{
		{{Text: "\U0001F519 Назад", CallbackData: "admin_schedule"}},
	}
	for _, r := range reservations {
		if r.Status != "cancelled" {
			keyboard = append(keyboard, []models.InlineKeyboardButton{{Text: fmt.Sprintf("❌ Отменить бронь #%05d", r.ID), CallbackData: fmt.Sprintf("admin_res_cancel_%d", r.ID)}})
		}
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

func (h *ScheduleHandler) HandleCancel(ctx context.Context, b *bot.Bot, chatID int64, reservationID uint) {
	reservation, err := h.reservationRepo.GetByID(reservationID)
	if err != nil {
		logger.Printf("failed to get reservation: %v", err)
		b.SendMessage(ctx, &bot.SendMessageParams{ChatID: chatID, Text: "Бронь не найдена."})
		return
	}
	if reservation.Status == "cancelled" {
		b.SendMessage(ctx, &bot.SendMessageParams{ChatID: chatID, Text: fmt.Sprintf("Бронь #%05d уже отменена.", reservationID)})
		return
	}
	if err := h.reservationRepo.Cancel(reservationID); err != nil {
		logger.Printf("failed to cancel reservation: %v", err)
		b.SendMessage(ctx, &bot.SendMessageParams{ChatID: chatID, Text: "Не удалось отменить бронь."})
		return
	}
	if err := h.orderRepo.CancelByReservationID(reservationID); err != nil {
		logger.Printf("failed to cancel reservation orders: %v", err)
		b.SendMessage(ctx, &bot.SendMessageParams{ChatID: chatID, Text: "Бронь отменена, но связанные заказы не удалось отменить."})
		return
	}
	h.notifyReservationCancelled(ctx, reservation)
	b.SendMessage(ctx, &bot.SendMessageParams{ChatID: chatID, Text: fmt.Sprintf("✅ Бронь #%05d отменена.", reservationID)})
}

func (h *ScheduleHandler) notifyReservationCancelled(ctx context.Context, reservation *db.Reservation) {
	if reservation.UserID == nil || reservation.User.TelegramID == 0 {
		return
	}
	message := "❌ Ваша бронь на {date}, {time_from}–{time_to} была отменена администратором."
	if setting, err := h.settingsRepo.Get("message_reservation_cancelled"); err == nil && setting != nil && strings.TrimSpace(setting.Value) != "" {
		message = setting.Value
	}
	message = strings.ReplaceAll(message, "{date}", formatDateRU(reservation.Date))
	message = strings.ReplaceAll(message, "{time_from}", reservation.TimeFrom)
	message = strings.ReplaceAll(message, "{time_to}", reservation.TimeTo)
	notify.SendToUser(ctx, h.clientBot, reservation.User.TelegramID, message)
}

func formatDateRU(date time.Time) string {
	months := []string{"января", "февраля", "марта", "апреля", "мая", "июня", "июля", "августа", "сентября", "октября", "ноября", "декабря"}
	return fmt.Sprintf("%d %s %d", date.Day(), months[date.Month()-1], date.Year())
}

func weekdayRU(date time.Time) string {
	weekdays := []string{"вс", "пн", "вт", "ср", "чт", "пт", "сб"}
	return weekdays[date.Weekday()]
}
