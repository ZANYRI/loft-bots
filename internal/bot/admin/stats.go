package admin

import (
	"context"
	"fmt"
	"loft-bots/internal/logger"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"loft-bots/internal/repository"
)

type StatsHandler struct {
	orderRepo       *repository.OrderRepo
	reservationRepo *repository.ReservationRepo
}

func NewStatsHandler(orderRepo *repository.OrderRepo, reservationRepo *repository.ReservationRepo) *StatsHandler {
	return &StatsHandler{
		orderRepo:       orderRepo,
		reservationRepo: reservationRepo,
	}
}

func (h *StatsHandler) Show(ctx context.Context, b *bot.Bot, chatID int64) {
	keyboard := [][]models.InlineKeyboardButton{
		{
			{Text: "За сегодня", CallbackData: "admin_stats_today"},
			{Text: "За неделю", CallbackData: "admin_stats_week"},
		},
		{
			{Text: "За месяц", CallbackData: "admin_stats_month"},
			{Text: "За всё время", CallbackData: "admin_stats_all"},
		},
		{
			{Text: "\U0001F519 Назад", CallbackData: "admin_main_menu"},
		},
	}

	b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID: chatID,
		Text:   "\U0001F4CA Статистика",
		ReplyMarkup: &models.InlineKeyboardMarkup{
			InlineKeyboard: keyboard,
		},
	})
}

func (h *StatsHandler) ShowPeriod(ctx context.Context, b *bot.Bot, chatID int64, period string) {
	stats, err := h.orderRepo.GetStats(period, nil, nil)
	if err != nil {
		logger.Printf("failed to get stats: %v", err)
		return
	}

	periodLabel := map[string]string{
		"today": "сегодня",
		"week":  "неделю",
		"month": "месяц",
		"all":   "всё время",
	}

	totalRevenue, _ := stats["total_revenue"].(float64)
	totalOrders, _ := stats["total_orders"].(int64)
	confirmed, _ := stats["confirmed"].(int64)
	pending, _ := stats["pending"].(int64)
	cancelled, _ := stats["cancelled"].(int64)
	uniqueUsers, _ := stats["unique_users"].(int64)
	ticketsRevenue, _ := stats["tickets_revenue"].(float64)
	rentalsRevenue, _ := stats["rentals_revenue"].(float64)
	menuRevenue, _ := stats["menu_revenue"].(float64)

	text := fmt.Sprintf("\U0001F4CA Статистика за %s\n\n\U0001F4B0 Общий доход: %.0f \u20BD\n   \u251C Билеты: %.0f \u20BD\n   \u251C Бронирования: %.0f \u20BD\n   \u2514 Доп. Услуги и Меню: %.0f \u20BD\n\n\U0001F4E6 Заказов всего: %d\n   \u251C Подтверждено: %d\n   \u251C Ожидает: %d\n   \u2514 Отменено: %d\n\n\U0001F464 Уникальных клиентов: %d",
		periodLabel[period], totalRevenue, ticketsRevenue, rentalsRevenue, menuRevenue,
		totalOrders, confirmed, pending, cancelled, uniqueUsers)

	keyboard := [][]models.InlineKeyboardButton{
		{{Text: "\U0001F519 Назад", CallbackData: "admin_stats"}},
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
