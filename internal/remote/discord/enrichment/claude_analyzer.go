package enrichment

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ClaudeAnalyzer uses Claude API to provide AI-powered item analysis.
type ClaudeAnalyzer struct {
	apiKey string
	model  string
	client *http.Client
}

// ClaudeAnalysis holds the AI-generated analysis for an item.
type ClaudeAnalysis struct {
	Summary        string // 1-2 sentence assessment
	Score          string // e.g. "A+", "B-", "S-tier"
	BestBuilds     string // what builds benefit most
	EstimatedPrice string // e.g. "400-800 fg" — AI-estimated d2jsp price
}

// NewClaudeAnalyzer creates an analyzer. Returns nil if no API key.
func NewClaudeAnalyzer(apiKey, model string) *ClaudeAnalyzer {
	if apiKey == "" {
		return nil
	}
	if model == "" {
		model = "claude-sonnet-4-6"
	}
	return &ClaudeAnalyzer{
		apiKey: apiKey,
		model:  model,
		client: &http.Client{Timeout: 30 * time.Second},
	}
}

type claudeRequest struct {
	Model     string           `json:"model"`
	MaxTokens int              `json:"max_tokens"`
	Messages  []claudeMessage  `json:"messages"`
}

type claudeMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type claudeResponse struct {
	Content []struct {
		Text string `json:"text"`
	} `json:"content"`
}

// Analyze sends item details to Claude and returns AI analysis.
func (c *ClaudeAnalyzer) Analyze(ctx context.Context, itemName string, stats string, rollReport *RollReport, price *D2JSPPrice) (*ClaudeAnalysis, error) {
	if c == nil {
		return nil, nil
	}

	prompt := buildAnalysisPrompt(itemName, stats, rollReport, price)

	reqBody := claudeRequest{
		Model:     c.model,
		MaxTokens: 300,
		Messages: []claudeMessage{
			{Role: "user", Content: prompt},
		},
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal claude request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", "https://api.anthropic.com/v1/messages", bytes.NewReader(jsonData))
	if err != nil {
		return nil, fmt.Errorf("failed to create claude request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", c.apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("claude API request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("claude API returned %d: %s", resp.StatusCode, string(body))
	}

	var result claudeResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode claude response: %w", err)
	}

	if len(result.Content) == 0 {
		return nil, fmt.Errorf("empty claude response")
	}

	return parseAnalysis(result.Content[0].Text), nil
}

func buildAnalysisPrompt(itemName, stats string, rollReport *RollReport, price *D2JSPPrice) string {
	var sb strings.Builder
	sb.WriteString("You are a Diablo 2 Resurrected item pricing and analysis expert. Your PRIMARY job is to give an accurate d2jsp forum gold (fg) price estimate for SC Non-Ladder.\n\n")
	sb.WriteString(fmt.Sprintf("Item: %s\n", itemName))
	sb.WriteString(fmt.Sprintf("Stats:\n%s\n", stats))

	if rollReport != nil && rollReport.HasKnownRanges {
		sb.WriteString(fmt.Sprintf("\nRoll Quality: %.0f%% overall\n", rollReport.OverallPercentile))
		for _, r := range rollReport.Rolls {
			sb.WriteString(fmt.Sprintf("  %s: %d (%d–%d) = %.0f%%\n", r.StatName, r.Value, r.Min, r.Max, r.Percentile))
		}
	}

	if price != nil {
		sb.WriteString(fmt.Sprintf("\nReference baseline price (SC NL average): %s\n", FormatPrice(price)))
		sb.WriteString("Use this as a starting point and adjust UP or DOWN based on the specific rolls. Low rolls = below min, perfect rolls = above max.\n")
	}

	sb.WriteString("\nPricing guidelines:\n")
	sb.WriteString("- Give a SPECIFIC fg range, not vague estimates. E.g. '200-400 fg' not 'moderate value'\n")
	sb.WriteString("- Perfect or near-perfect rolls can be worth 2-5x the average price\n")
	sb.WriteString("- Low rolls on key stats (e.g. low %ED on weapons, low res on Mara's) significantly reduce value\n")
	sb.WriteString("- Ethereal status matters hugely for some items (Titan's, merc weapons)\n")

	sb.WriteString("\nFormat your response EXACTLY as:\nSCORE: [S/A/B/C/D tier]\nPRICE: [specific fg range for SC NL, e.g. '400-800 fg']\nSUMMARY: [1-2 sentence assessment focusing on what makes these rolls good/bad]\nBUILDS: [best builds for this item]")

	return sb.String()
}

func parseAnalysis(text string) *ClaudeAnalysis {
	analysis := &ClaudeAnalysis{}
	lines := strings.Split(text, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "SCORE:"):
			analysis.Score = strings.TrimSpace(strings.TrimPrefix(line, "SCORE:"))
		case strings.HasPrefix(line, "SUMMARY:"):
			analysis.Summary = strings.TrimSpace(strings.TrimPrefix(line, "SUMMARY:"))
		case strings.HasPrefix(line, "PRICE:"):
			analysis.EstimatedPrice = strings.TrimSpace(strings.TrimPrefix(line, "PRICE:"))
		case strings.HasPrefix(line, "BUILDS:"):
			analysis.BestBuilds = strings.TrimSpace(strings.TrimPrefix(line, "BUILDS:"))
		}
	}

	// Fallback if parsing failed — use entire text as summary
	if analysis.Summary == "" && analysis.Score == "" {
		analysis.Summary = text
	}

	return analysis
}
