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

func (r *ReservationRepo) UpdateStatus(id uint, status string) error {
	return r.db.Model(&db.Reservation{}).Where("id = ?", id).Update("status", status).Error
}

func (r *ReservationRepo) Cancel(id uint) error {
	return r.UpdateStatus(id, "cancelled")
}

func (r *ReservationRepo) IsSlotAvailable(date time.Time, timeFrom, timeTo string) (bool, error) {
	var count int64
	err := r.db.Model(&db.Reservation{}).
		Where("date = ? AND status != ?", date.Format("2006-01-02"), "cancelled").
		Where("(time_from < ? AND time_to > ?)", timeTo, timeFrom).
		Count(&count).Error
	return count == 0, err
}
