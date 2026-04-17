package discord

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"local/internal/svc/internal/bot"
	"local/internal/svc/internal/config"
	"local/internal/svc/internal/remote/discord/enrichment"
)

type Bot struct {
	channelID         string
	itemChannelID     string
	manager           *bot.SupervisorManager
	webhookClient     *webhookClient
	itemWebhook       *webhookClient
	enrichmentService *enrichment.Service
}

func NewBot(token, channelID, itemChannelID string, manager *bot.SupervisorManager, useWebhook bool, webhookURL, itemWebhookURL string) (*Bot, error) {
	if !useWebhook {
		return nil, fmt.Errorf("discord gateway mode is no longer supported; enable webhook mode and configure webhook URL")
	}
	if strings.TrimSpace(webhookURL) == "" {
		return nil, fmt.Errorf("webhook URL is required")
	}

	b := &Bot{
		channelID:     channelID,
		itemChannelID: strings.TrimSpace(itemChannelID),
		manager:       manager,
		webhookClient: newWebhookClient(webhookURL),
	}
	if strings.TrimSpace(itemWebhookURL) != "" {
		b.itemWebhook = newWebhookClient(itemWebhookURL)
	}

	if config.App.Discord.EnableFancyItemDrops {
		logger := slog.Default()
		var flare *enrichment.FlareSolverr
		if config.App.Discord.FlareSolverrURL != "" || config.App.Discord.D2JSPScraping {
			flare = enrichment.NewFlareSolverr(config.App.Discord.FlareSolverrURL, logger)
		}
		d2jspScraper := enrichment.NewD2JSPScraper(config.App.Discord.D2JSPRealm, flare, logger, config.App.Discord.D2JSPCookie)
		traderieScraper := enrichment.NewTraderieScraper(flare, logger)
		b.enrichmentService = enrichment.NewService(d2jspScraper, traderieScraper, logger)
	}

	return b, nil
}

func (b *Bot) Start(ctx context.Context) error {
	<-ctx.Done()
	return nil
}
