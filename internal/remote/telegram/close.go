package telegram

func (b *Bot) Close() {
	if b == nil || b.client == nil {
		return
	}
	b.client.close()
}
