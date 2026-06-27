package repository

import (
	"time"

	"gorm.io/gorm"

	"loft-bots/internal/db"
)

type ReservationRepo struct {
	db *gorm.DB
}

func NewReservationRepo(db_ *gorm.DB) *ReservationRepo {
	return &ReservationRepo{db: db_}
}

func (r *ReservationRepo) Create(reservation *db.Reservation) error {
	return r.db.Create(reservation).Error
}

func (r *ReservationRepo) GetByID(id uint) (*db.Reservation, error) {
	var reservation db.Reservation
	err := r.db.Preload("User").First(&reservation, id).Error
	return &reservation, err
}

func (r *ReservationRepo) GetByDate(date time.Time) ([]db.Reservation, error) {
	var reservations []db.Reservation
	err := r.db.Where("date = ?", date.Format("2006-01-02")).Find(&reservations).Error
	return reservations, err
}

func (r *ReservationRepo) GetBookedSlots(date time.Time) ([]db.Reservation, error) {
	var reservations []db.Reservation
	err := r.db.Where("date = ? AND status != ?", date.Format("2006-01-02"), "cancelled").Find(&reservations).Error
	return reservations, err
}

func (r *ReservationRepo) GetBookedSlotsAround(date time.Time) ([]db.Reservation, error) {
	var reservations []db.Reservation
	from := date.AddDate(0, 0, -1).Format("2006-01-02")
	to := date.AddDate(0, 0, 1).Format("2006-01-02")
	err := r.db.Where("date >= ? AND date <= ? AND status != ?", from, to, "cancelled").Find(&reservations).Error
	return reservations, err
}

func (r *ReservationRepo) GetFiltered(period string) ([]db.Reservation, error) {
	query := r.db.Preload("User").Where("status != ?", "cancelled")
	now := time.Now()

	switch period {
	case "today":
		query = query.Where("date = ?", now.Format("2006-01-02"))
	case "week":
		end := now.AddDate(0, 0, 7)
		query = query.Where("date >= ? AND date <= ?", now.Format("2006-01-02"), end.Format("2006-01-02"))
	case "month":
		query = query.Where("EXTRACT(MONTH FROM date) = ? AND EXTRACT(YEAR FROM date) = ?", now.Month(), now.Year())
	}

	var reservations []db.Reservation
	err := query.Order("date ASC, time_from ASC").Find(&reservations).Error
	return reservations, err
}

func (r *ReservationRepo) GetAll() ([]db.Reservation, error) {
	var reservations []db.Reservation
	err := r.db.Preload("User").Order("date ASC, time_from ASC").Find(&reservations).Error
	return reservations, err
}

func (r *ReservationRepo) GetByRange(from, to *time.Time) ([]db.Reservation, error) {
	query := r.db.Preload("User").Where("status != ?", "cancelled")
	if from != nil {
		query = query.Where("date >= ?", from.Format("2006-01-02"))
	}
	if to != nil {
		query = query.Where("date <= ?", to.Format("2006-01-02"))
	}
	var reservations []db.Reservation
	err := query.Order("date ASC, time_from ASC").Find(&reservations).Error
	return reservations, err
}

func (r *ReservationRepo) UpdateStatus(id uint, status string) error {
	return r.db.Model(&db.Reservation{}).Where("id = ?", id).Update("status", status).Error
}

func (r *ReservationRepo) Cancel(id uint) error {
	return r.UpdateStatus(id, "cancelled")
}

func (r *ReservationRepo) UpdateDetails(reservation *db.Reservation) error {
	return r.db.Model(&db.Reservation{}).Where("id = ?", reservation.ID).Updates(map[string]interface{}{
		"user_id":        reservation.UserID,
		"date":           reservation.Date,
		"time_from":      reservation.TimeFrom,
		"time_to":        reservation.TimeTo,
		"day_type":       reservation.DayType,
		"price_per_hour": reservation.PricePerHour,
		"total_price":    reservation.TotalPrice,
		"paid_amount":    reservation.PaidAmount,
		"status":         reservation.Status,
	}).Error
}

func (r *ReservationRepo) GetDueBalanceReminders(now time.Time) ([]db.Reservation, error) {
	var reservations []db.Reservation
	err := r.db.Preload("User").
		Where("status = ? AND balance_reminder_sent_at IS NULL", "confirmed").
		Where("user_id IS NOT NULL").
		Where("(date + time_from::time) <= ?", now).
		Order("date ASC, time_from ASC").
		Find(&reservations).Error
	return reservations, err
}

func (r *ReservationRepo) GetDueStartReminders(now time.Time, minutesBefore int) ([]db.Reservation, error) {
	var reservations []db.Reservation
	target := now.Add(time.Duration(minutesBefore) * time.Minute)
	err := r.db.Preload("User").
		Where("status = ?", "confirmed").
		Where("user_id IS NOT NULL").
		Where("reminder_60_sent_at IS NULL").
		Where("(date + time_from::time) <= ?", target).
		Where("(date + time_from::time) > ?", now).
		Order("date ASC, time_from ASC").
		Find(&reservations).Error
	return reservations, err
}

func (r *ReservationRepo) GetDueEndReminders(now time.Time, minutesBefore int) ([]db.Reservation, error) {
	var reservations []db.Reservation
	target := now.Add(time.Duration(minutesBefore) * time.Minute)
	endExpr := `(date + time_to::time + CASE WHEN time_to <= time_from THEN interval '1 day' ELSE interval '0 day' END)`
	err := r.db.Preload("User").
		Where("status = ?", "confirmed").
		Where("user_id IS NOT NULL").
		Where("reminder_30_sent_at IS NULL").
		Where(endExpr+" <= ?", target).
		Where(endExpr+" > ?", now).
		Order("date ASC, time_to ASC").
		Find(&reservations).Error
	return reservations, err
}

func (r *ReservationRepo) MarkStartReminderSent(id uint, minutesBefore int, sentAt time.Time) error {
	return r.db.Model(&db.Reservation{}).Where("id = ? AND reminder_60_sent_at IS NULL", id).Update("reminder_60_sent_at", sentAt).Error
}

func (r *ReservationRepo) MarkEndReminderSent(id uint, sentAt time.Time) error {
	return r.db.Model(&db.Reservation{}).Where("id = ? AND reminder_30_sent_at IS NULL", id).Update("reminder_30_sent_at", sentAt).Error
}

func (r *ReservationRepo) MarkBalanceReminderSent(id uint, sentAt time.Time) error {
	return r.db.Model(&db.Reservation{}).Where("id = ? AND balance_reminder_sent_at IS NULL", id).Update("balance_reminder_sent_at", sentAt).Error
}

func (r *ReservationRepo) IsSlotAvailable(date time.Time, timeFrom, timeTo string) (bool, error) {
	var count int64
	err := r.db.Model(&db.Reservation{}).
		Where("date = ? AND status != ?", date.Format("2006-01-02"), "cancelled").
		Where("(time_from < ? AND time_to > ?)", timeTo, timeFrom).
		Count(&count).Error
	return count == 0, err
}

func (r *ReservationRepo) HasUpcomingReservation(userID uint) (bool, error) {
	var count int64
	err := r.db.Model(&db.Reservation{}).
		Where("user_id = ? AND status != ?", userID, "cancelled").
		Where("date >= ?", time.Now().Format("2006-01-02")).
		Count(&count).Error
	return count > 0, err
}
