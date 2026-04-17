package telegram

import (
	"context"
	"log/slog"
	"strings"
)

type Bot struct {
	client *client
	chatID int64
	logger *slog.Logger
}

func (b *Bot) Start(ctx context.Context) error {
	offset, err := b.getLatestOffset()
	if err != nil {
		return err
	}

	updates := b.client.getUpdatesChan(offset)

	for {
		select {
		case <-ctx.Done():
			b.client.close()
			for range updates {
			}
			return nil
		case upd, ok := <-updates:
			if !ok {
				return nil
			}
			if upd.Message != nil && upd.Message.Chat != nil && upd.Message.Chat.ID == b.chatID {
				switch strings.ToLower(upd.Message.Text) {
				case "stats":
					// add stats handling if needed
				}
			}
		}
	}
}

func (b *Bot) getLatestOffset() (int, error) {
	upds, err := b.client.getUpdates(context.Background(), -1, 0)
	if err != nil {
		return 0, err
	}
	offset := 0
	if len(upds) > 0 {
		offset = upds[0].UpdateID + 1
	}
	return offset, nil
}
