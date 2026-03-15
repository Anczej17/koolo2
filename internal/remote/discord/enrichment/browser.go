package enrichment

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
)

// FlareSolverr is a client for the FlareSolverr proxy that bypasses Cloudflare.
// See: https://github.com/FlareSolverr/FlareSolverr
type FlareSolverr struct {
	baseURL string
	client  *http.Client
	logger  *slog.Logger

	// Session management for authenticated requests (e.g. d2jsp login)
	sessionID string
	sessionMu sync.Mutex
}

// NewFlareSolverr creates a FlareSolverr client.
// Default URL is http://localhost:8191/v1 if empty.
func NewFlareSolverr(baseURL string, logger *slog.Logger) *FlareSolverr {
	if baseURL == "" {
		baseURL = "http://localhost:8191/v1"
	}
	return &FlareSolverr{
		baseURL: baseURL,
		client:  &http.Client{Timeout: 90 * time.Second},
		logger:  logger,
	}
}

// FlareCookie represents a cookie to inject into FlareSolverr requests.
// Per FlareSolverr API docs, cookies support: name, value, domain, path, expires, httpOnly, secure, sameSite.
type FlareCookie struct {
	Name     string `json:"name"`
	Value    string `json:"value"`
	Domain   string `json:"domain,omitempty"`
	Path     string `json:"path,omitempty"`
	Expires  int64  `json:"expires,omitempty"`
	HttpOnly bool   `json:"httpOnly,omitempty"`
	Secure   bool   `json:"secure,omitempty"`
	SameSite string `json:"sameSite,omitempty"`
}

type flareRequest struct {
	Cmd        string        `json:"cmd"`
	URL        string        `json:"url,omitempty"`
	Session    string        `json:"session,omitempty"`
	MaxTimeout int           `json:"maxTimeout,omitempty"`
	PostData   string        `json:"postData,omitempty"`
	Cookies    []FlareCookie `json:"cookies,omitempty"`
}

type flareResponse struct {
	Status   string `json:"status"`
	Message  string `json:"message"`
	Session  string `json:"session"`
	Solution struct {
		URL       string `json:"url"`
		Status    int    `json:"status"`
		Response  string `json:"response"`
		UserAgent string `json:"userAgent"`
	} `json:"solution"`
}

// GetPage fetches a URL through FlareSolverr, bypassing Cloudflare.
// Returns the full HTML of the page after CF challenge is resolved.
func (f *FlareSolverr) GetPage(ctx context.Context, targetURL string) (string, error) {
	return f.doRequest(ctx, "request.get", targetURL, "")
}

// GetPageWithCookies fetches a URL through FlareSolverr with injected cookies.
// FlareSolverr handles Cloudflare challenge, cookies provide site authentication.
func (f *FlareSolverr) GetPageWithCookies(ctx context.Context, targetURL string, cookies []FlareCookie) (string, error) {
	reqBody := flareRequest{
		Cmd:        "request.get",
		URL:        targetURL,
		MaxTimeout: 45000,
		Cookies:    cookies,
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("failed to marshal flaresolverr request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", f.baseURL, bytes.NewReader(jsonData))
	if err != nil {
		return "", fmt.Errorf("failed to create flaresolverr request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	f.logger.Debug("FlareSolverr request with cookies", slog.String("url", targetURL), slog.Int("cookieCount", len(cookies)))

	return f.executeRequest(req, targetURL)
}

// ParseCookieString parses a raw cookie header string ("name1=val1; name2=val2")
// into FlareCookie slice for injection into FlareSolverr.
// domain is set on each cookie so the browser sends them to the correct site.
func ParseCookieString(raw string, domain string) []FlareCookie {
	var cookies []FlareCookie
	for _, part := range strings.Split(raw, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		eq := strings.IndexByte(part, '=')
		if eq < 1 {
			continue
		}
		cookies = append(cookies, FlareCookie{
			Name:   strings.TrimSpace(part[:eq]),
			Value:  strings.TrimSpace(part[eq+1:]),
			Domain: domain,
			Path:   "/",
		})
	}
	return cookies
}

// GetPageWithSession fetches a URL using a persistent browser session.
// The session maintains cookies (e.g. after login).
func (f *FlareSolverr) GetPageWithSession(ctx context.Context, targetURL string) (string, error) {
	f.sessionMu.Lock()
	sid := f.sessionID
	f.sessionMu.Unlock()

	if sid == "" {
		return "", fmt.Errorf("no FlareSolverr session active")
	}
	return f.doSessionRequest(ctx, "request.get", targetURL, "", sid, nil)
}

// GetPageWithSessionAndCookies fetches a URL using a persistent session with injected cookies.
// Cookies are set in the session browser before navigation — they persist for subsequent requests.
func (f *FlareSolverr) GetPageWithSessionAndCookies(ctx context.Context, targetURL string, cookies []FlareCookie) (string, error) {
	f.sessionMu.Lock()
	sid := f.sessionID
	f.sessionMu.Unlock()

	if sid == "" {
		return "", fmt.Errorf("no FlareSolverr session active")
	}
	return f.doSessionRequest(ctx, "request.get", targetURL, "", sid, cookies)
}

// PostWithSession submits a POST request using the persistent session.
func (f *FlareSolverr) PostWithSession(ctx context.Context, targetURL string, postData string) (string, error) {
	f.sessionMu.Lock()
	sid := f.sessionID
	f.sessionMu.Unlock()

	if sid == "" {
		return "", fmt.Errorf("no FlareSolverr session active")
	}
	return f.doSessionRequest(ctx, "request.post", targetURL, postData, sid, nil)
}

// PostWithSessionAndCookies submits a POST request using a persistent session with injected cookies.
func (f *FlareSolverr) PostWithSessionAndCookies(ctx context.Context, targetURL string, postData string, cookies []FlareCookie) (string, error) {
	f.sessionMu.Lock()
	sid := f.sessionID
	f.sessionMu.Unlock()

	if sid == "" {
		return "", fmt.Errorf("no FlareSolverr session active")
	}
	return f.doSessionRequest(ctx, "request.post", targetURL, postData, sid, cookies)
}

// CreateSession creates a persistent FlareSolverr browser session.
func (f *FlareSolverr) CreateSession(ctx context.Context) (string, error) {
	reqBody := flareRequest{Cmd: "sessions.create"}
	jsonData, _ := json.Marshal(reqBody)

	req, err := http.NewRequestWithContext(ctx, "POST", f.baseURL, bytes.NewReader(jsonData))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := f.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to create session: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	var result flareResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return "", err
	}
	if result.Status != "ok" {
		return "", fmt.Errorf("session create failed: %s", result.Message)
	}

	f.sessionMu.Lock()
	f.sessionID = result.Session
	f.sessionMu.Unlock()

	f.logger.Info("FlareSolverr session created", slog.String("session", result.Session))
	return result.Session, nil
}

// DestroySession destroys the current persistent session.
func (f *FlareSolverr) DestroySession(ctx context.Context) {
	f.sessionMu.Lock()
	sid := f.sessionID
	f.sessionID = ""
	f.sessionMu.Unlock()

	if sid == "" {
		return
	}

	reqBody := flareRequest{Cmd: "sessions.destroy", Session: sid}
	jsonData, _ := json.Marshal(reqBody)

	req, _ := http.NewRequestWithContext(ctx, "POST", f.baseURL, bytes.NewReader(jsonData))
	if req != nil {
		req.Header.Set("Content-Type", "application/json")
		f.client.Do(req)
	}
	f.logger.Info("FlareSolverr session destroyed", slog.String("session", sid))
}

// HasSession returns true if there's an active session.
func (f *FlareSolverr) HasSession() bool {
	f.sessionMu.Lock()
	defer f.sessionMu.Unlock()
	return f.sessionID != ""
}

func (f *FlareSolverr) doRequest(ctx context.Context, cmd, targetURL, postData string) (string, error) {
	reqBody := flareRequest{
		Cmd:        cmd,
		URL:        targetURL,
		MaxTimeout: 45000,
	}
	if postData != "" {
		reqBody.PostData = postData
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("failed to marshal flaresolverr request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", f.baseURL, bytes.NewReader(jsonData))
	if err != nil {
		return "", fmt.Errorf("failed to create flaresolverr request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	f.logger.Debug("FlareSolverr request", slog.String("cmd", cmd), slog.String("url", targetURL))

	return f.executeRequest(req, targetURL)
}

func (f *FlareSolverr) doSessionRequest(ctx context.Context, cmd, targetURL, postData, session string, cookies []FlareCookie) (string, error) {
	reqBody := flareRequest{
		Cmd:        cmd,
		URL:        targetURL,
		Session:    session,
		MaxTimeout: 45000,
	}
	if postData != "" {
		reqBody.PostData = postData
	}
	if len(cookies) > 0 {
		reqBody.Cookies = cookies
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("failed to marshal flaresolverr request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", f.baseURL, bytes.NewReader(jsonData))
	if err != nil {
		return "", fmt.Errorf("failed to create flaresolverr request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	f.logger.Debug("FlareSolverr session request", slog.String("cmd", cmd), slog.String("url", targetURL), slog.String("session", session), slog.Int("cookies", len(cookies)))

	return f.executeRequest(req, targetURL)
}

func (f *FlareSolverr) executeRequest(req *http.Request, targetURL string) (string, error) {
	resp, err := f.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("flaresolverr request failed (is FlareSolverr running at %s?): %w", f.baseURL, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read flaresolverr response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("flaresolverr returned %d: %s", resp.StatusCode, string(body))
	}

	var result flareResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("failed to decode flaresolverr response: %w", err)
	}

	if result.Status != "ok" {
		return "", fmt.Errorf("flaresolverr error: %s", result.Message)
	}

	if result.Solution.Status != 200 {
		return "", fmt.Errorf("target page returned status %d", result.Solution.Status)
	}

	f.logger.Debug("FlareSolverr success", slog.String("url", targetURL), slog.Int("htmlLen", len(result.Solution.Response)))

	return result.Solution.Response, nil
}
