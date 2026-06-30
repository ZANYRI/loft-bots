package admin

import (
	"context"
	"fmt"
	"log"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"loft-bots/internal/repository"
	"loft-bots/internal/state"
)

type SettingsHandler struct {
	settingsRepo *repository.SettingsRepo
	fsm          *state.FSM
}

func NewSettingsHandler(settingsRepo *repository.SettingsRepo, fsm *state.FSM) *SettingsHandler {
	return &SettingsHandler{
		settingsRepo: settingsRepo,
		fsm:          fsm,
	}
}

func (h *SettingsHandler) Show(ctx context.Context, b *bot.Bot, chatID int64) {
	paymentPhone, _ := h.settingsRepo.Get("payment_phone")
	loftName, _ := h.settingsRepo.Get("loft_name")

	phone := "+7 (XXX) XXX-XX-XX"
	name := "Название лофта"
	if paymentPhone != nil {
		phone = paymentPhone.Value
	}
	if loftName != nil {
		name = loftName.Value
	}

	text := fmt.Sprintf("\u2699\uFE0F Настройки\n\n\U0001F4F2 Номера для оплаты: %s\n\U0001F3E0 Название лофта: %s",
		phone, name)

	keyboard := [][]models.InlineKeyboardButton{
		{{Text: "\u270F\uFE0F Изменить номера для оплаты", CallbackData: "admin_settings_phone"}},
		{
			{Text: "\u270F\uFE0F Изменить название лофта", CallbackData: "admin_settings_name"},
		},
		{
			{Text: "\U0001F519 Назад", CallbackData: "admin_main_menu"},
		},
	}

	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:    chatID,
		Text:      text,
		ReplyMarkup: &models.InlineKeyboardMarkup{
			InlineKeyboard: keyboard,
		},
	})
}

func (h *SettingsHandler) StartEditPhone(ctx context.Context, b *bot.Bot, chatID int64, telegramID int64) {
	current, _ := h.settingsRepo.Get("payment_phone")
	currentValue := "+7 (XXX) XXX-XX-XX"
	if current != nil {
		currentValue = current.Value
	}

	h.fsm.SetState(telegramID, "admin", "admin:settings:phone", nil)
	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID: chatID,
		Text:   fmt.Sprintf("Введите новый номер телефона для оплаты:\nТекущий: %s", currentValue),
	})
}

func (h *SettingsHandler) StartEditName(ctx context.Context, b *bot.Bot, chatID int64, telegramID int64) {
	current, _ := h.settingsRepo.Get("loft_name")
	currentValue := "Название лофта"
	if current != nil {
		currentValue = current.Value
	}

	h.fsm.SetState(telegramID, "admin", "admin:settings:name", nil)
	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID: chatID,
		Text:   fmt.Sprintf("Введите новое название лофта:\nТекущее: %s", currentValue),
	})
}

func (h *SettingsHandler) HandleNewPhone(ctx context.Context, b *bot.Bot, chatID int64, telegramID int64, phone string) {
	if err := h.settingsRepo.Set("payment_phone", phone); err != nil {
		log.Printf("failed to update phone: %v", err)
		SendAdminError(ctx, b, chatID)
		return
	}

	h.fsm.ClearState(telegramID, "admin")
	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID: chatID,
		Text:   "\u2705 Номер телефона обновлён!",
	})
}

func (h *SettingsHandler) HandleNewName(ctx context.Context, b *bot.Bot, chatID int64, telegramID int64, name string) {
	if err := h.settingsRepo.Set("loft_name", name); err != nil {
		log.Printf("failed to update loft name: %v", err)
		SendAdminError(ctx, b, chatID)
		return
	}

	h.fsm.ClearState(telegramID, "admin")
	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID: chatID,
		Text:   "\u2705 Название лофта обновлено!",
	})
}

func SendAdminError(ctx context.Context, b *bot.Bot, chatID int64) {
	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID: chatID,
		Text:   "\u274C Произошла ошибка. Пожалуйста, попробуйте позже.",
	})
}
