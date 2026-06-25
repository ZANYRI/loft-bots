package db

import (
	"time"

	"gorm.io/gorm"
)

type User struct {
	ID         uint   `gorm:"primaryKey"`
	TelegramID int64  `gorm:"uniqueIndex;not null"`
	Username   string `gorm:"size:255"`
	CreatedAt  time.Time
}

type Event struct {
	ID          uint      `gorm:"primaryKey"`
	Title       string    `gorm:"size:255;not null"`
	Description string    `gorm:"type:text"`
	ImageFileID string    `gorm:"size:512"`
	EventDate   time.Time `gorm:"type:date;not null"`
	TimeFrom    string    `gorm:"size:5;not null"`
	TimeTo      string    `gorm:"size:5;not null"`
	Price       float64   `gorm:"type:decimal(10,2);not null"`
	TotalPlaces int       `gorm:"not null;default:20"`
	PlacesLeft  int       `gorm:"not null;default:20"`
	IsActive    bool      `gorm:"default:true"`
	CreatedAt   time.Time
}

type RentalPrice struct {
	ID           uint    `gorm:"primaryKey"`
	DayType      string  `gorm:"size:10;uniqueIndex;not null"`
	PricePerHour float64 `gorm:"type:decimal(10,2);not null"`
	UpdatedAt    time.Time
}

type Setting struct {
	Key   string `gorm:"primaryKey;size:100"`
	Value string `gorm:"type:text"`
}

type Expense struct {
	ID        uint      `gorm:"primaryKey"`
	Title     string    `gorm:"size:255;not null"`
	Amount    float64   `gorm:"type:decimal(10,2);not null"`
	CreatedAt time.Time `gorm:"index"`
}

type Discount struct {
	ID       uint    `gorm:"primaryKey"`
	Name     string  `gorm:"size:255;not null"`
	Percent  float64 `gorm:"not null"`
	IsActive bool    `gorm:"default:true"`
}

type MenuCategory struct {
	ID        uint   `gorm:"primaryKey"`
	Name      string `gorm:"size:255;not null"`
	Emoji     string `gorm:"size:10"`
	ImageURL  string `gorm:"size:512"`
	SortOrder int    `gorm:"default:0"`
}

type MenuItem struct {
	ID          uint    `gorm:"primaryKey"`
	CategoryID  uint    `gorm:"not null;index"`
	Name        string  `gorm:"size:255;not null"`
	Price       float64 `gorm:"type:decimal(10,2);not null"`
	IsAvailable bool    `gorm:"default:true"`

	Category MenuCategory `gorm:"foreignKey:CategoryID"`
}

type Reservation struct {
	ID                    uint       `gorm:"primaryKey"`
	UserID                uint       `gorm:"not null;index"`
	Date                  time.Time  `gorm:"type:date;not null"`
	TimeFrom              string     `gorm:"size:5;not null"`
	TimeTo                string     `gorm:"size:5;not null"`
	DayType               string     `gorm:"size:10;not null"`
	PricePerHour          float64    `gorm:"type:decimal(10,2);not null"`
	TotalPrice            float64    `gorm:"type:decimal(10,2);not null"`
	Status                string     `gorm:"size:20;default:pending"`
	Reminder60SentAt      *time.Time `gorm:"column:reminder_60_sent_at;index"`
	Reminder30SentAt      *time.Time `gorm:"column:reminder_30_sent_at;index"`
	BalanceReminderSentAt *time.Time `gorm:"index"`

	User User `gorm:"foreignKey:UserID"`
}

type Order struct {
	ID             uint    `gorm:"primaryKey"`
	UserID         uint    `gorm:"not null;index"`
	EventID        *uint   `gorm:"index"`
	ReservationID  *uint   `gorm:"index"`
	MenuTotal      float64 `gorm:"type:decimal(10,2);default:0"`
	TicketQuantity int     `gorm:"not null;default:0"`
	ReceiptURL     string  `gorm:"size:512"`
	TotalPrice     float64 `gorm:"type:decimal(10,2);not null"`
	Status         string  `gorm:"size:20;default:pending"`
	CreatedAt      time.Time
	ReviewDueAt    *time.Time `gorm:"index"`
	ReviewSentAt   *time.Time `gorm:"index"`

	User        User            `gorm:"foreignKey:UserID"`
	Event       *Event          `gorm:"foreignKey:EventID"`
	Reservation *Reservation    `gorm:"foreignKey:ReservationID"`
	MenuItems   []OrderMenuItem `gorm:"foreignKey:OrderID"`
}

type OrderMenuItem struct {
	ID           uint    `gorm:"primaryKey"`
	OrderID      uint    `gorm:"not null;index"`
	MenuItemID   uint    `gorm:"not null"`
	Quantity     int     `gorm:"not null;default:1"`
	PriceAtOrder float64 `gorm:"type:decimal(10,2);not null"`

	Order    Order    `gorm:"foreignKey:OrderID"`
	MenuItem MenuItem `gorm:"foreignKey:MenuItemID"`
}

type Review struct {
	ID        uint      `gorm:"primaryKey"`
	UserID    uint      `gorm:"not null;index"`
	OrderID   *uint     `gorm:"index"`
	Rating    int       `gorm:"not null"`
	Text      string    `gorm:"type:text"`
	CreatedAt time.Time `gorm:"index"`

	User  User   `gorm:"foreignKey:UserID"`
	Order *Order `gorm:"foreignKey:OrderID"`
}

type UserState struct {
	TelegramID int64  `gorm:"primaryKey"`
	Bot        string `gorm:"primaryKey;size:10;not null"`
	State      string `gorm:"size:100;not null"`
	Data       string `gorm:"type:jsonb"`
	UpdatedAt  time.Time
}

func AutoMigrate(db *gorm.DB) error {
	return db.AutoMigrate(
		&User{},
		&Event{},
		&RentalPrice{},
		&Setting{},
		&Expense{},
		&Discount{},
		&MenuCategory{},
		&MenuItem{},
		&Reservation{},
		&Order{},
		&OrderMenuItem{},
		&Review{},
		&UserState{},
	)
}
