package telegram

import (
	"log/slog"
)

// NewBot matches main.go usage: NewBot(token string, chatID int64, logger *slog.Logger)
func NewBot(token string, chatID int64, logger *slog.Logger) (*Bot, error) {
	c, err := newClient(token)
	if err != nil {
		return nil, err
	}
	return &Bot{client: c, chatID: chatID, logger: logger}, nil
}
