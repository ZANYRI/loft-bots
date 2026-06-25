package repository

import (
	"loft-bots/internal/db"

	"gorm.io/gorm"
)

type ReviewRepo struct {
	db *gorm.DB
}

func NewReviewRepo(db_ *gorm.DB) *ReviewRepo {
	return &ReviewRepo{db: db_}
}

func (r *ReviewRepo) Create(review *db.Review) error {
	return r.db.Create(review).Error
}

func (r *ReviewRepo) GetAll() ([]db.Review, error) {
	var reviews []db.Review
	err := r.db.Preload("User").Preload("Order").Order("created_at DESC").Find(&reviews).Error
	return reviews, err
}
