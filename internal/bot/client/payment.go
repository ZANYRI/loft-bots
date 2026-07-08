package client

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"loft-bots/internal/logger"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"loft-bots/internal/db"
	"loft-bots/internal/metrics"
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
		logger.Printf("no payment data found: %v", err)
		SendErrorMessage(ctx, b, chatID)
		return
	}

	paymentPhone := "+7 (XXX) XXX-XX-XX"
	if setting, err := h.settingsRepo.Get("payment_phone"); err == nil {
		paymentPhone = setting.Value
	}

	text := "\U0001F4B3 Ваш заказ:\n\n"

	var totalPrice float64

	var eventPaymentPhone string
	if eventID, ok := data["event_id"]; ok {
		event, err := h.eventRepo.GetByID(uint(eventID.(float64)))
		if err == nil {
			quantity, _ := data["ticket_quantity"].(float64)
			if quantity < 1 {
				quantity = 1
			}
			text += fmt.Sprintf("\U0001F39F Билет на «%s» × %d \u2014 %.0f \u20BD\n", event.Title, int(quantity), event.Price*quantity)
			totalPrice += event.Price * quantity
			if event.PaymentPhone != "" {
				eventPaymentPhone = event.PaymentPhone
			}
		}
	}

	reservationFullPrice := 0.0
	if _, ok := data["reservation_id"]; ok {
		totalFromData, _ := data["total_price"].(float64)
		reservationFullPrice = totalFromData
		prepaymentPercent := h.settingInt("prepayment_percent", 30)
		if prepaymentPercent < 0 {
			prepaymentPercent = 0
		}
		if prepaymentPercent > 100 {
			prepaymentPercent = 100
		}
		prepaymentAmount := reservationFullPrice * float64(prepaymentPercent) / 100
		text += fmt.Sprintf("\U0001F511 Бронирование лофта \u2014 %.0f \u20BD\n", reservationFullPrice)
		text += fmt.Sprintf("\U0001F4B5 Предоплата %d%% \u2014 %.0f \u20BD\n", prepaymentPercent, prepaymentAmount)
		totalPrice += prepaymentAmount
	}

	menuTotal := h.calculateMenuTotal(data)
	if menuTotal > 0 {
		for _, group := range []struct{ kind, title string }{{"menu", "🍽 Меню:"}, {"service", "✨ Дополнительные услуги:"}} {
			groupText := ""
			groupTotal := 0.0
			for key, val := range data {
				if len(key) <= 5 || key[:5] != "cart_" {
					continue
				}
				item, ok := val.(map[string]interface{})
				if !ok {
					continue
				}
				kind, _ := item["category_type"].(string)
				if kind == "" {
					kind = "menu"
				}
				if kind != group.kind {
					continue
				}
				name, _ := item["name"].(string)
				price, _ := item["price"].(float64)
				quantity, _ := item["qty"].(float64)
				subtotal := price * quantity
				groupTotal += subtotal
				groupText += fmt.Sprintf("   • %s × %d — %.0f ₽\n", name, int(quantity), subtotal)
			}
			if groupText != "" {
				text += group.title + "\n" + groupText + fmt.Sprintf("   Итого — %.0f ₽\n", groupTotal)
			}
		}
		totalPrice += menuTotal
	}

	customPhone, _ := data["custom_payment_phone"].(string)
	displayPhone := paymentPhone
	switch {
	case customPhone != "":
		displayPhone = customPhone
	case eventPaymentPhone != "":
		displayPhone = eventPaymentPhone
	}

	text += fmt.Sprintf("\n\u2501\u2501\u2501\u2501\u2501\u2501\u2501\u2501\u2501\u2501\u2501\u2501\u2501\u2501\u2501\n\U0001F4B0 Итого: %.0f \u20BD\n\n", totalPrice)
	text += fmt.Sprintf("\U0001F4F2 Переведите сумму на реквизиты:\n%s\n\n", displayPhone)
	text += "После оплаты отправьте фото или PDF-чек одним сообщением. \U0001F447"
	if _, isTicket := data["event_id"]; !isTicket {
		depositStr := "0"
		if setting, err := h.settingsRepo.Get("deposit_amount"); err == nil {
			depositStr = setting.Value
		}
		depositVal, _ := strconv.ParseFloat(depositStr, 64)
		if depositVal > 0 {
			text += fmt.Sprintf("\n\n\U0001F512 Залог: %.0f \u20BD (возвращается после уборки)", depositVal)
		}
	}

	keyboard := [][]models.InlineKeyboardButton{
		{
			{Text: "\u274C Отмена", CallbackData: "main_menu"},
		},
	}
	if _, isTicket := data["event_id"]; isTicket {
		extraRow := []models.InlineKeyboardButton{{Text: "\U0001F4DD Ввести другой номер", CallbackData: "payment_custom_phone"}}
		if customPhone != "" {
			extraRow = []models.InlineKeyboardButton{{Text: "\U0001F4DD \u2705 Другой номер введён", CallbackData: "noop"}}
		}
		keyboard = append([][]models.InlineKeyboardButton{extraRow}, keyboard...)
	}

	h.fsm.UpdateData(telegramID, "client", map[string]interface{}{
		"total_price":            totalPrice,
		"reservation_full_price": reservationFullPrice,
		"menu_total":             menuTotal,
		"payment_phone":          paymentPhone,
	})
	_, paymentData, err := h.fsm.GetState(telegramID, "client")
	if err == nil {
		h.fsm.SetState(telegramID, "client", "payment:receipt", paymentData)
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

func (h *PaymentHandler) HandlePaymentDone(ctx context.Context, b *bot.Bot, chatID int64, telegramID int64) {
	_, data, err := h.fsm.GetState(telegramID, "client")
	if err != nil {
		logger.Printf("no payment data: %v", err)
		SendErrorMessage(ctx, b, chatID)
		return
	}

	user, err := h.userRepo.FindOrCreate(telegramID, "")
	if err != nil {
		logger.Printf("failed to find user: %v", err)
		SendErrorMessage(ctx, b, chatID)
		return
	}

	totalPrice, _ := data["total_price"].(float64)
	menuTotal, _ := data["menu_total"].(float64)

	var eventID *uint
	ticketQuantity := 0
	if eid, ok := data["event_id"]; ok {
		id := uint(eid.(float64))
		eventID = &id
		if quantity, ok := data["ticket_quantity"].(float64); ok {
			ticketQuantity = int(quantity)
		}
		if ticketQuantity < 1 {
			ticketQuantity = 1
		}
		reserved, err := h.eventRepo.ReservePlaces(id, ticketQuantity)
		if err != nil || !reserved {
			b.SendMessage(ctx, &bot.SendMessageParams{ChatID: chatID, Text: "К сожалению, выбранное количество билетов уже закончилось."})
			return
		}
	}

	var reservationID *uint
	if rid, ok := data["reservation_id"]; ok {
		id := uint(rid.(float64))
		reservationID = &id
	}

	order := &db.Order{
		UserID:         user.ID,
		EventID:        eventID,
		ReservationID:  reservationID,
		MenuTotal:      menuTotal,
		TicketQuantity: ticketQuantity,
		ReceiptURL:     receiptURL(data),
		TotalPrice:     totalPrice,
		Status:         "pending",
		CreatedAt:      time.Now(),
	}

	if err := h.orderRepo.Create(order); err != nil {
		logger.Printf("failed to create order: %v", err)
		SendErrorMessage(ctx, b, chatID)
		return
	}

	orderType := "menu"
	switch {
	case eventID != nil:
		orderType = "ticket"
	case reservationID != nil:
		orderType = "reservation"
	}
	metrics.OrdersCreatedTotal.WithLabelValues(orderType).Inc()
	logger.Info("order created", "order_id", order.ID, "type", orderType, "total_price", order.TotalPrice, "user_id", user.ID)

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
	sentMessages := notify.SendToAdminsWithReceipt(ctx, h.adminBot, orderText, order.ReceiptURL, h.buildAdminKeyboard(order.ID))
	notify.RegisterOrderMessages(order.ID, sentMessages)

	h.fsm.ClearState(telegramID, "client")

	pendingMessage := h.settingValue("message_after_payment", "✅ Чек получен. Мы проверим оплату и скоро вернёмся с подтверждением.")
	depositAmount := h.settingValue("deposit_amount", "0")
	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID: chatID,
		Text: renderMessage(pendingMessage, map[string]string{
			"order_id": fmt.Sprintf("%05d", order.ID),
			"deposit":  depositAmount,
		}),
		ReplyMarkup: &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
			{{Text: "🏠 Главное меню", CallbackData: "main_menu"}},
		}},
	})

	if eventID != nil || reservationID != nil {
		corkFee := "300"
		if setting, err := h.settingsRepo.Get("cork_fee"); err == nil && setting != nil {
			corkFee = setting.Value
		}
		delayedMessage := h.settingValue("message_delayed_event", "Важная информация про наш бар!\n\n🍹 У нас действует система BYOB.\nПриносите любимые напитки с собой. Пробковый сбор составит {cork_fee}₽ за бутылку (лед и бокалы мы предоставим). Вы можете заранее заказать кальян или напиток со скидкой 15%. Он будет готов точно к вашему приходу.")
		delayMinutes := h.settingInt("message_delayed_event_minutes", 15)
		go func() {
			time.Sleep(time.Duration(delayMinutes) * time.Minute)
			b.SendMessage(context.Background(), &bot.SendMessageParams{ChatID: chatID, Text: renderMessage(delayedMessage, map[string]string{"cork_fee": corkFee})})
		}()
	}
	dueAt := reviewDueAtAfter(h.reviewBaseTime(eventID, reservationID), h.settingInt("message_review_day_offset", 1), h.settingValue("message_review_next_day_time", "12:00"))
	if err := h.orderRepo.SetReviewDueAt(order.ID, dueAt); err != nil {
		logger.Printf("failed to set review due time: order_id=%d err=%v", order.ID, err)
	}
}

func (h *PaymentHandler) reviewBaseTime(eventID, reservationID *uint) time.Time {
	if eventID != nil {
		if event, err := h.eventRepo.GetByID(*eventID); err == nil {
			return endDateTime(event.EventDate, event.TimeFrom, event.TimeTo)
		}
	}
	if reservationID != nil {
		if reservation, err := h.reservationRepo.GetByID(*reservationID); err == nil {
			return endDateTime(reservation.Date, reservation.TimeFrom, reservation.TimeTo)
		}
	}
	return time.Now()
}

func (h *PaymentHandler) RequestReceipt(ctx context.Context, b *bot.Bot, chatID int64, telegramID int64) {
	_, data, err := h.fsm.GetState(telegramID, "client")
	if err != nil {
		SendErrorMessage(ctx, b, chatID)
		return
	}
	h.fsm.SetState(telegramID, "client", "payment:receipt", data)

	text := "Пожалуйста, отправьте фото или скриншот чека одним сообщением. После этого заказ будет передан администратору на подтверждение."
	if _, isTicket := data["event_id"]; !isTicket {
		depositStr := "0"
		if setting, err := h.settingsRepo.Get("deposit_amount"); err == nil {
			depositStr = setting.Value
		}
		depositVal, _ := strconv.ParseFloat(depositStr, 64)
		if depositVal > 0 {
			text += fmt.Sprintf("\n\n\U0001F512 Залог: %.0f \u20BD (возвращается после уборки)", depositVal)
		}
	}

	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID: chatID,
		Text:   text,
		ReplyMarkup: &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
			{{Text: "❌ Отмена", CallbackData: "main_menu"}},
		}},
	})
}

func (h *PaymentHandler) HandleReceipt(ctx context.Context, b *bot.Bot, chatID int64, telegramID int64, fileID string, isDocument bool) {
	if url, err := saveTelegramReceipt(fileID); err == nil {
		h.fsm.UpdateData(telegramID, "client", map[string]interface{}{"receipt_url": url})
	} else {
		logger.Printf("failed to save receipt: %v", err)
	}
	h.HandlePaymentDone(ctx, b, chatID, telegramID)
}

func (h *PaymentHandler) settingValue(key, fallback string) string {
	setting, err := h.settingsRepo.Get(key)
	if err != nil || setting == nil || strings.TrimSpace(setting.Value) == "" {
		return fallback
	}
	return setting.Value
}

func (h *PaymentHandler) settingInt(key string, fallback int) int {
	value := h.settingValue(key, "")
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || parsed < 0 {
		return fallback
	}
	return parsed
}

func reviewDueAt(dayOffset int, nextDayTime string) time.Time {
	return reviewDueAtAfter(time.Now(), dayOffset, nextDayTime)
}

func reviewDueAtAfter(base time.Time, dayOffset int, nextDayTime string) time.Time {
	now := time.Now()
	if dayOffset < 0 {
		dayOffset = 1
	}
	hour, minute := 12, 0
	parts := strings.Split(strings.TrimSpace(nextDayTime), ":")
	if len(parts) >= 2 {
		if h, err := strconv.Atoi(parts[0]); err == nil && h >= 0 && h <= 23 {
			hour = h
		}
		if m, err := strconv.Atoi(parts[1]); err == nil && m >= 0 && m <= 59 {
			minute = m
		}
	}
	target := time.Date(base.Year(), base.Month(), base.Day(), hour, minute, 0, 0, base.Location()).AddDate(0, 0, dayOffset)
	if !target.After(now) {
		target = time.Date(now.Year(), now.Month(), now.Day(), hour, minute, 0, 0, now.Location()).AddDate(0, 0, 1)
	}
	return target
}

func endDateTime(date time.Time, timeFrom, timeTo string) time.Time {
	startMinutes := minutesOfDay(timeFrom)
	endMinutes := minutesOfDay(timeTo)
	endDate := dateOnly(date)
	if endMinutes <= startMinutes {
		endDate = endDate.AddDate(0, 0, 1)
	}
	return endDate.Add(time.Duration(endMinutes) * time.Minute)
}

func renderMessage(template string, values map[string]string) string {
	result := template
	for key, value := range values {
		result = strings.ReplaceAll(result, "{"+key+"}", value)
	}
	return result
}

func receiptURL(data map[string]interface{}) string {
	value, _ := data["receipt_url"].(string)
	return value
}
func saveTelegramReceipt(fileID string) (string, error) {
	token := os.Getenv("CLIENT_BOT_TOKEN")
	metaResponse, err := http.Get("https://api.telegram.org/bot" + token + "/getFile?file_id=" + fileID)
	if err != nil {
		return "", err
	}
	defer metaResponse.Body.Close()
	var meta struct {
		OK     bool `json:"ok"`
		Result struct {
			FilePath string `json:"file_path"`
		} `json:"result"`
	}
	if err := json.NewDecoder(metaResponse.Body).Decode(&meta); err != nil || !meta.OK {
		return "", fmt.Errorf("telegram getFile failed")
	}
	fileResponse, err := http.Get("https://api.telegram.org/file/bot" + token + "/" + meta.Result.FilePath)
	if err != nil {
		return "", err
	}
	defer fileResponse.Body.Close()
	if err := os.MkdirAll("uploads", 0755); err != nil {
		return "", err
	}
	random := make([]byte, 12)
	rand.Read(random)
	name := "receipt-" + hex.EncodeToString(random) + filepath.Ext(meta.Result.FilePath)
	destination, err := os.Create(filepath.Join("uploads", name))
	if err != nil {
		return "", err
	}
	defer destination.Close()
	if _, err := io.Copy(destination, fileResponse.Body); err != nil {
		return "", err
	}
	destination.Close()

	servedName := name
	if strings.EqualFold(filepath.Ext(name), ".pdf") {
		if pngName, err := convertPDFToPNG(name); err == nil {
			servedName = pngName
		} else {
			logger.Printf("failed to convert receipt pdf to image: %v", err)
		}
	}

	return strings.TrimRight(os.Getenv("WEBAPP_URL"), "/") + "/uploads/" + servedName, nil
}

// convertPDFToPNG renders the first page of an uploaded PDF receipt to a PNG
// (via poppler's pdftoppm) so admins see the receipt as an image instead of
// having to open a PDF link.
func convertPDFToPNG(pdfName string) (string, error) {
	pdfPath := filepath.Join("uploads", pdfName)
	outBase := strings.TrimSuffix(pdfPath, filepath.Ext(pdfPath))
	cmd := exec.Command("pdftoppm", "-png", "-r", "150", "-singlefile", pdfPath, outBase)
	if output, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("pdftoppm failed: %w (%s)", err, string(output))
	}
	return strings.TrimSuffix(pdfName, filepath.Ext(pdfName)) + ".png", nil
}

func (h *PaymentHandler) buildOrderText(order *db.Order, data map[string]interface{}, username string) string {
	text := fmt.Sprintf("\U0001F514 Новый платёж на проверку!\n\n\U0001F464 Пользователь: @%s\n\U0001F4CB Заказ #%05d:\n", username, order.ID)

	if eventID, ok := data["event_id"]; ok {
		event, err := h.eventRepo.GetByID(uint(eventID.(float64)))
		if err == nil {
			quantity, _ := data["ticket_quantity"].(float64)
			if quantity < 1 {
				quantity = 1
			}
			text += fmt.Sprintf("   \U0001F39F Билет «%s» × %d \u2014 %.0f \u20BD\n", event.Title, int(quantity), event.Price*quantity)
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
