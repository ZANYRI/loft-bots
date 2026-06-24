package notify

import (
	"context"
	"log"
	"os"
	"strconv"
	"strings"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

func GetAdminIDs() []int64 {
	raw := os.Getenv("ADMIN_TELEGRAM_IDS")
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	var ids []int64
	for _, p := range parts {
		id, err := strconv.ParseInt(strings.TrimSpace(p), 10, 64)
		if err != nil {
			log.Printf("invalid admin telegram ID: %s", p)
			continue
		}
		ids = append(ids, id)
	}
	return ids
}

func GetAdminUsernames() []string {
	raw := os.Getenv("ADMIN_USERNAMES")
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	var usernames []string
	for _, p := range parts {
		usernames = append(usernames, strings.TrimSpace(p))
	}
	return usernames
}

func SendToAdmins(ctx context.Context, b *bot.Bot, text string, kb *models.InlineKeyboardMarkup) {
	adminIDs := GetAdminIDs()
	for _, id := range adminIDs {
		params := &bot.SendMessageParams{
			ChatID:      id,
			Text:        text,
			ParseMode:   models.ParseModeHTML,
		}
		if kb != nil {
			params.ReplyMarkup = kb
		}
		_, err := b.SendMessage(ctx, params)
		if err != nil {
			log.Printf("failed to send notification to admin %d: %v", id, err)
		}
	}
}

func SendToUser(ctx context.Context, b *bot.Bot, chatID int64, text string) {
	_, err := b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:    chatID,
		Text:      text,
		ParseMode: models.ParseModeHTML,
	})
	if err != nil {
		log.Printf("failed to send message to user %d: %v", chatID, err)
	}
}
