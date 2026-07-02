package admin

import (
	"context"
	"fmt"
	"loft-bots/internal/logger"
	"strings"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"loft-bots/internal/metrics"
	"loft-bots/internal/notify"
	"loft-bots/internal/repository"
)

type OrdersHandler struct {
	orderRepo       *repository.OrderRepo
	eventRepo       *repository.EventRepo
	reservationRepo *repository.ReservationRepo
	settingsRepo    *repository.SettingsRepo
	clientBot       *bot.Bot
}

func NewOrdersHandler(orderRepo *repository.OrderRepo, eventRepo *repository.EventRepo, reservationRepo *repository.ReservationRepo, settingsRepo *repository.SettingsRepo, clientBot *bot.Bot) *OrdersHandler {
	return &OrdersHandler{
		orderRepo:       orderRepo,
		eventRepo:       eventRepo,
		reservationRepo: reservationRepo,
		settingsRepo:    settingsRepo,
		clientBot:       clientBot,
	}
}

func (h *OrdersHandler) HandleConfirm(ctx context.Context, b *bot.Bot, chatID int64, orderID uint) {
	order, err := h.orderRepo.GetByID(orderID)
	if err != nil {
		logger.Printf("failed to get order: %v", err)
		return
	}
	newStatus := "confirmed"
	if order.Reservation != nil && order.TotalPrice < order.Reservation.TotalPrice {
		newStatus = "prepaid"
	}
	if err := h.orderRepo.UpdateStatus(orderID, newStatus); err != nil {
		logger.Printf("failed to confirm order: %v", err)
		return
	}
	if order.ReservationID != nil {
		if err := h.reservationRepo.UpdateStatus(*order.ReservationID, "confirmed"); err != nil {
			logger.Printf("failed to confirm reservation: %v", err)
			return
		}
	}

	message := h.settingValue("message_payment_confirmed", "✅ Оплата по заказу #{order_id} подтверждена!")
	values := map[string]string{"order_id": fmt.Sprintf("%05d", orderID)}
	if order.Event != nil {
		message = h.settingValue("message_payment_confirmed", "✅ Оплата подтверждена! Ждем вас {date}, {time} — {title} по адресу: г. Пятигорск, ул. Мира 155А.\n\n🤝 Приходи не один! Перешли это сообщение друзьям. Если кто-то из них тоже купит билет на эту игру, мы бесплатно сделаем вам скидку 50% на кальян на ваш стол или скидку на покупку билетов к следующему мероприятию. Просто скажите на баре, с кем вы пришли!")
		values["date"] = order.Event.EventDate.Format("02.01.2006")
		values["time"] = order.Event.TimeFrom
		values["title"] = order.Event.Title
	}
	notify.SendToUser(ctx, h.clientBot, order.User.TelegramID, renderMessage(message, values))
	notify.DeleteOrderMessages(ctx, b, orderID)
	metrics.OrdersResolvedTotal.WithLabelValues("confirmed").Inc()
	logger.Info("order confirmed", "order_id", orderID)
	if order.ReservationID != nil {
		if setting, err := h.settingsRepo.Get("offer_pdf_url"); err == nil && setting != nil && strings.TrimSpace(setting.Value) != "" {
			if _, err := h.clientBot.SendDocument(ctx, &bot.SendDocumentParams{
				ChatID:   order.User.TelegramID,
				Document: &models.InputFileString{Data: strings.TrimSpace(setting.Value)},
				Caption:  "Договор-оферта",
			}); err != nil {
				logger.Printf("failed to send offer pdf: %v", err)
			}
		}
	}

	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID: chatID,
		Text:   fmt.Sprintf("\u2705 Заказ #%05d подтверждён", orderID),
	})
}

func (h *OrdersHandler) HandleReject(ctx context.Context, b *bot.Bot, chatID int64, orderID uint) {
	order, err := h.orderRepo.GetByID(orderID)
	if err != nil {
		logger.Printf("failed to get order: %v", err)
		return
	}
	if order.Status == "pending" && order.EventID != nil {
		quantity := order.TicketQuantity
		if quantity < 1 {
			quantity = 1
		}
		if err := h.eventRepo.ReleasePlaces(*order.EventID, quantity); err != nil {
			logger.Printf("failed to return ticket places: %v", err)
			return
		}
	}
	if err := h.orderRepo.UpdateStatus(orderID, "cancelled"); err != nil {
		logger.Printf("failed to reject order: %v", err)
		return
	}
	if order.ReservationID != nil {
		if err := h.reservationRepo.Cancel(*order.ReservationID); err != nil {
			logger.Printf("failed to cancel reservation: %v", err)
			return
		}
	}

	order, err = h.orderRepo.GetByID(orderID)
	if err != nil {
		logger.Printf("failed to get order: %v", err)
		return
	}

	contact := "@admin"
	if setting, err := h.settingsRepo.Get("admin_contact"); err == nil && setting != nil && setting.Value != "" {
		contact = setting.Value
	}
	rejectMessage := h.settingValue("message_payment_rejected", "❌ По вашему заказу #{order_id} оплата не была подтверждена.\nЕсли это ошибка, напишите администратору: {admin_contact}")
	notify.SendToUser(ctx, h.clientBot, order.User.TelegramID, renderMessage(rejectMessage, map[string]string{
		"order_id":      fmt.Sprintf("%05d", orderID),
		"admin_contact": contact,
	}))
	notify.DeleteOrderMessages(ctx, b, orderID)
	metrics.OrdersResolvedTotal.WithLabelValues("rejected").Inc()
	logger.Info("order rejected", "order_id", orderID)

	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID: chatID,
		Text:   fmt.Sprintf("\u274C Заказ #%05d отклонён", orderID),
	})
}

func (h *OrdersHandler) settingValue(key, fallback string) string {
	setting, err := h.settingsRepo.Get(key)
	if err != nil || setting == nil || strings.TrimSpace(setting.Value) == "" {
		return fallback
	}
	return setting.Value
}

func renderMessage(template string, values map[string]string) string {
	result := template
	for key, value := range values {
		result = strings.ReplaceAll(result, "{"+key+"}", value)
	}
	return result
}

func (h *OrdersHandler) ShowNewOrders(ctx context.Context, b *bot.Bot, chatID int64) {
	orders, err := h.orderRepo.GetPending()
	if err != nil {
		logger.Printf("failed to get pending orders: %v", err)
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
		orderType := "\U0001F37D Доп. Услуги и Меню"
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
