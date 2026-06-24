package client

import (
	"context"
	"fmt"
	"log"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"loft-bots/internal/db"
	"loft-bots/internal/repository"
	"loft-bots/internal/state"
)

type ReservationHandler struct {
	reservationRepo *repository.ReservationRepo
	rentalPriceRepo *repository.RentalPriceRepo
	userRepo        *repository.UserRepo
	fsm             *state.FSM
}

func NewReservationHandler(
	reservationRepo *repository.ReservationRepo,
	rentalPriceRepo *repository.RentalPriceRepo,
	userRepo *repository.UserRepo,
	fsm *state.FSM,
) *ReservationHandler {
	return &ReservationHandler{
		reservationRepo: reservationRepo,
		rentalPriceRepo: rentalPriceRepo,
		userRepo:        userRepo,
		fsm:             fsm,
	}
}

func (h *ReservationHandler) Start(ctx context.Context, b *bot.Bot, chatID int64, telegramID int64) {
	h.fsm.SetState(telegramID, "client", "reservation:pick_month", nil)
	h.showMonthPicker(ctx, b, chatID)
}

func (h *ReservationHandler) showMonthPicker(ctx context.Context, b *bot.Bot, chatID int64) {
	now := time.Now()
	months := []string{
		"Январь", "Февраль", "Март", "Апрель", "Май", "Июнь",
		"Июль", "Август", "Сентябрь", "Октябрь", "Ноябрь", "Декабрь",
	}

	keyboard := make([][]models.InlineKeyboardButton, 0)
	var row []models.InlineKeyboardButton

	currentMonth := now.Month()
	currentYear := now.Year()

	for i := 0; i < 6; i++ {
		m := time.Month(((int(currentMonth)-1)+i)%12 + 1)
		y := currentYear
		if int(currentMonth)+i > 12 {
			y++
		}
		label := fmt.Sprintf("%s %d", months[m-1], y)
		cb := fmt.Sprintf("res_month_%d_%d", m, y)
		row = append(row, models.InlineKeyboardButton{
			Text:         label,
			CallbackData: cb,
		})
		if (i+1)%3 == 0 {
			keyboard = append(keyboard, row)
			row = make([]models.InlineKeyboardButton, 0)
		}
	}
	if len(row) > 0 {
		keyboard = append(keyboard, row)
	}

	keyboard = append(keyboard, []models.InlineKeyboardButton{
		{Text: "\U0001F519 Назад", CallbackData: "main_menu"},
	})

	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID: chatID,
		Text:   "\U0001F4C5 Выберите месяц:",
		ReplyMarkup: &models.InlineKeyboardMarkup{
			InlineKeyboard: keyboard,
		},
	})
}

func (h *ReservationHandler) HandleMonthPick(ctx context.Context, b *bot.Bot, chatID int64, telegramID int64, month int, year int) {
	h.fsm.SetState(telegramID, "client", "reservation:pick_date", map[string]interface{}{
		"month": month,
		"year":  year,
	})
	h.showDatePicker(ctx, b, chatID, month, year)
}

func (h *ReservationHandler) showDatePicker(ctx context.Context, b *bot.Bot, chatID int64, month int, year int) {
	now := time.Now()
	monthName := time.Month(month).String()

	firstDay := time.Date(year, time.Month(month), 1, 0, 0, 0, 0, time.Local)
	lastDay := firstDay.AddDate(0, 1, -1).Day()

	keyboard := make([][]models.InlineKeyboardButton, 0)
	var row []models.InlineKeyboardButton

	for day := 1; day <= lastDay; day++ {
		date := time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.Local)
		if date.Before(now.Truncate(24 * time.Hour)) {
			continue
		}

		bookings, _ := h.reservationRepo.GetBookedSlots(date)
		isFullyBooked := h.isDateFullyBooked(date, bookings)

		label := fmt.Sprintf("%2d", day)
		if isFullyBooked {
			label = fmt.Sprintf("\u26AB%2d", day)
		} else {
			label = fmt.Sprintf("\u2705%2d", day)
		}

		row = append(row, models.InlineKeyboardButton{
			Text:         label,
			CallbackData: fmt.Sprintf("res_date_%d_%d_%d", day, month, year),
		})

		if len(row) == 5 {
			keyboard = append(keyboard, row)
			row = make([]models.InlineKeyboardButton, 0)
		}
	}
	if len(row) > 0 {
		keyboard = append(keyboard, row)
	}

	keyboard = append(keyboard, []models.InlineKeyboardButton{
		{Text: "\u26AB \u2014 занято,  \u2705 \u2014 свободно", CallbackData: "noop"},
	})
	keyboard = append(keyboard, []models.InlineKeyboardButton{
		{Text: "\U0001F519 Назад", CallbackData: "reservation_start"},
	})

	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID: chatID,
		Text:   fmt.Sprintf("\U0001F4C5 %s %d \u2014 выберите дату:", monthName, year),
		ReplyMarkup: &models.InlineKeyboardMarkup{
			InlineKeyboard: keyboard,
		},
	})
}

func (h *ReservationHandler) isDateFullyBooked(date time.Time, bookings []db.Reservation) bool {
	if len(bookings) == 0 {
		return false
	}

	slots := []string{"10:00", "11:00", "12:00", "13:00", "14:00", "15:00", "16:00", "17:00", "18:00", "19:00", "20:00", "21:00"}
	for _, s := range slots {
		available := true
		for _, b := range bookings {
			if b.TimeFrom <= s && b.TimeTo > s {
				available = false
				break
			}
		}
		if available {
			return false
		}
	}
	return true
}

func (h *ReservationHandler) HandleDatePick(ctx context.Context, b *bot.Bot, chatID int64, telegramID int64, day, month, year int) {
	h.fsm.UpdateData(telegramID, "client", map[string]interface{}{
		"date": fmt.Sprintf("%d-%02d-%02d", year, month, day),
	})
	h.fsm.SetState(telegramID, "client", "reservation:pick_time_from", nil)
	h.showTimePicker(ctx, b, chatID, "from")
}

func (h *ReservationHandler) showTimePicker(ctx context.Context, b *bot.Bot, chatID int64, pickerType string) {
	times := []string{"10:00", "11:00", "12:00", "13:00", "14:00", "15:00", "16:00", "17:00", "18:00", "19:00", "20:00", "21:00"}
	prefix := "res_time_" + pickerType + "_"

	keyboard := make([][]models.InlineKeyboardButton, 0)
	var row []models.InlineKeyboardButton

	for i, t := range times {
		row = append(row, models.InlineKeyboardButton{
			Text:         t,
			CallbackData: prefix + t,
		})
		if (i+1)%4 == 0 || i == len(times)-1 {
			keyboard = append(keyboard, row)
			row = make([]models.InlineKeyboardButton, 0)
		}
	}

	pickerText := "\U0001F550 Выберите время начала:"
	if pickerType == "to" {
		pickerText = "\U0001F550 Выберите время окончания:\n(минимум 1 час от начала)"
	}

	keyboard = append(keyboard, []models.InlineKeyboardButton{
		{Text: "\U0001F519 Назад", CallbackData: "reservation_start"},
	})

	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID: chatID,
		Text:   pickerText,
		ReplyMarkup: &models.InlineKeyboardMarkup{
			InlineKeyboard: keyboard,
		},
	})
}

func (h *ReservationHandler) HandleTimeFrom(ctx context.Context, b *bot.Bot, chatID int64, telegramID int64, timeFrom string) {
	h.fsm.UpdateData(telegramID, "client", map[string]interface{}{
		"time_from": timeFrom,
	})
	h.fsm.SetState(telegramID, "client", "reservation:pick_time_to", nil)
	h.showTimePickerTo(ctx, b, chatID, telegramID, timeFrom)
}

func (h *ReservationHandler) showTimePickerTo(ctx context.Context, b *bot.Bot, chatID int64, telegramID int64, timeFrom string) {
	times := []string{"11:00", "12:00", "13:00", "14:00", "15:00", "16:00", "17:00", "18:00", "19:00", "20:00", "21:00", "22:00", "23:00"}

	_, data, _ := h.fsm.GetState(telegramID, "client")
	dateStr, _ := data["date"].(string)
	date, _ := time.Parse("2006-01-02", dateStr)
	bookings, _ := h.reservationRepo.GetBookedSlots(date)

	keyboard := make([][]models.InlineKeyboardButton, 0)
	var row []models.InlineKeyboardButton
	idx := 0

	for _, t := range times {
		if t <= timeFrom {
			continue
		}

		available := true
		for _, b := range bookings {
			if b.TimeFrom < t && b.TimeTo > timeFrom {
				available = false
				break
			}
		}
		if !available {
			continue
		}

		row = append(row, models.InlineKeyboardButton{
			Text:         t,
			CallbackData: "res_time_to_" + t,
		})
		idx++

		if idx%4 == 0 {
			keyboard = append(keyboard, row)
			row = make([]models.InlineKeyboardButton, 0)
		}
	}
	if len(row) > 0 {
		keyboard = append(keyboard, row)
	}

	keyboard = append(keyboard, []models.InlineKeyboardButton{
		{Text: "\U0001F519 Назад", CallbackData: "reservation_start"},
	})

	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID: chatID,
		Text:   "\U0001F550 Выберите время окончания:\n(минимум 1 час от начала)",
		ReplyMarkup: &models.InlineKeyboardMarkup{
			InlineKeyboard: keyboard,
		},
	})
}

func (h *ReservationHandler) HandleTimeTo(ctx context.Context, b *bot.Bot, chatID int64, telegramID int64, timeTo string) {
	_, data, _ := h.fsm.GetState(telegramID, "client")
	timeFrom, _ := data["time_from"].(string)
	dateStr, _ := data["date"].(string)

	date, _ := time.Parse("2006-01-02", dateStr)
	dayType := h.getDayType(date)

	rentalPrice, err := h.rentalPriceRepo.GetByDayType(dayType)
	if err != nil {
		log.Printf("failed to get rental price: %v", err)
		SendErrorMessage(ctx, b, chatID)
		return
	}

	hours := calcHours(timeFrom, timeTo)
	totalPrice := float64(hours) * rentalPrice.PricePerHour

	dayTypeStr := "будний день"
	if dayType == "weekend" {
		dayTypeStr = "выходной день"
	}

	h.fsm.SetState(telegramID, "client", "reservation:confirm", map[string]interface{}{
		"date":         dateStr,
		"time_from":    timeFrom,
		"time_to":      timeTo,
		"day_type":     dayType,
		"hours":        hours,
		"price_per_hour": rentalPrice.PricePerHour,
		"total_price":  totalPrice,
	})

	text := fmt.Sprintf("\u2705 Проверьте детали бронирования:\n\n\U0001F4C5 Дата: %s (%s)\n\U0001F550 Время: %s \u2013 %s\n\u23F1 Продолжительность: %d часа(ов)\n\U0001F4B0 Стоимость: %.0f \u20BD (%.0f \u20BD/час)",
		date.Format("2 January 2006"), dayTypeStr, timeFrom, timeTo, hours, totalPrice, rentalPrice.PricePerHour)

	keyboard := [][]models.InlineKeyboardButton{
		{
			{Text: "\u2705 Подтвердить", CallbackData: "reservation_confirm"},
			{Text: "\U0001F519 Изменить", CallbackData: "reservation_start"},
		},
	}

	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID: chatID,
		Text:   text,
		ReplyMarkup: &models.InlineKeyboardMarkup{
			InlineKeyboard: keyboard,
		},
	})
}

func (h *ReservationHandler) Confirm(ctx context.Context, b *bot.Bot, chatID int64, telegramID int64) {
	_, data, err := h.fsm.GetState(telegramID, "client")
	if err != nil {
		log.Printf("no reservation data found: %v", err)
		SendErrorMessage(ctx, b, chatID)
		return
	}

	user, err := h.userRepo.FindOrCreate(telegramID, "")
	if err != nil {
		log.Printf("failed to find user: %v", err)
		SendErrorMessage(ctx, b, chatID)
		return
	}

	dateStr, _ := data["date"].(string)
	date, _ := time.Parse("2006-01-02", dateStr)
	timeFrom, _ := data["time_from"].(string)
	timeTo, _ := data["time_to"].(string)
	dayType, _ := data["day_type"].(string)
	pricePerHour, _ := data["price_per_hour"].(float64)
	totalPrice, _ := data["total_price"].(float64)

	reservation := &db.Reservation{
		UserID:       user.ID,
		Date:         date,
		TimeFrom:     timeFrom,
		TimeTo:       timeTo,
		DayType:      dayType,
		PricePerHour: pricePerHour,
		TotalPrice:   totalPrice,
		Status:       "pending",
	}

	if err := h.reservationRepo.Create(reservation); err != nil {
		log.Printf("failed to create reservation: %v", err)
		SendErrorMessage(ctx, b, chatID)
		return
	}

	h.fsm.UpdateData(telegramID, "client", map[string]interface{}{
		"reservation_id": reservation.ID,
	})

	h.PromptMenuOrPayment(ctx, b, chatID, telegramID)
}

func (h *ReservationHandler) PromptMenuOrPayment(ctx context.Context, b *bot.Bot, chatID int64, telegramID int64) {
	keyboard := [][]models.InlineKeyboardButton{
		{
			{Text: "\U0001F355 Посмотреть меню", CallbackData: "menu_categories"},
			{Text: "\u27A1 Продолжить без меню", CallbackData: "go_to_payment"},
		},
	}

	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID: chatID,
		Text:   "\U0001F37D Хотите добавить что-нибудь из меню к вашему заказу?",
		ReplyMarkup: &models.InlineKeyboardMarkup{
			InlineKeyboard: keyboard,
		},
	})
}

func (h *ReservationHandler) getDayType(date time.Time) string {
	weekday := date.Weekday()
	if weekday == time.Saturday || weekday == time.Sunday {
		return "weekend"
	}
	return "weekday"
}

func (h *ReservationHandler) HandleCallback(ctx context.Context, b *bot.Bot, chatID int64, telegramID int64, data string) {
	parts := strings.Split(data, "_")
	if len(parts) < 3 {
		return
	}

	switch parts[1] {
	case "month":
		if len(parts) >= 4 {
			month, _ := strconv.Atoi(parts[2])
			year, _ := strconv.Atoi(parts[3])
			h.HandleMonthPick(ctx, b, chatID, telegramID, month, year)
		}
	case "date":
		if len(parts) >= 5 {
			day, _ := strconv.Atoi(parts[2])
			month, _ := strconv.Atoi(parts[3])
			year, _ := strconv.Atoi(parts[4])
			h.HandleDatePick(ctx, b, chatID, telegramID, day, month, year)
		}
	case "time":
		if len(parts) >= 4 {
			if parts[2] == "from" {
				h.HandleTimeFrom(ctx, b, chatID, telegramID, parts[3])
			} else if parts[2] == "to" {
				h.HandleTimeTo(ctx, b, chatID, telegramID, parts[3])
			}
		}
	}
}

func calcHours(timeFrom, timeTo string) int {
	fromParts := strings.Split(timeFrom, ":")
	toParts := strings.Split(timeTo, ":")
	fromHour, _ := strconv.Atoi(fromParts[0])
	toHour, _ := strconv.Atoi(toParts[0])
	fromMin, _ := strconv.Atoi(fromParts[1])
	toMin, _ := strconv.Atoi(toParts[1])

	hours := float64(toHour-fromHour) + float64(toMin-fromMin)/60.0
	return int(math.Ceil(hours))
}
