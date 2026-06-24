package repository

import (
	"gorm.io/gorm"

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
	err := r.db.Where("is_active = ?", true).Order("event_date ASC, time_from ASC").Find(&events).Error
	return events, err
}

func (r *EventRepo) GetAll() ([]db.Event, error) {
	var events []db.Event
	err := r.db.Order("event_date DESC").Find(&events).Error
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
	return r.db.Delete(&db.Event{}, id).Error
}

func (r *EventRepo) ToggleActive(id uint) error {
	var event db.Event
	if err := r.db.First(&event, id).Error; err != nil {
		return err
	}
	return r.db.Model(&event).Update("is_active", !event.IsActive).Error
}
