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

func (r *OrderRepo) SetReviewDueAt(id uint, dueAt time.Time) error {
	return r.db.Model(&db.Order{}).Where("id = ?", id).Update("review_due_at", dueAt).Error
}

func (r *OrderRepo) GetDueReviewRequests(now time.Time) ([]db.Order, error) {
	var orders []db.Order
	err := r.db.Preload("User").
		Where("review_due_at IS NOT NULL AND review_due_at <= ? AND review_sent_at IS NULL", now).
		Where("status IN ?", []string{"confirmed", "prepaid"}).
		Order("review_due_at ASC").
		Find(&orders).Error
	return orders, err
}

func (r *OrderRepo) MarkReviewRequested(id uint, sentAt time.Time) error {
	return r.db.Model(&db.Order{}).Where("id = ? AND review_sent_at IS NULL", id).Update("review_sent_at", sentAt).Error
}

func (r *OrderRepo) PrepaidByReservationID(reservationID uint) (float64, error) {
	var total float64
	err := r.db.Model(&db.Order{}).
		Where("reservation_id = ? AND status IN ?", reservationID, []string{"prepaid", "confirmed"}).
		Select("COALESCE(SUM(total_price), 0)").
		Scan(&total).Error
	return total, err
}

func (r *OrderRepo) CancelByReservationID(reservationID uint) error {
	return r.db.Model(&db.Order{}).
		Where("reservation_id = ? AND status != ?", reservationID, "cancelled").
		Update("status", "cancelled").Error
}

func (r *OrderRepo) GetStats(period string, from, to *time.Time) (map[string]interface{}, error) {
	query := r.db.Model(&db.Order{}).Joins("LEFT JOIN reservations ON reservations.id = orders.reservation_id")
	now := time.Now()

	if from != nil || to != nil {
		if from != nil {
			query = query.Where("orders.created_at >= ?", *from)
		}
		if to != nil {
			query = query.Where("orders.created_at < ?", to.AddDate(0, 0, 1))
		}
	} else {
		switch period {
		case "today":
			query = query.Where("orders.created_at >= ?", now.Truncate(24*time.Hour))
		case "week":
			query = query.Where("orders.created_at >= ?", now.AddDate(0, 0, -7))
		case "month":
			query = query.Where("orders.created_at >= ?", time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location()))
		}
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
		COALESCE(SUM(CASE WHEN orders.status IN ('confirmed', 'prepaid') AND (orders.reservation_id IS NULL OR reservations.status != 'cancelled') THEN orders.total_price ELSE 0 END), 0) as total_revenue,
		COUNT(*) as total_orders,
		COALESCE(SUM(CASE WHEN orders.status IN ('confirmed', 'prepaid') AND (orders.reservation_id IS NULL OR reservations.status != 'cancelled') THEN 1 ELSE 0 END), 0) as confirmed,
		COALESCE(SUM(CASE WHEN orders.status = 'pending' THEN 1 ELSE 0 END), 0) as pending,
		COALESCE(SUM(CASE WHEN orders.status = 'cancelled' THEN 1 ELSE 0 END), 0) as cancelled,
		COUNT(DISTINCT orders.user_id) as unique_users
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
	revenueQuery := r.db.Model(&db.Order{}).Joins("LEFT JOIN reservations ON reservations.id = orders.reservation_id")
	revenueQuery = revenueQuery.Where("orders.status IN ?", []string{"confirmed", "prepaid"}).Where("orders.reservation_id IS NULL OR reservations.status != ?", "cancelled")
	if from != nil {
		revenueQuery = revenueQuery.Where("orders.created_at >= ?", *from)
	} else if period == "today" {
		revenueQuery = revenueQuery.Where("orders.created_at >= ?", now.Truncate(24*time.Hour))
	} else if period == "week" {
		revenueQuery = revenueQuery.Where("orders.created_at >= ?", now.AddDate(0, 0, -7))
	} else if period == "month" {
		revenueQuery = revenueQuery.Where("orders.created_at >= ?", time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location()))
	}
	if to != nil {
		revenueQuery = revenueQuery.Where("orders.created_at < ?", to.AddDate(0, 0, 1))
	}
	revenueQuery.Select(`CASE WHEN orders.event_id IS NOT NULL THEN 'tickets' WHEN orders.reservation_id IS NOT NULL THEN 'rentals' ELSE 'menu' END as type, COALESCE(SUM(orders.total_price), 0) as total`).Group("type").Scan(&revenueByType)

	for _, rbt := range revenueByType {
		stats[rbt.Type+"_revenue"] = rbt.Total
	}

	manualQuery := r.db.Model(&db.Reservation{}).
		Where("user_id IS NULL AND status != ?", "cancelled")
	if from != nil {
		manualQuery = manualQuery.Where("date >= ?", from.Format("2006-01-02"))
	} else if period == "today" {
		manualQuery = manualQuery.Where("date = ?", now.Format("2006-01-02"))
	} else if period == "week" {
		manualQuery = manualQuery.Where("date >= ?", now.AddDate(0, 0, -7).Format("2006-01-02"))
	} else if period == "month" {
		manualQuery = manualQuery.Where("date >= ?", time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location()).Format("2006-01-02"))
	}
	if to != nil {
		manualQuery = manualQuery.Where("date <= ?", to.Format("2006-01-02"))
	}
	var manualRevenue float64
	if err := manualQuery.Select("COALESCE(SUM(paid_amount), 0)").Scan(&manualRevenue).Error; err != nil {
		return nil, err
	}
	stats["total_revenue"] = result.TotalRevenue + manualRevenue
	rentalRevenue, _ := stats["rentals_revenue"].(float64)
	stats["rentals_revenue"] = rentalRevenue + manualRevenue

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

func (r *OrderRepo) HasActiveEventTicket(userID uint) (bool, error) {
	var count int64
	err := r.db.Model(&db.Order{}).
		Joins("JOIN events ON events.id = orders.event_id").
		Where("orders.user_id = ? AND orders.status = ?", userID, "confirmed").
		Where("events.is_active = ? AND events.event_date >= ?", true, time.Now().Format("2006-01-02")).
		Count(&count).Error
	return count > 0, err
}
