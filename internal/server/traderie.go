package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os/exec"
	"strings"

	"github.com/hectorgimenez/koolo/internal/config"
)

const traderieBaseURL = "https://traderie.com/api/diablo2resurrected"

func traderieJSONError(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]interface{}{"error": msg, "success": false})
}

// traderieCurl executes a curl request to Traderie API, bypassing Go's TLS fingerprint
// which Cloudflare blocks. Returns response body bytes.
func traderieCurl(method, rawURL string, body []byte) ([]byte, int, error) {
	args := []string{
		"-s",                  // silent
		"-w", "\n%{http_code}", // append HTTP code
		"-X", method,
		"-H", "Accept: application/json",
		"-H", "Content-Type: application/json",
		"-H", "Accept-Language: pl,en;q=0.9",
		"-H", "User-Agent: Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/145.0.0.0 Safari/537.36",
		"-H", "Referer: https://traderie.com/diablo2resurrected/listings/create",
		"-H", "Origin: https://traderie.com",
	}

	if config.Koolo != nil {
		if config.Koolo.Traderie.CfClearance != "" {
			args = append(args, "-H", "Cookie: cf_clearance="+config.Koolo.Traderie.CfClearance)
		}
		if config.Koolo.Traderie.Token != "" {
			args = append(args, "-H", "Authorization: Bearer "+config.Koolo.Traderie.Token)
		}
	}

	if body != nil {
		args = append(args, "-d", string(body))
	}

	args = append(args, rawURL)

	cmd := exec.Command("curl", args...)
	var out bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, 0, fmt.Errorf("curl failed: %w (stderr: %s)", err, stderr.String())
	}

	output := out.Bytes()
	// Last line is HTTP code, everything before is body
	lastNewline := bytes.LastIndexByte(output, '\n')
	if lastNewline < 0 {
		return nil, 0, fmt.Errorf("unexpected curl output format")
	}

	respBody := output[:lastNewline]
	httpCode := 0
	fmt.Sscanf(string(output[lastNewline+1:]), "%d", &httpCode)

	return respBody, httpCode, nil
}

// traderieCurlMultipart executes a multipart POST via curl (for listing creation).
func traderieCurlMultipart(rawURL string, formBody []byte) ([]byte, int, error) {
	args := []string{
		"-s",
		"-w", "\n%{http_code}",
		"-X", "POST",
		"-H", "Accept: application/json",
		"-H", "Accept-Language: pl,en;q=0.9",
		"-H", "User-Agent: Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/145.0.0.0 Safari/537.36",
		"-H", "Referer: https://traderie.com/diablo2resurrected/listings/create",
		"-H", "Origin: https://traderie.com",
		"-F", "body=" + string(formBody),
	}

	if config.Koolo != nil {
		if config.Koolo.Traderie.CfClearance != "" {
			args = append(args, "-H", "Cookie: cf_clearance="+config.Koolo.Traderie.CfClearance)
		}
		if config.Koolo.Traderie.Token != "" {
			args = append(args, "-H", "Authorization: Bearer "+config.Koolo.Traderie.Token)
		}
	}

	args = append(args, rawURL)

	cmd := exec.Command("curl", args...)
	var out bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, 0, fmt.Errorf("curl failed: %w (stderr: %s)", err, stderr.String())
	}

	output := out.Bytes()
	lastNewline := bytes.LastIndexByte(output, '\n')
	if lastNewline < 0 {
		return nil, 0, fmt.Errorf("unexpected curl output format")
	}

	respBody := output[:lastNewline]
	httpCode := 0
	fmt.Sscanf(string(output[lastNewline+1:]), "%d", &httpCode)

	return respBody, httpCode, nil
}

// traderieWriteResponse checks response and writes JSON to client.
func traderieWriteResponse(w http.ResponseWriter, body []byte, httpCode int) {
	// Check for Cloudflare HTML block
	if httpCode == 403 || httpCode == 503 || (len(body) > 0 && body[0] == '<') {
		preview := string(body)
		if len(preview) > 200 {
			preview = preview[:200]
		}
		slog.Warn("Traderie returned non-JSON (Cloudflare block?)",
			slog.Int("status", httpCode), slog.String("body_preview", preview))
		traderieJSONError(w, fmt.Sprintf("Cloudflare blokuje request (HTTP %d). Odśwież cf_clearance w ustawieniach.", httpCode), http.StatusBadGateway)
		return
	}

	if httpCode != 200 {
		slog.Warn("Traderie error", slog.Int("status", httpCode), slog.String("body", string(body)))
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(httpCode)
	w.Write(body)
}

// GET /api/traderie/search?q=harlequin
func (s *HttpServer) traderieSearchItems(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query().Get("q")
	if query == "" {
		traderieJSONError(w, "q parameter required", http.StatusBadRequest)
		return
	}

	searchURL := fmt.Sprintf("%s/items?search=%s&variants=&active=true&properties=&tags=",
		traderieBaseURL, url.QueryEscape(query))

	slog.Debug("Traderie search", slog.String("query", query))

	body, code, err := traderieCurl("GET", searchURL, nil)
	if err != nil {
		slog.Error("Traderie search failed", slog.Any("error", err))
		traderieJSONError(w, "Traderie request failed: "+err.Error(), http.StatusBadGateway)
		return
	}

	traderieWriteResponse(w, body, code)
}

// GET /api/traderie/listings?item=2487112490
func (s *HttpServer) traderieGetListings(w http.ResponseWriter, r *http.Request) {
	itemID := r.URL.Query().Get("item")
	if itemID == "" {
		traderieJSONError(w, "item parameter required", http.StatusBadRequest)
		return
	}

	listingsURL := fmt.Sprintf("%s/listings?item=%s&selling=true&completed=false&page=0&getMod=true",
		traderieBaseURL, url.QueryEscape(itemID))

	body, code, err := traderieCurl("GET", listingsURL, nil)
	if err != nil {
		traderieJSONError(w, "Traderie request failed: "+err.Error(), http.StatusBadGateway)
		return
	}

	traderieWriteResponse(w, body, code)
}

// TraderieCreateListingRequest is the JSON body sent from the frontend.
type TraderieCreateListingRequest struct {
	Item       string                   `json:"item"`
	ItemType   string                   `json:"itemType"`
	MakeOffer  bool                     `json:"makeOffer"`
	Amount     string                   `json:"amount"`
	Properties []map[string]interface{} `json:"properties"`
}

// POST /api/traderie/create-listing
func (s *HttpServer) traderieCreateListing(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		traderieJSONError(w, "POST required", http.StatusMethodNotAllowed)
		return
	}

	token := config.Koolo.Traderie.Token
	if token == "" {
		traderieJSONError(w, "Traderie token nie skonfigurowany. Wejdź w Ustawienia i dodaj JWT token.", http.StatusUnauthorized)
		return
	}

	var req TraderieCreateListingRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		traderieJSONError(w, "Nieprawidłowy JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	listingBody := map[string]interface{}{
		"acceptListingPrice": false,
		"captcha":            "",
		"captchaManaged":     false,
		"diy":                false,
		"endTime":            "",
		"free":               false,
		"item":               req.Item,
		"itemMode":           nil,
		"itemType":           req.ItemType,
		"makeOffer":          req.MakeOffer,
		"needMaterials":      false,
		"offerBells":         false,
		"offerNmt":           false,
		"offerWishlist":      false,
		"offerWishlistId":    "",
		"selling":            true,
		"standingListing":    false,
		"stockListing":       false,
		"touchTrading":       false,
		"wishlist":           "",
		"amount":             req.Amount,
		"properties":         req.Properties,
	}

	bodyJSON, err := json.Marshal(listingBody)
	if err != nil {
		traderieJSONError(w, "JSON marshal error: "+err.Error(), http.StatusInternalServerError)
		return
	}

	body, code, err := traderieCurlMultipart(traderieBaseURL+"/listings/create", bodyJSON)
	if err != nil {
		traderieJSONError(w, "Traderie request failed: "+err.Error(), http.StatusBadGateway)
		return
	}

	traderieWriteResponse(w, body, code)
}

// GET /api/traderie/status
func (s *HttpServer) traderieGetCookieStatus(w http.ResponseWriter, r *http.Request) {
	hasToken := config.Koolo.Traderie.Token != ""
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"configured": hasToken})
}

// POST /api/traderie/cookies
func (s *HttpServer) traderieSetCookies(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		traderieJSONError(w, "POST required", http.StatusMethodNotAllowed)
		return
	}

	var reqBody struct {
		Token       string `json:"token"`
		CfClearance string `json:"cfClearance"`
	}
	if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
		traderieJSONError(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	config.Koolo.Traderie.Token = strings.TrimSpace(reqBody.Token)
	config.Koolo.Traderie.CfClearance = strings.TrimSpace(reqBody.CfClearance)
	if err := config.SaveKooloConfig(config.Koolo); err != nil {
		traderieJSONError(w, "failed to save config: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"success": true})
}
