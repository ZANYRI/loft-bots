package admin

import (
	"context"
	"fmt"
	"log"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"loft-bots/internal/db"
	"loft-bots/internal/repository"
)

type ScheduleHandler struct {
	reservationRepo *repository.ReservationRepo
}

func NewScheduleHandler(reservationRepo *repository.ReservationRepo) *ScheduleHandler {
	return &ScheduleHandler{
		reservationRepo: reservationRepo,
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
		log.Printf("failed to get reservations: %v", err)
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

	for i, r := range reservations {
		dayTypeStr := "будний"
		if r.DayType == "weekend" {
			dayTypeStr = "выходной"
		}
		icon := statusIcons[r.Status]
		if icon == "" {
			icon = "\u2753"
		}

		text += fmt.Sprintf("\U0001F4C5 %s (%s \u2014 %s)\n\U0001F550 %s \u2013 %s\n\U0001F464 @%s | #%05d\n\U0001F4B0 %.0f \u20BD | %s %s\n",
			r.Date.Format("2 January 2006"), r.Date.Weekday().String()[:2], dayTypeStr,
			r.TimeFrom, r.TimeTo, r.User.Username, r.ID, r.TotalPrice, icon, r.Status)

		if i < len(reservations)-1 {
			text += "\n"
		}
	}

	keyboard := [][]models.InlineKeyboardButton{
		{{Text: "\U0001F519 Назад", CallbackData: "admin_schedule"}},
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
