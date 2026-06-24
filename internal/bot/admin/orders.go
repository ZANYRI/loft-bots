package admin

import (
	"context"
	"fmt"
	"log"

	"github.com/go-telegram/bot"

	"loft-bots/internal/notify"
	"loft-bots/internal/repository"
)

type OrdersHandler struct {
	orderRepo *repository.OrderRepo
	clientBot *bot.Bot
}

func NewOrdersHandler(orderRepo *repository.OrderRepo, clientBot *bot.Bot) *OrdersHandler {
	return &OrdersHandler{
		orderRepo: orderRepo,
		clientBot: clientBot,
	}
}

func (h *OrdersHandler) HandleConfirm(ctx context.Context, b *bot.Bot, chatID int64, orderID uint) {
	if err := h.orderRepo.UpdateStatus(orderID, "confirmed"); err != nil {
		log.Printf("failed to confirm order: %v", err)
		return
	}

	order, err := h.orderRepo.GetByID(orderID)
	if err != nil {
		log.Printf("failed to get order: %v", err)
		return
	}

	notify.SendToUser(ctx, h.clientBot, order.User.TelegramID,
		fmt.Sprintf("\u2705 Ваш платёж по заказу #%05d подтверждён!\nЖдём вас \U0001F389", orderID))

	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID: chatID,
		Text:   fmt.Sprintf("\u2705 Заказ #%05d подтверждён", orderID),
	})
}

func (h *OrdersHandler) HandleReject(ctx context.Context, b *bot.Bot, chatID int64, orderID uint) {
	if err := h.orderRepo.UpdateStatus(orderID, "cancelled"); err != nil {
		log.Printf("failed to reject order: %v", err)
		return
	}

	order, err := h.orderRepo.GetByID(orderID)
	if err != nil {
		log.Printf("failed to get order: %v", err)
		return
	}

	notify.SendToUser(ctx, h.clientBot, order.User.TelegramID,
		fmt.Sprintf("\u274C По вашему заказу #%05d оплата не была подтверждена.\nЕсли это ошибка \u2014 свяжитесь с нами.", orderID))

	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID: chatID,
		Text:   fmt.Sprintf("\u274C Заказ #%05d отклонён", orderID),
	})
}

func (h *OrdersHandler) ShowNewOrders(ctx context.Context, b *bot.Bot, chatID int64) {
	orders, err := h.orderRepo.GetPending()
	if err != nil {
		log.Printf("failed to get pending orders: %v", err)
		return
	}

	if len(orders) == 0 {
		b.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: chatID,
			Text:   "\u2705 Новых заказов нет.",
		})
		return
	}

	text := fmt.Sprintf("\U0001F514 Новых заказов: %d\n\n", len(orders))
	for _, o := range orders {
		orderType := "\U0001F37D Меню"
		if o.EventID != nil {
			orderType = "\U0001F39F Билет"
		} else if o.ReservationID != nil {
			orderType = "\U0001F511 Бронирование"
		}
		text += fmt.Sprintf("#%05d | %s | %s | %.0f \u20BD\n",
			o.ID, orderType, o.CreatedAt.Format("15:04 02.01"), o.TotalPrice)
	}

	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID: chatID,
		Text:   text,
	})
}
