package repository

import (
	"time"

	"gorm.io/gorm"

	"loft-bots/internal/db"
)

type OrderRepo struct {
	db *gorm.DB
}

func NewOrderRepo(db_ *gorm.DB) *OrderRepo {
	return &OrderRepo{db: db_}
}

func (r *OrderRepo) Create(order *db.Order) error {
	return r.db.Create(order).Error
}

func (r *OrderRepo) GetByID(id uint) (*db.Order, error) {
	var order db.Order
	err := r.db.Preload("User").Preload("Event").Preload("Reservation").Preload("MenuItems").First(&order, id).Error
	return &order, err
}

func (r *OrderRepo) GetByUserID(userID uint) ([]db.Order, error) {
	var orders []db.Order
	err := r.db.Where("user_id = ?", userID).Preload("Event").Preload("MenuItems").Order("created_at DESC").Find(&orders).Error
	return orders, err
}

func (r *OrderRepo) GetPending() ([]db.Order, error) {
	var orders []db.Order
	err := r.db.Where("status = ?", "pending").Preload("User").Preload("Event").Preload("Reservation").Preload("MenuItems").Order("created_at ASC").Find(&orders).Error
	return orders, err
}

func (r *OrderRepo) GetFiltered(status, period string) ([]db.Order, error) {
	query := r.db.Preload("User").Preload("Event").Preload("Reservation").Preload("MenuItems")

	switch period {
	case "today":
		today := time.Now().Truncate(24 * time.Hour)
		query = query.Where("created_at >= ?", today)
	case "week":
		weekAgo := time.Now().AddDate(0, 0, -7)
		query = query.Where("created_at >= ?", weekAgo)
	case "month":
		monthAgo := time.Now().AddDate(0, -1, 0)
		query = query.Where("created_at >= ?", monthAgo)
	}

	if status != "" {
		query = query.Where("status = ?", status)
	}

	var orders []db.Order
	err := query.Order("created_at DESC").Find(&orders).Error
	return orders, err
}

func (r *OrderRepo) UpdateStatus(id uint, status string) error {
	return r.db.Model(&db.Order{}).Where("id = ?", id).Update("status", status).Error
}

func (r *OrderRepo) GetStats(period string) (map[string]interface{}, error) {
	query := r.db.Model(&db.Order{})
	now := time.Now()

	switch period {
	case "today":
		query = query.Where("created_at >= ?", now.Truncate(24*time.Hour))
	case "week":
		query = query.Where("created_at >= ?", now.AddDate(0, 0, -7))
	case "month":
		query = query.Where("created_at >= ?", now.AddDate(0, -1, 0))
	}

	type StatsResult struct {
		TotalRevenue float64
		TotalOrders  int64
		Confirmed    int64
		Pending      int64
		Cancelled    int64
		UniqueUsers  int64
	}

	var result StatsResult
	query.Select(`
		COALESCE(SUM(total_price), 0) as total_revenue,
		COUNT(*) as total_orders,
		COALESCE(SUM(CASE WHEN status = 'confirmed' THEN 1 ELSE 0 END), 0) as confirmed,
		COALESCE(SUM(CASE WHEN status = 'pending' THEN 1 ELSE 0 END), 0) as pending,
		COALESCE(SUM(CASE WHEN status = 'cancelled' THEN 1 ELSE 0 END), 0) as cancelled,
		COUNT(DISTINCT user_id) as unique_users
	`).Scan(&result)

	stats := map[string]interface{}{
		"total_revenue": result.TotalRevenue,
		"total_orders":  result.TotalOrders,
		"confirmed":     result.Confirmed,
		"pending":       result.Pending,
		"cancelled":     result.Cancelled,
		"unique_users":  result.UniqueUsers,
	}

	var revenueByType []struct {
		Type  string
		Total float64
	}
	r.db.Raw(`
		SELECT
			CASE
				WHEN event_id IS NOT NULL THEN 'tickets'
				WHEN reservation_id IS NOT NULL THEN 'rentals'
				ELSE 'menu'
			END as type,
			COALESCE(SUM(total_price), 0) as total
		FROM orders
		GROUP BY type
	`).Scan(&revenueByType)

	for _, rbt := range revenueByType {
		stats[rbt.Type+"_revenue"] = rbt.Total
	}

	return stats, nil
}

func (r *OrderRepo) AddMenuItem(orderMenuItem *db.OrderMenuItem) error {
	return r.db.Create(orderMenuItem).Error
}

func (r *OrderRepo) CountByEventID(eventID uint) (int64, error) {
	var count int64
	err := r.db.Model(&db.Order{}).Where("event_id = ? AND status = ?", eventID, "confirmed").Count(&count).Error
	return count, err
}
