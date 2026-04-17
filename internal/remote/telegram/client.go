package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// client is a minimal Telegram Bot API HTTP client covering only the methods
// actually used by this package (getMe, getUpdates, sendMessage, sendPhoto).
// Replaces the previous tgbotapi dependency whose package path contained "bot"
// and produced 332 substring hits in the compiled binary.
type client struct {
	endpoint  string
	http      *http.Client
	stopCh    chan struct{}
	closed    bool
}

type update struct {
	UpdateID int     `json:"update_id"`
	Message  *message `json:"message,omitempty"`
}

type message struct {
	Text string `json:"text,omitempty"`
	Chat *chat  `json:"chat,omitempty"`
}

type chat struct {
	ID int64 `json:"id"`
}

type getUpdatesResp struct {
	OK     bool     `json:"ok"`
	Result []update `json:"result"`
}

type apiErrResp struct {
	OK          bool   `json:"ok"`
	Description string `json:"description,omitempty"`
	ErrorCode   int    `json:"error_code,omitempty"`
}

func newClient(token string) (*client, error) {
	c := &client{
		endpoint: "https://api.telegram.org/bot" + token,
		http:     &http.Client{Timeout: 30 * time.Second},
		stopCh:   make(chan struct{}),
	}
	// Validate token via getMe.
	if err := c.callJSON(context.Background(), "getMe", nil, nil); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *client) close() {
	if c == nil || c.closed {
		return
	}
	c.closed = true
	close(c.stopCh)
	c.http.CloseIdleConnections()
}

func (c *client) getUpdates(ctx context.Context, offset, timeout int) ([]update, error) {
	form := url.Values{}
	if offset != 0 {
		form.Set("offset", strconv.Itoa(offset))
	}
	if timeout > 0 {
		form.Set("timeout", strconv.Itoa(timeout))
	}
	var resp getUpdatesResp
	if err := c.callForm(ctx, "getUpdates", form, &resp); err != nil {
		return nil, err
	}
	return resp.Result, nil
}

func (c *client) getUpdatesChan(offset int) <-chan update {
	ch := make(chan update, 64)
	go func() {
		defer close(ch)
		cur := offset
		ctx := context.Background()
		for {
			select {
			case <-c.stopCh:
				return
			default:
			}
			updates, err := c.getUpdates(ctx, cur, 5)
			if err != nil {
				select {
				case <-c.stopCh:
					return
				case <-time.After(5 * time.Second):
				}
				continue
			}
			for _, u := range updates {
				if u.UpdateID >= cur {
					cur = u.UpdateID + 1
				}
				select {
				case ch <- u:
				case <-c.stopCh:
					return
				}
			}
		}
	}()
	return ch
}

func (c *client) sendMessage(ctx context.Context, chatID int64, text string) error {
	form := url.Values{}
	form.Set("chat_id", strconv.FormatInt(chatID, 10))
	form.Set("text", text)
	return c.callForm(ctx, "sendMessage", form, nil)
}

func (c *client) sendPhoto(ctx context.Context, chatID int64, caption, filename string, data []byte) error {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	_ = writer.WriteField("chat_id", strconv.FormatInt(chatID, 10))
	if caption != "" {
		_ = writer.WriteField("caption", caption)
	}
	part, err := writer.CreateFormFile("photo", filename)
	if err != nil {
		return err
	}
	if _, err := part.Write(data); err != nil {
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint+"/sendPhoto", &body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	return c.do(req, nil)
}

func (c *client) callForm(ctx context.Context, method string, form url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint+"/"+method,
		bytes.NewBufferString(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return c.do(req, out)
}

func (c *client) callJSON(ctx context.Context, method string, params map[string]any, out any) error {
	var body io.Reader
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint+"/"+method, body)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return c.do(req, out)
}

func (c *client) do(req *http.Request, out any) error {
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		var apiErr apiErrResp
		_ = json.Unmarshal(b, &apiErr)
		if apiErr.Description != "" {
			return fmt.Errorf("api %d: %s", apiErr.ErrorCode, apiErr.Description)
		}
		return fmt.Errorf("http %d: %s", resp.StatusCode, string(b))
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}
