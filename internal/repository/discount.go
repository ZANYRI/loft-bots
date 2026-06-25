package repository

import (
	"gorm.io/gorm"
	"loft-bots/internal/db"
)

type DiscountRepo struct{ db *gorm.DB }

func NewDiscountRepo(db_ *gorm.DB) *DiscountRepo { return &DiscountRepo{db: db_} }
func (r *DiscountRepo) GetAll() ([]db.Discount, error) {
	var items []db.Discount
	return items, r.db.Order("id DESC").Find(&items).Error
}
func (r *DiscountRepo) Create(item *db.Discount) error { return r.db.Create(item).Error }
func (r *DiscountRepo) Update(item *db.Discount) error { return r.db.Save(item).Error }
