package telegram

import (
	"bytes"
	"context"
	"image/jpeg"

	"local/internal/svc/internal/event"
)

func (b *Bot) Handle(ctx context.Context, e event.Event) error {
	if e.Image() != nil {
		buf := new(bytes.Buffer)
		if err := jpeg.Encode(buf, e.Image(), &jpeg.Options{Quality: 90}); err != nil {
			_ = b.client.sendMessage(ctx, b.chatID, e.Message()+" (screenshot encode failed)")
			return err
		}
		return b.client.sendPhoto(ctx, b.chatID, e.Message(), "screenshot.jpg", buf.Bytes())
	}
	return b.client.sendMessage(ctx, b.chatID, e.Message())
}
