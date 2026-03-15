package discord

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
)

type webhookClient struct {
	url    string
	client *http.Client
}

func newWebhookClient(url string) *webhookClient {
	return &webhookClient{
		url: strings.TrimSpace(url),
		client: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

func (w *webhookClient) Send(ctx context.Context, content, fileName string, fileData []byte) error {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)

	if err := writer.WriteField("content", content); err != nil {
		writer.Close()
		return fmt.Errorf("failed to prepare webhook payload: %w", err)
	}

	if len(fileData) > 0 && fileName != "" {
		part, err := writer.CreateFormFile("file", fileName)
		if err != nil {
			writer.Close()
			return fmt.Errorf("failed to add webhook file field: %w", err)
		}

		if _, err := part.Write(fileData); err != nil {
			writer.Close()
			return fmt.Errorf("failed to write webhook file data: %w", err)
		}
	}

	contentType := writer.FormDataContentType()
	if err := writer.Close(); err != nil {
		return fmt.Errorf("failed to finalize webhook payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.url, &body)
	if err != nil {
		return fmt.Errorf("failed to create webhook request: %w", err)
	}
	req.Header.Set("Content-Type", contentType)

	resp, err := w.client.Do(req)
	if err != nil {
		return fmt.Errorf("webhook request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= http.StatusBadRequest {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("webhook returned %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}

	return nil
}

func (w *webhookClient) SendEmbed(ctx context.Context, embed *discordgo.MessageEmbed) error {
	_, err := w.sendEmbedInternal(ctx, embed, false, nil, "")
	return err
}

// SendEmbedWithResponse sends an embed and returns the Discord message ID.
// Uses ?wait=true so Discord returns the created message object.
func (w *webhookClient) SendEmbedWithResponse(ctx context.Context, embed *discordgo.MessageEmbed) (string, error) {
	return w.sendEmbedInternal(ctx, embed, true, nil, "")
}

// SendEmbedWithThumbnail sends an embed with a local image file as attachment thumbnail.
// The embed's Thumbnail.URL should be set to "attachment://filename" before calling.
func (w *webhookClient) SendEmbedWithThumbnail(ctx context.Context, embed *discordgo.MessageEmbed, imageData []byte, imageFilename string) (string, error) {
	return w.sendEmbedInternal(ctx, embed, true, imageData, imageFilename)
}

// EditEmbed edits an existing webhook message by ID with a new embed.
func (w *webhookClient) EditEmbed(ctx context.Context, messageID string, embed *discordgo.MessageEmbed) error {
	payload := struct {
		Embeds []*discordgo.MessageEmbed `json:"embeds"`
	}{
		Embeds: []*discordgo.MessageEmbed{embed},
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to serialize edit embed: %w", err)
	}

	editURL := fmt.Sprintf("%s/messages/%s", w.url, messageID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, editURL, bytes.NewReader(payloadJSON))
	if err != nil {
		return fmt.Errorf("failed to create edit request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := w.client.Do(req)
	if err != nil {
		return fmt.Errorf("edit webhook request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= http.StatusBadRequest {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("edit webhook returned %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}

	return nil
}

// FileAttachment represents a file to attach to a webhook message.
type FileAttachment struct {
	Data     []byte
	Filename string
}

// EditEmbedWithFiles edits an existing webhook message with a new embed and file attachments.
// This uses multipart/form-data so new attachments (e.g. rune icon) can be added on PATCH.
// IMPORTANT: Discord requires an explicit "attachments" array in the payload when sending files
// on PATCH — only listed attachments survive; unlisted ones (including the original) are removed.
func (w *webhookClient) EditEmbedWithFiles(ctx context.Context, messageID string, embed *discordgo.MessageEmbed, files []FileAttachment) error {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)

	// Build attachments array — each new file maps to its index (id=0 → files[0], etc.)
	// This tells Discord to REPLACE all existing attachments with only these new ones.
	type attachmentRef struct {
		ID          int    `json:"id"`
		Filename    string `json:"filename"`
		Description string `json:"description,omitempty"`
	}
	attachments := make([]attachmentRef, len(files))
	for i, f := range files {
		attachments[i] = attachmentRef{ID: i, Filename: f.Filename}
	}

	payload := struct {
		Embeds      []*discordgo.MessageEmbed `json:"embeds"`
		Attachments []attachmentRef           `json:"attachments"`
	}{
		Embeds:      []*discordgo.MessageEmbed{embed},
		Attachments: attachments,
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		writer.Close()
		return fmt.Errorf("failed to serialize edit embed: %w", err)
	}

	if err := writer.WriteField("payload_json", string(payloadJSON)); err != nil {
		writer.Close()
		return fmt.Errorf("failed to prepare edit payload: %w", err)
	}

	for i, f := range files {
		part, err := writer.CreateFormFile(fmt.Sprintf("files[%d]", i), f.Filename)
		if err != nil {
			writer.Close()
			return fmt.Errorf("failed to add file attachment %d: %w", i, err)
		}
		if _, err := part.Write(f.Data); err != nil {
			writer.Close()
			return fmt.Errorf("failed to write file attachment %d: %w", i, err)
		}
	}

	contentType := writer.FormDataContentType()
	if err := writer.Close(); err != nil {
		return fmt.Errorf("failed to finalize edit payload: %w", err)
	}

	editURL := fmt.Sprintf("%s/messages/%s", w.url, messageID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, editURL, &body)
	if err != nil {
		return fmt.Errorf("failed to create edit request: %w", err)
	}
	req.Header.Set("Content-Type", contentType)

	resp, err := w.client.Do(req)
	if err != nil {
		return fmt.Errorf("edit webhook request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= http.StatusBadRequest {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("edit webhook returned %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}

	return nil
}

func (w *webhookClient) sendEmbedInternal(ctx context.Context, embed *discordgo.MessageEmbed, wantResponse bool, imageData []byte, imageFilename string) (string, error) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)

	payload := struct {
		Embeds []*discordgo.MessageEmbed `json:"embeds"`
	}{
		Embeds: []*discordgo.MessageEmbed{embed},
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		writer.Close()
		return "", fmt.Errorf("failed to serialize webhook embed: %w", err)
	}

	if err := writer.WriteField("payload_json", string(payloadJSON)); err != nil {
		writer.Close()
		return "", fmt.Errorf("failed to prepare webhook embed payload: %w", err)
	}

	// Attach thumbnail image if provided
	if len(imageData) > 0 && imageFilename != "" {
		part, err := writer.CreateFormFile("file", imageFilename)
		if err != nil {
			writer.Close()
			return "", fmt.Errorf("failed to add image attachment: %w", err)
		}
		if _, err := part.Write(imageData); err != nil {
			writer.Close()
			return "", fmt.Errorf("failed to write image data: %w", err)
		}
	}

	contentType := writer.FormDataContentType()
	if err := writer.Close(); err != nil {
		return "", fmt.Errorf("failed to finalize webhook embed payload: %w", err)
	}

	url := w.url
	if wantResponse {
		if strings.Contains(url, "?") {
			url += "&wait=true"
		} else {
			url += "?wait=true"
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, &body)
	if err != nil {
		return "", fmt.Errorf("failed to create webhook request: %w", err)
	}
	req.Header.Set("Content-Type", contentType)

	resp, err := w.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("webhook request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode >= http.StatusBadRequest {
		return "", fmt.Errorf("webhook returned %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}

	if wantResponse && len(respBody) > 0 {
		var msg struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(respBody, &msg); err == nil && msg.ID != "" {
			return msg.ID, nil
		}
	}

	return "", nil
}
