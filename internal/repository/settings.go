package repository

import (
	"gorm.io/gorm"

	"loft-bots/internal/db"
)

type SettingsRepo struct {
	db *gorm.DB
}

func NewSettingsRepo(db_ *gorm.DB) *SettingsRepo {
	return &SettingsRepo{db: db_}
}

func (r *SettingsRepo) Get(key string) (*db.Setting, error) {
	var setting db.Setting
	err := r.db.Where("key = ?", key).First(&setting).Error
	return &setting, err
}

func (r *SettingsRepo) Set(key, value string) error {
	return r.db.Model(&db.Setting{}).Where("key = ?", key).Update("value", value).Error
}

func (r *SettingsRepo) GetAll() ([]db.Setting, error) {
	var settings []db.Setting
	err := r.db.Find(&settings).Error
	return settings, err
}

type RentalPriceRepo struct {
	db *gorm.DB
}

func NewRentalPriceRepo(db_ *gorm.DB) *RentalPriceRepo {
	return &RentalPriceRepo{db: db_}
}

func (r *RentalPriceRepo) GetByDayType(dayType string) (*db.RentalPrice, error) {
	var price db.RentalPrice
	err := r.db.Where("day_type = ?", dayType).First(&price).Error
	return &price, err
}

func (r *RentalPriceRepo) GetAll() ([]db.RentalPrice, error) {
	var prices []db.RentalPrice
	err := r.db.Find(&prices).Error
	return prices, err
}

func (r *RentalPriceRepo) Update(dayType string, pricePerHour float64) error {
	return r.db.Model(&db.RentalPrice{}).Where("day_type = ?", dayType).Update("price_per_hour", pricePerHour).Error
}

type MenuCategoryRepo struct {
	db *gorm.DB
}

func NewMenuCategoryRepo(db_ *gorm.DB) *MenuCategoryRepo {
	return &MenuCategoryRepo{db: db_}
}

func (r *MenuCategoryRepo) GetAll() ([]db.MenuCategory, error) {
	var categories []db.MenuCategory
	err := r.db.Order("sort_order ASC").Find(&categories).Error
	return categories, err
}

func (r *MenuCategoryRepo) GetByID(id uint) (*db.MenuCategory, error) {
	var category db.MenuCategory
	err := r.db.First(&category, id).Error
	return &category, err
}

func (r *MenuCategoryRepo) Create(category *db.MenuCategory) error {
	return r.db.Create(category).Error
}

func (r *MenuCategoryRepo) Update(category *db.MenuCategory) error {
	return r.db.Save(category).Error
}

func (r *MenuCategoryRepo) Delete(id uint) error {
	return r.db.Delete(&db.MenuCategory{}, id).Error
}

type MenuItemRepo struct {
	db *gorm.DB
}

func NewMenuItemRepo(db_ *gorm.DB) *MenuItemRepo {
	return &MenuItemRepo{db: db_}
}

func (r *MenuItemRepo) GetAll() ([]db.MenuItem, error) {
	var items []db.MenuItem
	err := r.db.Preload("Category").Order("category_id ASC").Find(&items).Error
	return items, err
}

func (r *MenuItemRepo) GetByCategoryID(categoryID uint) ([]db.MenuItem, error) {
	var items []db.MenuItem
	err := r.db.Where("category_id = ?", categoryID).Find(&items).Error
	return items, err
}

func (r *MenuItemRepo) GetAvailable() ([]db.MenuItem, error) {
	var items []db.MenuItem
	err := r.db.Where("is_available = ?", true).Preload("Category").Find(&items).Error
	return items, err
}

func (r *MenuItemRepo) GetByID(id uint) (*db.MenuItem, error) {
	var item db.MenuItem
	err := r.db.Preload("Category").First(&item, id).Error
	return &item, err
}

func (r *MenuItemRepo) Create(item *db.MenuItem) error {
	return r.db.Create(item).Error
}

func (r *MenuItemRepo) Update(item *db.MenuItem) error {
	return r.db.Save(item).Error
}

func (r *MenuItemRepo) Delete(id uint) error {
	return r.db.Delete(&db.MenuItem{}, id).Error
}

func (r *MenuItemRepo) ToggleAvailability(id uint) error {
	var item db.MenuItem
	if err := r.db.First(&item, id).Error; err != nil {
		return err
	}
	return r.db.Model(&item).Update("is_available", !item.IsAvailable).Error
}
