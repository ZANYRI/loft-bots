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
		Text: `Планируете свой праздник или ищете место для корпоратива? 🥳
Профессиональный звук, отсутствие ограничений по шуму после 23:00, вместимость до 20 человек.

📅 Выберите месяц:`,
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
	monthName := russianMonthName(time.Month(month))

	firstDay := time.Date(year, time.Month(month), 1, 0, 0, 0, 0, time.Local)
	lastDay := firstDay.AddDate(0, 1, -1).Day()

	keyboard := make([][]models.InlineKeyboardButton, 0)
	var row []models.InlineKeyboardButton

	for day := 1; day <= lastDay; day++ {
		date := time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.Local)
		if date.Before(now.Truncate(24 * time.Hour)) {
			continue
		}

		bookings, _ := h.reservationRepo.GetBookedSlotsAround(date)
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
	slots := hourlySlots(0, 23)
	for _, from := range slots {
		for _, to := range slots {
			if isReservationIntervalAvailable(date, from, to, bookings) {
				return false
			}
		}
	}
	return true
}

func (h *ReservationHandler) HandleDatePick(ctx context.Context, b *bot.Bot, chatID int64, telegramID int64, day, month, year int) {
	date := time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.Local)
	if date.Before(time.Now().Truncate(24 * time.Hour)) {
		b.SendMessage(ctx, &bot.SendMessageParams{ChatID: chatID, Text: "Нельзя забронировать лофт задним числом. Выберите сегодняшнюю или будущую дату."})
		h.showMonthPicker(ctx, b, chatID)
		return
	}
	h.fsm.SetState(telegramID, "client", "reservation:pick_time_from", map[string]interface{}{
		"date": fmt.Sprintf("%d-%02d-%02d", year, month, day),
	})
	h.showTimePicker(ctx, b, chatID, telegramID)
}

func (h *ReservationHandler) showTimePicker(ctx context.Context, b *bot.Bot, chatID int64, telegramID int64) {
	times := hourlySlots(0, 23)
	_, data, _ := h.fsm.GetState(telegramID, "client")
	dateStr, _ := data["date"].(string)
	date, _ := time.Parse("2006-01-02", dateStr)
	bookings, _ := h.reservationRepo.GetBookedSlotsAround(date)

	keyboard := make([][]models.InlineKeyboardButton, 0)
	var row []models.InlineKeyboardButton
	idx := 0

	for _, t := range times {
		if !isStartHourAvailable(date, t, bookings) {
			continue
		}
		row = append(row, models.InlineKeyboardButton{
			Text:         t,
			CallbackData: "res_time_from_" + t,
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
	if idx == 0 {
		keyboard = append(keyboard, []models.InlineKeyboardButton{{Text: "Нет свободного времени", CallbackData: "noop"}})
	}

	keyboard = append(keyboard, []models.InlineKeyboardButton{
		{Text: "\U0001F519 Назад", CallbackData: "reservation_start"},
	})

	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID: chatID,
		Text:   "\U0001F550 Выберите время начала:",
		ReplyMarkup: &models.InlineKeyboardMarkup{
			InlineKeyboard: keyboard,
		},
	})
}

func (h *ReservationHandler) HandleTimeFrom(ctx context.Context, b *bot.Bot, chatID int64, telegramID int64, timeFrom string) {
	_, data, err := h.fsm.GetState(telegramID, "client")
	if err != nil {
		SendErrorMessage(ctx, b, chatID)
		return
	}
	data["time_from"] = timeFrom
	h.fsm.SetState(telegramID, "client", "reservation:pick_time_to", data)
	h.showTimePickerTo(ctx, b, chatID, telegramID, timeFrom)
}

func (h *ReservationHandler) showTimePickerTo(ctx context.Context, b *bot.Bot, chatID int64, telegramID int64, timeFrom string) {
	times := hourlySlots(0, 23)

	_, data, _ := h.fsm.GetState(telegramID, "client")
	dateStr, _ := data["date"].(string)
	date, _ := time.Parse("2006-01-02", dateStr)
	bookings, _ := h.reservationRepo.GetBookedSlotsAround(date)

	keyboard := make([][]models.InlineKeyboardButton, 0)
	var row []models.InlineKeyboardButton
	idx := 0

	for _, t := range times {
		if minutesOfDay(t) <= minutesOfDay(timeFrom) {
			continue
		}
		if !isReservationIntervalAvailable(date, timeFrom, t, bookings) {
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
	if idx == 0 {
		keyboard = append(keyboard, []models.InlineKeyboardButton{{Text: "Нет доступного времени окончания", CallbackData: "noop"}})
	}
	if hasNextDayEndTime(date, timeFrom, bookings) {
		keyboard = append(keyboard, []models.InlineKeyboardButton{{Text: "Следующий день", CallbackData: "res_next_day"}})
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

func (h *ReservationHandler) showNextDayTimePickerTo(ctx context.Context, b *bot.Bot, chatID int64, telegramID int64) {
	_, data, _ := h.fsm.GetState(telegramID, "client")
	timeFrom, _ := data["time_from"].(string)
	dateStr, _ := data["date"].(string)
	date, _ := time.Parse("2006-01-02", dateStr)
	bookings, _ := h.reservationRepo.GetBookedSlotsAround(date)

	keyboard := make([][]models.InlineKeyboardButton, 0)
	var row []models.InlineKeyboardButton
	idx := 0
	for _, t := range hourlySlots(0, 23) {
		if minutesOfDay(t) > minutesOfDay(timeFrom) {
			continue
		}
		if !isReservationIntervalAvailable(date, timeFrom, t, bookings) {
			break
		}
		row = append(row, models.InlineKeyboardButton{Text: t, CallbackData: "res_time_to_" + t})
		idx++
		if idx%4 == 0 {
			keyboard = append(keyboard, row)
			row = make([]models.InlineKeyboardButton, 0)
		}
	}
	if len(row) > 0 {
		keyboard = append(keyboard, row)
	}
	if idx == 0 {
		keyboard = append(keyboard, []models.InlineKeyboardButton{{Text: "Нет свободных часов", CallbackData: "noop"}})
	}
	keyboard = append(keyboard, []models.InlineKeyboardButton{{Text: "← Этот день", CallbackData: "res_time_from_" + timeFrom}})
	keyboard = append(keyboard, []models.InlineKeyboardButton{{Text: "\U0001F519 Назад", CallbackData: "reservation_start"}})

	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:      chatID,
		Text:        "\U0001F550 Выберите время окончания на следующий день:",
		ReplyMarkup: &models.InlineKeyboardMarkup{InlineKeyboard: keyboard},
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
		"date":           dateStr,
		"time_from":      timeFrom,
		"time_to":        timeTo,
		"day_type":       dayType,
		"hours":          hours,
		"price_per_hour": rentalPrice.PricePerHour,
		"total_price":    totalPrice,
	})

	endNote := ""
	if minutesOfDay(timeTo) <= minutesOfDay(timeFrom) {
		endNote = " следующего дня"
	}
	text := fmt.Sprintf("\u2705 Проверьте детали бронирования:\n\n\U0001F4C5 Дата: %s (%s)\n\U0001F550 Время: %s \u2013 %s%s\n\u23F1 Продолжительность: %d часа(ов)\n\U0001F4B0 Стоимость: %.0f \u20BD (%.0f \u20BD/час)",
		formatDateRU(date), dayTypeStr, timeFrom, timeTo, endNote, hours, totalPrice, rentalPrice.PricePerHour)

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

func (h *ReservationHandler) Confirm(ctx context.Context, b *bot.Bot, chatID int64, telegramID int64) bool {
	_, data, err := h.fsm.GetState(telegramID, "client")
	if err != nil {
		log.Printf("no reservation data found: %v", err)
		SendErrorMessage(ctx, b, chatID)
		return false
	}

	user, err := h.userRepo.FindOrCreate(telegramID, "")
	if err != nil {
		log.Printf("failed to find user: %v", err)
		SendErrorMessage(ctx, b, chatID)
		return false
	}

	dateStr, _ := data["date"].(string)
	date, _ := time.Parse("2006-01-02", dateStr)
	date = time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, time.Local)
	if date.Before(time.Now().Truncate(24 * time.Hour)) {
		b.SendMessage(ctx, &bot.SendMessageParams{ChatID: chatID, Text: "Нельзя забронировать лофт задним числом. Выберите сегодняшнюю или будущую дату."})
		h.showMonthPicker(ctx, b, chatID)
		return false
	}
	timeFrom, _ := data["time_from"].(string)
	timeTo, _ := data["time_to"].(string)
	bookings, err := h.reservationRepo.GetBookedSlotsAround(date)
	if err != nil {
		log.Printf("failed to get booked slots: %v", err)
		SendErrorMessage(ctx, b, chatID)
		return false
	}
	if !isReservationIntervalAvailable(date, timeFrom, timeTo, bookings) {
		b.SendMessage(ctx, &bot.SendMessageParams{ChatID: chatID, Text: "Это время уже занято. Выберите другой интервал."})
		h.showTimePicker(ctx, b, chatID, telegramID)
		return false
	}
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
		return false
	}

	h.fsm.UpdateData(telegramID, "client", map[string]interface{}{
		"reservation_id": reservation.ID,
	})

	return true
}

func (h *ReservationHandler) PromptMenuOrPayment(ctx context.Context, b *bot.Bot, chatID int64, telegramID int64) {
	keyboard := [][]models.InlineKeyboardButton{
		{
			{Text: "\U0001F355 Посмотреть Меню", CallbackData: "menu_categories"},
			{Text: "\u27A1 Продолжить без Меню", CallbackData: "go_to_payment"},
		},
	}

	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID: chatID,
		Text:   "\U0001F37D Хотите добавить Меню к вашему заказу?",
		ReplyMarkup: &models.InlineKeyboardMarkup{
			InlineKeyboard: keyboard,
		},
	})
}

func (h *ReservationHandler) getDayType(date time.Time) string {
	weekday := date.Weekday()
	if weekday == time.Friday || weekday == time.Saturday || weekday == time.Sunday {
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
	case "next":
		if len(parts) >= 3 && parts[2] == "day" {
			h.showNextDayTimePickerTo(ctx, b, chatID, telegramID)
		}
	}
}

func calcHours(timeFrom, timeTo string) int {
	fromMinutes := minutesOfDay(timeFrom)
	toMinutes := minutesOfDay(timeTo)
	if toMinutes <= fromMinutes {
		toMinutes += 24 * 60
	}
	hours := float64(toMinutes-fromMinutes) / 60.0
	return int(math.Ceil(hours))
}

func isStartHourAvailable(date time.Time, timeFrom string, bookings []db.Reservation) bool {
	return isReservationIntervalAvailable(date, timeFrom, nextHour(timeFrom), bookings)
}

func hasNextDayEndTime(date time.Time, timeFrom string, bookings []db.Reservation) bool {
	for _, timeTo := range hourlySlots(0, 23) {
		if minutesOfDay(timeTo) > minutesOfDay(timeFrom) {
			continue
		}
		if isReservationIntervalAvailable(date, timeFrom, timeTo, bookings) {
			return true
		}
	}
	return false
}

func nextHour(value string) string {
	minutes := minutesOfDay(value) + 60
	if minutes >= 24*60 {
		minutes -= 24 * 60
	}
	return fmt.Sprintf("%02d:00", minutes/60)
}

func isReservationIntervalAvailable(date time.Time, timeFrom, timeTo string, bookings []db.Reservation) bool {
	start := minutesOfDay(timeFrom)
	end := minutesOfDay(timeTo)
	if end <= start {
		end += 24 * 60
	}
	baseDate := dateOnly(date)
	for _, booking := range bookings {
		offset := int(dateOnly(booking.Date).Sub(baseDate).Hours()/24) * 24 * 60
		bookingStart := offset + minutesOfDay(booking.TimeFrom)
		bookingEnd := offset + minutesOfDay(booking.TimeTo)
		if bookingEnd <= bookingStart {
			bookingEnd += 24 * 60
		}
		// 1-hour gap after each reservation
		bookingEnd += 60
		if start < bookingEnd && end > bookingStart {
			return false
		}
	}
	return true
}

func dateOnly(value time.Time) time.Time {
	return time.Date(value.Year(), value.Month(), value.Day(), 0, 0, 0, 0, time.Local)
}

func minutesOfDay(value string) int {
	parts := strings.Split(value, ":")
	if len(parts) != 2 {
		return 0
	}
	hour, _ := strconv.Atoi(parts[0])
	minute, _ := strconv.Atoi(parts[1])
	return hour*60 + minute
}

func hourlySlots(fromHour, toHour int) []string {
	slots := make([]string, 0, toHour-fromHour+1)
	for hour := fromHour; hour <= toHour; hour++ {
		slots = append(slots, fmt.Sprintf("%02d:00", hour))
	}
	return slots
}

func russianMonthName(month time.Month) string {
	months := []string{"Январь", "Февраль", "Март", "Апрель", "Май", "Июнь", "Июль", "Август", "Сентябрь", "Октябрь", "Ноябрь", "Декабрь"}
	if month < time.January || month > time.December {
		return ""
	}
	return months[month-1]
}
