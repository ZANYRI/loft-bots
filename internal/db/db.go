package db

import (
	"log"
	"os"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func Connect(dsn string) *gorm.DB {
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Warn),
	})
	if err != nil {
		log.Fatalf("failed to connect database: %v", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		log.Fatalf("failed to get sql.DB: %v", err)
	}

	sqlDB.SetMaxOpenConns(25)
	sqlDB.SetMaxIdleConns(5)

	if err := AutoMigrate(db); err != nil {
		log.Fatalf("failed to auto-migrate: %v", err)
	}

	seedDefaults(db)

	return db
}

func seedDefaults(db *gorm.DB) {
	rentalPrices := []RentalPrice{
		{DayType: "weekday", PricePerHour: 1000},
		{DayType: "weekend", PricePerHour: 1500},
	}
	for _, rp := range rentalPrices {
		db.Where("day_type = ?", rp.DayType).FirstOrCreate(&rp)
	}

	settings := []Setting{
		{Key: "payment_phone", Value: "+7 (XXX) XXX-XX-XX"},
		{Key: "loft_name", Value: "Название лофта"},
	}
	for _, s := range settings {
		db.Where("key = ?", s.Key).FirstOrCreate(&s)
	}
}

func GetDSN() string {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		log.Fatal("DATABASE_URL is not set")
	}
	return dsn
}
