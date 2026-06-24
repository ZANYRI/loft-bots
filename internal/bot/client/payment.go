package client

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"loft-bots/internal/db"
	"loft-bots/internal/notify"
	"loft-bots/internal/repository"
	"loft-bots/internal/state"
)

type PaymentHandler struct {
	orderRepo       *repository.OrderRepo
	menuItemRepo    *repository.MenuItemRepo
	settingsRepo    *repository.SettingsRepo
	userRepo        *repository.UserRepo
	eventRepo       *repository.EventRepo
	reservationRepo *repository.ReservationRepo
	fsm             *state.FSM
	adminBot        *bot.Bot
}

func NewPaymentHandler(
	orderRepo *repository.OrderRepo,
	menuItemRepo *repository.MenuItemRepo,
	settingsRepo *repository.SettingsRepo,
	userRepo *repository.UserRepo,
	eventRepo *repository.EventRepo,
	reservationRepo *repository.ReservationRepo,
	fsm *state.FSM,
	adminBot *bot.Bot,
) *PaymentHandler {
	return &PaymentHandler{
		orderRepo:       orderRepo,
		menuItemRepo:    menuItemRepo,
		settingsRepo:    settingsRepo,
		userRepo:        userRepo,
		eventRepo:       eventRepo,
		reservationRepo: reservationRepo,
		fsm:             fsm,
		adminBot:        adminBot,
	}
}

func (h *PaymentHandler) ShowPayment(ctx context.Context, b *bot.Bot, chatID int64, telegramID int64) {
	_, data, err := h.fsm.GetState(telegramID, "client")
	if err != nil {
		log.Printf("no payment data found: %v", err)
		SendErrorMessage(ctx, b, chatID)
		return
	}

	paymentPhone := "+7 (XXX) XXX-XX-XX"
	if setting, err := h.settingsRepo.Get("payment_phone"); err == nil {
		paymentPhone = setting.Value
	}

	text := "\U0001F4B3 Ваш заказ:\n\n"

	var totalPrice float64

	if eventID, ok := data["event_id"]; ok {
		event, err := h.eventRepo.GetByID(uint(eventID.(float64)))
		if err == nil {
			text += fmt.Sprintf("\U0001F39F Билет на «%s» \u2014 %.0f \u20BD\n", event.Title, event.Price)
			totalPrice += event.Price
		}
	}

	if _, ok := data["reservation_id"]; ok {
		totalFromData, _ := data["total_price"].(float64)
		menuTotal := h.calculateMenuTotal(data)
		reservationPrice := totalFromData - menuTotal
		text += fmt.Sprintf("\U0001F511 Бронирование лофта \u2014 %.0f \u20BD\n", reservationPrice)
		totalPrice += totalFromData
	}

	menuTotal := h.calculateMenuTotal(data)
	if menuTotal > 0 {
		text += fmt.Sprintf("\U0001F37D Меню \u2014 %.0f \u20BD\n", menuTotal)
	}

	text += fmt.Sprintf("\n\u2501\u2501\u2501\u2501\u2501\u2501\u2501\u2501\u2501\u2501\u2501\u2501\u2501\u2501\u2501\n\U0001F4B0 Итого: %.0f \u20BD\n\n", totalPrice)
	text += fmt.Sprintf("\U0001F4F2 Переведите сумму на номер:\n%s\n\n", paymentPhone)
	text += "После оплаты нажмите кнопку ниже \U0001F447"

	keyboard := [][]models.InlineKeyboardButton{
		{
			{Text: "\u2705 Я оплатил(а)", CallbackData: fmt.Sprintf("payment_done_%d", int(totalPrice))},
			{Text: "\u274C Отмена", CallbackData: "main_menu"},
		},
	}

	h.fsm.UpdateData(telegramID, "client", map[string]interface{}{
		"total_price":   totalPrice,
		"menu_total":    menuTotal,
		"payment_phone": paymentPhone,
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

func (h *PaymentHandler) HandlePaymentDone(ctx context.Context, b *bot.Bot, chatID int64, telegramID int64) {
	_, data, err := h.fsm.GetState(telegramID, "client")
	if err != nil {
		log.Printf("no payment data: %v", err)
		SendErrorMessage(ctx, b, chatID)
		return
	}

	user, err := h.userRepo.FindOrCreate(telegramID, "")
	if err != nil {
		log.Printf("failed to find user: %v", err)
		SendErrorMessage(ctx, b, chatID)
		return
	}

	totalPrice, _ := data["total_price"].(float64)
	menuTotal, _ := data["menu_total"].(float64)

	var eventID *uint
	if eid, ok := data["event_id"]; ok {
		id := uint(eid.(float64))
		eventID = &id
	}

	var reservationID *uint
	if rid, ok := data["reservation_id"]; ok {
		id := uint(rid.(float64))
		reservationID = &id
	}

	order := &db.Order{
		UserID:        user.ID,
		EventID:       eventID,
		ReservationID: reservationID,
		MenuTotal:     menuTotal,
		TotalPrice:    totalPrice,
		Status:        "pending",
		CreatedAt:     time.Now(),
	}

	if err := h.orderRepo.Create(order); err != nil {
		log.Printf("failed to create order: %v", err)
		SendErrorMessage(ctx, b, chatID)
		return
	}

	for key, val := range data {
		if len(key) > 5 && key[:5] == "cart_" {
			item := val.(map[string]interface{})
			menuItemID := uint(item["item_id"].(float64))
			qty := int(item["qty"].(float64))
			price, _ := item["price"].(float64)

			orderMenuItem := &db.OrderMenuItem{
				OrderID:      order.ID,
				MenuItemID:   menuItemID,
				Quantity:     qty,
				PriceAtOrder: price,
			}
			h.orderRepo.AddMenuItem(orderMenuItem)
		}
	}

	orderText := h.buildOrderText(order, data, user.Username)
	notify.SendToAdmins(ctx, h.adminBot, orderText, h.buildAdminKeyboard(order.ID))

	h.fsm.ClearState(telegramID, "client")

	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID: chatID,
		Text:   fmt.Sprintf("\u23F3 Отлично! Ваш платёж отправлен на проверку.\nМы уведомим вас, как только подтвердим получение средств.\n\nНомер вашего заказа: #%05d", order.ID),
	})
}

func (h *PaymentHandler) buildOrderText(order *db.Order, data map[string]interface{}, username string) string {
	text := fmt.Sprintf("\U0001F514 Новый платёж на проверку!\n\n\U0001F464 Пользователь: @%s\n\U0001F4CB Заказ #%05d:\n", username, order.ID)

	if eventID, ok := data["event_id"]; ok {
		event, err := h.eventRepo.GetByID(uint(eventID.(float64)))
		if err == nil {
			text += fmt.Sprintf("   \U0001F39F Билет «%s» \u2014 %.0f \u20BD\n", event.Title, event.Price)
		}
	}

	if _, ok := data["reservation_id"]; ok {
		reservationTotal, _ := data["total_price"].(float64)
		menuTotal := h.calculateMenuTotal(data)
		text += fmt.Sprintf("   \U0001F511 Бронирование лофта \u2014 %.0f \u20BD\n", reservationTotal-menuTotal)
	}

	for key, val := range data {
		if len(key) > 5 && key[:5] == "cart_" {
			item := val.(map[string]interface{})
			name, _ := item["name"].(string)
			price, _ := item["price"].(float64)
			qty := int(item["qty"].(float64))
			text += fmt.Sprintf("   \U0001F37D %s \u00d7 %d \u2014 %.0f \u20BD\n", name, qty, price*float64(qty))
		}
	}

	text += fmt.Sprintf("\n\U0001F4B0 Итого: %.0f \u20BD\n\U0001F4C5 Время заявки: %s",
		order.TotalPrice, order.CreatedAt.Format("2 January 2006, 15:04"))

	return text
}

func (h *PaymentHandler) buildAdminKeyboard(orderID uint) *models.InlineKeyboardMarkup {
	return &models.InlineKeyboardMarkup{
		InlineKeyboard: [][]models.InlineKeyboardButton{
			{
				{Text: "\u2705 Деньги получены", CallbackData: fmt.Sprintf("admin_confirm_%d", orderID)},
				{Text: "\u274C Деньги не получены", CallbackData: fmt.Sprintf("admin_reject_%d", orderID)},
			},
		},
	}
}

func (h *PaymentHandler) calculateMenuTotal(data map[string]interface{}) float64 {
	var total float64
	for key, val := range data {
		if len(key) > 5 && key[:5] == "cart_" {
			item := val.(map[string]interface{})
			price, _ := item["price"].(float64)
			qty := int(item["qty"].(float64))
			total += price * float64(qty)
		}
	}
	return total
}
