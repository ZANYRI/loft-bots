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

func (r *UserRepo) GetByID(id uint) (*db.User, error) {
	var user db.User
	err := r.db.First(&user, id).Error
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
		if username != "" && user.Username != username {
			if err := r.db.Model(user).Update("username", username).Error; err != nil {
				return nil, err
			}
			user.Username = username
		}
		return user, nil
	}
	return r.Create(telegramID, username)
}

func (r *UserRepo) GetAll() ([]db.User, error) {
	var users []db.User
	err := r.db.Order("created_at DESC").Find(&users).Error
	return users, err
}
