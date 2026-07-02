package repository

import (
	"gorm.io/gorm"
	"time"

	"loft-bots/internal/db"
)

type EventRepo struct {
	db *gorm.DB
}

func NewEventRepo(db *gorm.DB) *EventRepo {
	return &EventRepo{db: db}
}

func (r *EventRepo) GetActive() ([]db.Event, error) {
	var events []db.Event
	err := r.db.Where("is_active = ? AND deleted_at IS NULL", true).Order("event_date ASC, time_from ASC").Find(&events).Error
	return events, err
}

func (r *EventRepo) GetAll() ([]db.Event, error) {
	var events []db.Event
	err := r.db.Where("deleted_at IS NULL").Order("event_date DESC").Find(&events).Error
	return events, err
}

func (r *EventRepo) GetByRange(from, to *time.Time) ([]db.Event, error) {
	query := r.db.Where("deleted_at IS NULL")
	if from != nil {
		query = query.Where("event_date >= ?", from.Format("2006-01-02"))
	}
	if to != nil {
		query = query.Where("event_date <= ?", to.Format("2006-01-02"))
	}
	var events []db.Event
	err := query.Order("event_date ASC, time_from ASC").Find(&events).Error
	return events, err
}

func (r *EventRepo) GetByID(id uint) (*db.Event, error) {
	var event db.Event
	err := r.db.First(&event, id).Error
	return &event, err
}

func (r *EventRepo) Create(event *db.Event) error {
	return r.db.Create(event).Error
}

func (r *EventRepo) Update(event *db.Event) error {
	return r.db.Save(event).Error
}

func (r *EventRepo) UpdateField(id uint, column string, value interface{}) error {
	return r.db.Model(&db.Event{}).Where("id = ?", id).Update(column, value).Error
}

func (r *EventRepo) Delete(id uint) error {
	now := time.Now()
	return r.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&db.Order{}).
			Where("event_id = ? AND status != ?", id, "cancelled").
			Updates(map[string]interface{}{
				"status":         "cancelled",
				"review_due_at":  nil,
				"review_sent_at": nil,
			}).Error; err != nil {
			return err
		}
		return tx.Model(&db.Event{}).Where("id = ?", id).Updates(map[string]interface{}{
			"is_active":   false,
			"places_left": gorm.Expr("total_places"),
			"deleted_at":  now,
		}).Error
	})
}

func (r *EventRepo) ReservePlaces(id uint, quantity int) (bool, error) {
	result := r.db.Model(&db.Event{}).Where("id = ? AND places_left >= ? AND is_active = ? AND deleted_at IS NULL", id, quantity, true).
		Update("places_left", gorm.Expr("places_left - ?", quantity))
	if result.Error != nil {
		return false, result.Error
	}
	if result.RowsAffected == 0 {
		return false, nil
	}
	if err := r.db.Model(&db.Event{}).Where("id = ? AND places_left = 0", id).Update("is_active", false).Error; err != nil {
		return false, err
	}
	return true, nil
}

func (r *EventRepo) ReleasePlaces(id uint, quantity int) error {
	return r.db.Model(&db.Event{}).Where("id = ? AND deleted_at IS NULL", id).Updates(map[string]interface{}{"places_left": gorm.Expr("LEAST(places_left + ?, total_places)", quantity), "is_active": true}).Error
}
