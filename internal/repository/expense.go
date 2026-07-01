package repository

import (
	"time"

	"gorm.io/gorm"

	"loft-bots/internal/db"
)

type ExpenseRepo struct {
	db *gorm.DB
}

func NewExpenseRepo(db_ *gorm.DB) *ExpenseRepo {
	return &ExpenseRepo{db: db_}
}

func (r *ExpenseRepo) GetAll() ([]db.Expense, error) {
	var expenses []db.Expense
	err := r.db.Order("created_at DESC").Find(&expenses).Error
	return expenses, err
}

func (r *ExpenseRepo) GetByRange(from, to *time.Time) ([]db.Expense, error) {
	query := r.db
	if from != nil {
		query = query.Where("created_at >= ?", *from)
	}
	if to != nil {
		query = query.Where("created_at < ?", to.AddDate(0, 0, 1))
	}
	var expenses []db.Expense
	err := query.Order("created_at DESC").Find(&expenses).Error
	return expenses, err
}

func (r *ExpenseRepo) Create(expense *db.Expense) error {
	return r.db.Create(expense).Error
}

func (r *ExpenseRepo) Delete(id uint) error {
	return r.db.Delete(&db.Expense{}, id).Error
}

func (r *ExpenseRepo) TotalForPeriod(period string) (float64, error) {
	query := r.db.Model(&db.Expense{})
	now := time.Now()
	switch period {
	case "today":
		query = query.Where("created_at >= ?", now.Truncate(24*time.Hour))
	case "week":
		query = query.Where("created_at >= ?", now.AddDate(0, 0, -7))
	case "month":
		query = query.Where("created_at >= ?", time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location()))
	}

	var total float64
	err := query.Select("COALESCE(SUM(amount), 0)").Scan(&total).Error
	return total, err
}

func (r *ExpenseRepo) TotalByRange(from, to *time.Time) (float64, error) {
	query := r.db.Model(&db.Expense{})
	if from != nil {
		query = query.Where("created_at >= ?", *from)
	}
	if to != nil {
		query = query.Where("created_at < ?", to.AddDate(0, 0, 1))
	}
	var total float64
	err := query.Select("COALESCE(SUM(amount), 0)").Scan(&total).Error
	return total, err
}
