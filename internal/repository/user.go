package repository

import (
	"time"

	"gorm.io/gorm"

	"loft-bots/internal/db"
)

type UserRepo struct {
	db *gorm.DB
}

func NewUserRepo(db *gorm.DB) *UserRepo {
	return &UserRepo{db: db}
}

func (r *UserRepo) GetByTelegramID(telegramID int64) (*db.User, error) {
	var user db.User
	err := r.db.Where("telegram_id = ?", telegramID).First(&user).Error
	return &user, err
}

func (r *UserRepo) Create(telegramID int64, username string) (*db.User, error) {
	user := &db.User{
		TelegramID: telegramID,
		Username:   username,
		CreatedAt:  time.Now(),
	}
	err := r.db.Create(user).Error
	return user, err
}

func (r *UserRepo) FindOrCreate(telegramID int64, username string) (*db.User, error) {
	user, err := r.GetByTelegramID(telegramID)
	if err == nil {
		return user, nil
	}
	return r.Create(telegramID, username)
}
