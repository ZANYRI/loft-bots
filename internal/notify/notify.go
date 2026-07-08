package notify

import (
	"context"
	"loft-bots/internal/logger"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

type AdminMessage struct {
	ChatID    int64
	MessageID int
}

var (
	adminMessagesMu sync.Mutex
	adminMessages   = map[uint][]AdminMessage{}
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
			logger.Printf("invalid admin telegram ID: %s", p)
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

func SendToAdmins(ctx context.Context, b *bot.Bot, text string, kb *models.InlineKeyboardMarkup) []AdminMessage {
	adminIDs := GetAdminIDs()
	var sent []AdminMessage
	for _, id := range adminIDs {
		params := &bot.SendMessageParams{
			ChatID:    id,
			Text:      text,
			ParseMode: models.ParseModeHTML,
		}
		if kb != nil {
			params.ReplyMarkup = kb
		}
		msg, err := b.SendMessage(ctx, params)
		if err != nil {
			logger.Printf("failed to send notification to admin %d: %v", id, err)
			continue
		}
		sent = append(sent, AdminMessage{ChatID: id, MessageID: msg.ID})
	}
	return sent
}

// SendToAdminsWithReceipt behaves like SendToAdmins, but when receiptURL is set
// it attaches the payment receipt (photo or PDF) natively so admins see it at
// a readable size instead of a bare link, with the order text as the caption.
func SendToAdminsWithReceipt(ctx context.Context, b *bot.Bot, text, receiptURL string, kb *models.InlineKeyboardMarkup) []AdminMessage {
	if receiptURL == "" {
		return SendToAdmins(ctx, b, text, kb)
	}
	adminIDs := GetAdminIDs()
	var sent []AdminMessage
	isPDF := strings.HasSuffix(strings.ToLower(receiptURL), ".pdf")
	caption := text
	if runes := []rune(caption); len(runes) > 1024 {
		caption = string(runes[:1021]) + "..."
	}
	for _, id := range adminIDs {
		var msg *models.Message
		var err error
		if isPDF {
			msg, err = b.SendDocument(ctx, &bot.SendDocumentParams{
				ChatID:      id,
				Document:    &models.InputFileString{Data: receiptURL},
				Caption:     caption,
				ParseMode:   models.ParseModeHTML,
				ReplyMarkup: kb,
			})
		} else {
			msg, err = b.SendPhoto(ctx, &bot.SendPhotoParams{
				ChatID:      id,
				Photo:       &models.InputFileString{Data: receiptURL},
				Caption:     caption,
				ParseMode:   models.ParseModeHTML,
				ReplyMarkup: kb,
			})
		}
		if err != nil {
			logger.Printf("failed to send receipt to admin %d: %v", id, err)
			continue
		}
		sent = append(sent, AdminMessage{ChatID: id, MessageID: msg.ID})
	}
	return sent
}

// RegisterOrderMessages remembers which admin chat/message pairs carry the
// notification for orderID, so it can be wiped from every admin chat once
// one admin acts on it.
func RegisterOrderMessages(orderID uint, messages []AdminMessage) {
	adminMessagesMu.Lock()
	defer adminMessagesMu.Unlock()
	adminMessages[orderID] = messages
}

// DeleteOrderMessages removes the order notification from every admin chat
// it was sent to.
func DeleteOrderMessages(ctx context.Context, b *bot.Bot, orderID uint) {
	adminMessagesMu.Lock()
	messages := adminMessages[orderID]
	delete(adminMessages, orderID)
	adminMessagesMu.Unlock()

	for _, m := range messages {
		if _, err := b.DeleteMessage(ctx, &bot.DeleteMessageParams{ChatID: m.ChatID, MessageID: m.MessageID}); err != nil {
			logger.Printf("failed to delete admin notification: chat=%d msg=%d err=%v", m.ChatID, m.MessageID, err)
		}
	}
}

// SendToUser sends text to a client chat and returns any send error so
// callers can surface delivery failures (e.g. user blocked the bot) instead
// of silently assuming the notification arrived.
func SendToUser(ctx context.Context, b *bot.Bot, chatID int64, text string) error {
	_, err := b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:    chatID,
		Text:      text,
		ParseMode: models.ParseModeHTML,
	})
	if err != nil {
		logger.Printf("failed to send message to user %d: %v", chatID, err)
	}
	return err
}
