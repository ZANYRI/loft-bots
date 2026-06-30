package db

import (
	"log"
	"os"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
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

	lockMigrations(db)
	defer unlockMigrations(db)

	migrateUserStatesPrimaryKey(db)

	if err := AutoMigrate(db); err != nil {
		log.Fatalf("failed to auto-migrate: %v", err)
	}
	// Admin-created reservations may be walk-ins without a Telegram user.
	db.Exec(`ALTER TABLE reservations ALTER COLUMN user_id DROP NOT NULL`)
	db.Exec(`UPDATE menu_categories SET type = 'menu' WHERE type IS NULL OR type = ''`)
	db.Exec(`UPDATE menu_categories SET type = 'service' WHERE LOWER(name) LIKE '%доп%услуг%'`)
	db.Exec(`DELETE FROM settings WHERE key = 'message_receipt_received'`)
	syncReservationStatusesFromOrders(db)
	db.Exec(`ALTER TABLE reservations DROP COLUMN IF EXISTS reminder30_sent_at`)
	db.Exec(`ALTER TABLE reservations DROP COLUMN IF EXISTS reminder60_sent_at`)
	db.Exec(`UPDATE settings SET value = ? WHERE key = ? AND value = ?`,
		"⏰ До конца вашей брони осталось 30 минут. Время окончания: {time_to}.",
		"message_reservation_reminder_30",
		"⏰ До начала вашей брони осталось 30 минут. Ждём вас {date} в {time_from}.",
	)
	db.Exec(`UPDATE settings SET value = ? WHERE key = ? AND value = ?`,
		"✅ Чек получен. Мы проверим оплату и скоро вернёмся с подтверждением.",
		"message_after_payment",
		"✅ Чек получен. Мы проверим оплату и скоро вернёмся с подтверждением.\n\nЗалог: {deposit} ₽.",
	)

	seedDefaults(db)

	return db
}

func lockMigrations(db *gorm.DB) {
	if err := db.Exec("SELECT pg_advisory_lock(?)", int64(424242)).Error; err != nil {
		log.Printf("failed to lock migrations: %v", err)
	}
}

func unlockMigrations(db *gorm.DB) {
	if err := db.Exec("SELECT pg_advisory_unlock(?)", int64(424242)).Error; err != nil {
		log.Printf("failed to unlock migrations: %v", err)
	}
}

func migrateUserStatesPrimaryKey(db *gorm.DB) {
	query := `
DO $$
BEGIN
	IF to_regclass('public.user_states') IS NOT NULL THEN
		IF EXISTS (
			SELECT 1 FROM pg_constraint
			WHERE conrelid = 'public.user_states'::regclass
			AND contype = 'p'
			AND conname = 'user_states_pkey'
		) THEN
			ALTER TABLE public.user_states DROP CONSTRAINT user_states_pkey;
		END IF;

		IF NOT EXISTS (
			SELECT 1 FROM pg_constraint
			WHERE conrelid = 'public.user_states'::regclass
			AND contype = 'p'
			AND conname = 'user_states_pkey'
		) THEN
			ALTER TABLE public.user_states ADD CONSTRAINT user_states_pkey PRIMARY KEY (telegram_id, bot);
		END IF;
	END IF;
END $$;`
	if err := db.Exec(query).Error; err != nil {
		log.Printf("failed to migrate user_states primary key: %v", err)
	}
}

func syncReservationStatusesFromOrders(db *gorm.DB) {
	queries := []string{
		`UPDATE reservations r
		 SET status = 'confirmed'
		 FROM orders o
		 WHERE o.reservation_id = r.id
		 AND o.status = 'confirmed'
		 AND r.status = 'pending'`,
		`UPDATE reservations r
		 SET status = 'cancelled'
		 FROM orders o
		 WHERE o.reservation_id = r.id
		 AND o.status IN ('cancelled', 'refunded')
		 AND r.status != 'cancelled'`,
	}
	for _, query := range queries {
		if err := db.Exec(query).Error; err != nil {
			log.Printf("failed to sync reservation statuses: %v", err)
		}
	}
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
		{Key: "cork_fee", Value: "300"},
		{Key: "prepayment_percent", Value: "30"},
		{Key: "admin_contact", Value: "@admin"},
		{Key: "loft_name", Value: "Название лофта"},
		{Key: "site_url", Value: ""},
		{Key: "instagram_url", Value: ""},
		{Key: "wifi_name", Value: ""},
		{Key: "wifi_password", Value: ""},
		{Key: "bot_start_title", Value: "Название лофта"},
		{Key: "bot_start_description", Value: "Выберите раздел:"},
		{Key: "bot_start_avatar", Value: ""},
		{Key: "message_delayed_event_minutes", Value: "15"},
		{Key: "message_review_request_event", Value: "Спасибо, что посетили наше мероприятие! Оцените, пожалуйста, впечатления по 5-балльной шкале."},
		{Key: "message_review_request_reservation", Value: "Спасибо, что бронировали наш лофт! Оцените, пожалуйста, ваш визит по 5-балльной шкале."},
		{Key: "message_review_thanks_event_low", Value: "Спасибо за честный отзыв. Мы учтём замечания и постараемся сделать следующие мероприятия лучше."},
		{Key: "message_review_thanks_event_5", Value: "Спасибо за отличную оценку мероприятия! Очень рады, что вам понравилось."},
		{Key: "message_review_thanks_reservation_low", Value: "Спасибо за честный отзыв. Мы учтём замечания и постараемся улучшить сервис."},
		{Key: "message_review_thanks_reservation_5", Value: "Спасибо за отличную оценку бронирования! Очень рады, что вам понравилось."},
		{Key: "message_reservation_cancelled", Value: "❌ Ваша бронь на {date}, {time_from}–{time_to} была отменена администратором."},
		{Key: "message_reservation_reminder_60", Value: "⏰ До начала вашей брони остался 1 час. Ждём вас {date} в {time_from}."},
		{Key: "message_reservation_reminder_30", Value: "⏰ До конца вашей брони осталось 30 минут. Время окончания: {time_to}."},
		{Key: "message_reservation_balance_due", Value: "⏰ Ваша бронь началась. Осталось оплатить {amount} ₽ из общей суммы {total} ₽."},
		{Key: "message_after_payment", Value: "✅ Чек получен. Мы проверим оплату и скоро вернёмся с подтверждением."},
		{Key: "offer_pdf_url", Value: ""},
		{Key: "deposit_amount", Value: "0"},
		{Key: "message_review_day_offset", Value: "1"},
		{Key: "message_review_next_day_time", Value: "12:00"},
		{Key: "client_spam_window_seconds", Value: "10"},
		{Key: "client_spam_max_actions", Value: "8"},
		{Key: "client_spam_block_seconds", Value: "30"},
	}
	for _, s := range settings {
		db.Clauses(clause.OnConflict{DoNothing: true}).Create(&s)
	}
	db.Delete(&Setting{}, "key IN ?", []string{
		"message_review_thanks_event",
		"message_review_thanks_reservation",
		"message_review_thanks_event_1",
		"message_review_thanks_event_2",
		"message_review_thanks_event_3",
		"message_review_thanks_event_4",
		"message_review_thanks_reservation_1",
		"message_review_thanks_reservation_2",
		"message_review_thanks_reservation_3",
		"message_review_thanks_reservation_4",
	})
}

func GetDSN() string {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		log.Fatal("DATABASE_URL is not set")
	}
	return dsn
}
