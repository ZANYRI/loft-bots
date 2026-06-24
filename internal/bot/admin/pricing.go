package admin

import (
	"context"
	"fmt"
	"log"
	"strconv"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"loft-bots/internal/repository"
	"loft-bots/internal/state"
)

type PricingHandler struct {
	rentalPriceRepo *repository.RentalPriceRepo
	fsm             *state.FSM
}

func NewPricingHandler(rentalPriceRepo *repository.RentalPriceRepo, fsm *state.FSM) *PricingHandler {
	return &PricingHandler{
		rentalPriceRepo: rentalPriceRepo,
		fsm:             fsm,
	}
}

func (h *PricingHandler) Show(ctx context.Context, b *bot.Bot, chatID int64) {
	weekday, err := h.rentalPriceRepo.GetByDayType("weekday")
	if err != nil {
		log.Printf("failed to get weekday price: %v", err)
		return
	}

	weekend, err := h.rentalPriceRepo.GetByDayType("weekend")
	if err != nil {
		log.Printf("failed to get weekend price: %v", err)
		return
	}

	text := fmt.Sprintf("\U0001F4B0 Тарифы на аренду лофта\n\n\U0001F4C5 Будние дни (Пн \u2013 Пт):  %.0f \u20BD / час\n\U0001F389 Выходные дни (Сб \u2013 Вс): %.0f \u20BD / час",
		weekday.PricePerHour, weekend.PricePerHour)

	keyboard := [][]models.InlineKeyboardButton{
		{
			{Text: "\u270F\uFE0F Изменить тариф \u2014 будни", CallbackData: "admin_price_weekday"},
		},
		{
			{Text: "\u270F\uFE0F Изменить тариф \u2014 выходные", CallbackData: "admin_price_weekend"},
		},
		{
			{Text: "\U0001F519 Назад", CallbackData: "admin_main_menu"},
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

func (h *PricingHandler) StartEdit(ctx context.Context, b *bot.Bot, chatID int64, telegramID int64, dayType string) {
	price, err := h.rentalPriceRepo.GetByDayType(dayType)
	if err != nil {
		log.Printf("failed to get price: %v", err)
		return
	}

	dayName := "будние дни"
	if dayType == "weekend" {
		dayName = "выходные дни"
	}

	h.fsm.SetState(telegramID, "admin", "admin:price:"+dayType, map[string]interface{}{
		"day_type": dayType,
	})

	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID: chatID,
		Text:   fmt.Sprintf("Введите новую цену за час аренды в %s (\u20BD):\nТекущая цена: %.0f \u20BD", dayName, price.PricePerHour),
	})
}

func (h *PricingHandler) HandleNewPrice(ctx context.Context, b *bot.Bot, chatID int64, telegramID int64, priceStr string) {
	price, err := strconv.ParseFloat(priceStr, 64)
	if err != nil {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: chatID,
			Text:   "Неверный формат. Введите число:",
		})
		return
	}

	stateStr, data, _ := h.fsm.GetState(telegramID, "admin")
	dayType, _ := data["day_type"].(string)

	dayName := "будние дни"
	if dayType == "weekend" {
		dayName = "выходные дни"
	}

	h.fsm.SetState(telegramID, "admin", stateStr, map[string]interface{}{
		"day_type": dayType,
		"new_price": price,
	})

	text := fmt.Sprintf("Новая цена: %.0f \u20BD/час (%s)", price, dayName)
	keyboard := [][]models.InlineKeyboardButton{
		{
			{Text: "\u2705 Сохранить", CallbackData: "admin_price_save"},
			{Text: "\u274C Отмена", CallbackData: "admin_pricing"},
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

func (h *PricingHandler) Save(ctx context.Context, b *bot.Bot, chatID int64, telegramID int64) {
	_, data, err := h.fsm.GetState(telegramID, "admin")
	if err != nil {
		log.Printf("no price data: %v", err)
		return
	}

	dayType, _ := data["day_type"].(string)
	newPrice, _ := data["new_price"].(float64)

	if err := h.rentalPriceRepo.Update(dayType, newPrice); err != nil {
		log.Printf("failed to update price: %v", err)
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: chatID,
			Text:   "\u274C Ошибка при сохранении.",
		})
		return
	}

	h.fsm.ClearState(telegramID, "admin")
	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID: chatID,
		Text:   "\u2705 Цена обновлена! Новый тариф применяется ко всем будущим бронированиям.",
	})
}
