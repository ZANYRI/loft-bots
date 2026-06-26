package client

import (
	"context"
	"fmt"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"loft-bots/internal/repository"
)

type ProfileHandler struct {
	userRepo  *repository.UserRepo
	orderRepo *repository.OrderRepo
}

func NewProfileHandler(userRepo *repository.UserRepo, orderRepo *repository.OrderRepo) *ProfileHandler {
	return &ProfileHandler{
		userRepo:  userRepo,
		orderRepo: orderRepo,
	}
}

func (h *ProfileHandler) Show(ctx context.Context, b *bot.Bot, chatID int64, telegramID int64) {
	user, err := h.userRepo.FindOrCreate(telegramID, "")
	if err != nil {
		SendErrorMessage(ctx, b, chatID)
		return
	}

	orders, err := h.orderRepo.GetByUserID(user.ID)
	if err != nil {
		SendErrorMessage(ctx, b, chatID)
		return
	}

	text := fmt.Sprintf("\U0001F464 Ваш профиль\n\nПользователь: @%s\n\n\U0001F4CB История заказов:\n\n", user.Username)

	statusIcons := map[string]string{
		"pending":   "\u23F3",
		"confirmed": "\u2705",
		"cancelled": "\u274C",
		"refunded":  "\U0001F504",
	}
	statusText := map[string]string{
		"pending":   "Ожидает подтверждения",
		"confirmed": "Подтверждено",
		"cancelled": "Отменён",
		"refunded":  "Возврат средств",
	}

	if len(orders) == 0 {
		text += "У вас пока нет заказов."
	} else {
		for _, order := range orders {
			icon := statusIcons[order.Status]
			st := statusText[order.Status]

			var orderType string
			if order.EventID != nil && order.Event != nil {
				orderType = fmt.Sprintf("\U0001F39F Билет «%s»", order.Event.Title)
			} else if order.ReservationID != nil {
				orderType = fmt.Sprintf("\U0001F511 Бронирование %s, %s\u2013%s",
					order.CreatedAt.Format("2 January"), "10:00", "18:00")
			} else {
				orderType = "\U0001F37D Заказ из меню"
			}

			if order.MenuTotal > 0 {
				orderType += " + \U0001F37D Доп. Услуги и Меню"
			}

			text += fmt.Sprintf("#%05d | %s\n", order.ID, orderType)
			text += fmt.Sprintf("        \U0001F4C5 %s\n", order.CreatedAt.Format("2 January 2006"))
			text += fmt.Sprintf("        \U0001F4B0 %.0f \u20BD | %s %s\n\n", order.TotalPrice, icon, st)
		}
	}

	keyboard := [][]models.InlineKeyboardButton{
		{{Text: "\U0001F519 Главное меню", CallbackData: "main_menu"}},
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
