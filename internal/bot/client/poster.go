package client

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"loft-bots/internal/db"
	"loft-bots/internal/repository"
)

type PosterHandler struct {
	eventRepo *repository.EventRepo
}

func NewPosterHandler(eventRepo *repository.EventRepo) *PosterHandler {
	return &PosterHandler{eventRepo: eventRepo}
}

func (h *PosterHandler) ShowList(ctx context.Context, b *bot.Bot, chatID int64) {
	events, err := h.eventRepo.GetActive()
	if err != nil {
		log.Printf("failed to get events: %v", err)
		SendErrorMessage(ctx, b, chatID)
		return
	}

	if len(events) == 0 {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: chatID,
			Text:   "\U0001F3AD В настоящее время нет активных мероприятий.",
		})
		return
	}

	h.showEvent(ctx, b, chatID, events, 0)
}

func (h *PosterHandler) showEvent(ctx context.Context, b *bot.Bot, chatID int64, events []db.Event, index int) {
	ev := events[index]

	text := fmt.Sprintf("\U0001F389 %s", ev.Title)
	if strings.TrimSpace(ev.Description) != "" {
		text += fmt.Sprintf("\n\n\U0001F4DD %s", ev.Description)
	}
	text += fmt.Sprintf("\n\n\U0001F4C5 Дата и время: %s, %s \u2013 %s\n\U0001F4B0 Цена билета: %.0f \u20BD",
		formatDateRU(ev.EventDate), ev.TimeFrom, ev.TimeTo, ev.Price)

	if ev.PlacesLeft == 0 {
		text += "\n🚫 Sold Out"
	} else {
		text += fmt.Sprintf("\n🔥 Осталось мест: %d из %d", ev.PlacesLeft, ev.TotalPlaces)
	}
	keyboard := h.buildPosterKeyboard(ev.ID, ev.PlacesLeft, index, len(events))

	params := &bot.SendMessageParams{
		ChatID:    chatID,
		Text:      text,
		ParseMode: models.ParseModeHTML,
		ReplyMarkup: &models.InlineKeyboardMarkup{
			InlineKeyboard: keyboard,
		},
	}

	if ev.ImageFileID != "" {
		b.SendPhoto(ctx, &bot.SendPhotoParams{
			ChatID:      chatID,
			Photo:       &models.InputFileString{Data: ev.ImageFileID},
			Caption:     text,
			ParseMode:   models.ParseModeHTML,
			ReplyMarkup: &models.InlineKeyboardMarkup{InlineKeyboard: keyboard},
		})
	} else {
		b.SendMessage(ctx, params)
	}
}

func formatDateRU(date time.Time) string {
	months := []string{"января", "февраля", "марта", "апреля", "мая", "июня", "июля", "августа", "сентября", "октября", "ноября", "декабря"}
	return fmt.Sprintf("%d %s %d", date.Day(), months[date.Month()-1], date.Year())
}

func (h *PosterHandler) buildPosterKeyboard(eventID uint, placesLeft, index, total int) [][]models.InlineKeyboardButton {
	keyboard := make([][]models.InlineKeyboardButton, 0)
	if placesLeft > 0 {
		keyboard = append(keyboard, []models.InlineKeyboardButton{{Text: "\U0001F39F Купить билет", CallbackData: fmt.Sprintf("buy_ticket_%d", eventID)}})
	}

	if total > 1 {
		navRow := make([]models.InlineKeyboardButton, 0)
		if index > 0 {
			navRow = append(navRow, models.InlineKeyboardButton{
				Text:         "\u25C0 Назад",
				CallbackData: fmt.Sprintf("poster_%d", index-1),
			})
		}
		if index < total-1 {
			navRow = append(navRow, models.InlineKeyboardButton{
				Text:         "Вперёд \u25B6",
				CallbackData: fmt.Sprintf("poster_%d", index+1),
			})
		}
		keyboard = append(keyboard, navRow)
	}

	keyboard = append(keyboard, []models.InlineKeyboardButton{
		{Text: "\U0001F519 Главное меню", CallbackData: "main_menu"},
	})

	return keyboard
}
