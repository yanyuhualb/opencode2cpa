package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Server exposes an OpenCode Zen catalog as an OpenAI compatible API so that
// CLIProxyAPI (or any other OpenAI client) can consume it.
type Server struct {
	Mode       Mode
	Upstream   string
	APIKey     string
	KeySource  string
	FreeOnly   bool
	ClientKeys []string
	HTTP       *http.Client

	// UserAgent is sent upstream. The zen free tier is gated on the headers the
	// opencode CLI sends, so this must stay `opencode/<version>`.
	UserAgent string
	// ExtraHeaders are added to every upstream request (flag `--header k=v`).
	ExtraHeaders map[string]string
}

// NewServer builds a Server. upstream may be empty, in which case the default
// endpoint for the mode is used.
func NewServer(mode Mode, upstream, apiKey, keySource string, freeOnly bool, clientKeys []string) *Server {
	if upstream == "" {
		upstream = mode.BaseURL()
	}
	return &Server{
		Mode:       mode,
		Upstream:   strings.TrimSuffix(upstream, "/"),
		APIKey:     apiKey,
		KeySource:  keySource,
		FreeOnly:   freeOnly,
		ClientKeys: clientKeys,
		UserAgent:  DefaultUserAgent,
		HTTP: &http.Client{
			Transport: &http.Transport{
				Proxy:                 http.ProxyFromEnvironment,
				ResponseHeaderTimeout: 5 * time.Minute,
				IdleConnTimeout:       90 * time.Second,
			},
		},
	}
}

// Handler returns the HTTP routes of the proxy.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.HandleFunc("GET /v1/models", s.handleModels)
	mux.HandleFunc("POST /v1/chat/completions", s.handleChatCompletions)
	return mux
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status":     "ok",
		"mode":       string(s.Mode),
		"upstream":   s.Upstream,
		"key_source": s.KeySource,
		"free_only":  s.FreeOnly,
	})
}

func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	all := r.URL.Query().Get("all") == "1"
	models := s.Mode.FreeCatalog()
	if all || !s.FreeOnly {
		if all || len(models) == 0 {
			models = s.Mode.Catalog()
		}
	}
	data := make([]map[string]any, 0, len(models))
	for _, model := range models {
		data = append(data, map[string]any{
			"id":             model.ID,
			"object":         "model",
			"created":        0,
			"owned_by":       "opencode",
			"name":           model.Name,
			"context_length": model.Context,
			"free":           model.Free(),
			"pricing":        map[string]float64{"prompt": model.CostInput / 1e6, "completion": model.CostOutput / 1e6},
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": data})
}

func (s *Server) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	if !s.authorize(w, r) {
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 32<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "failed to read request body: "+err.Error(), "")
		return
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "request body is not valid JSON: "+err.Error(), "")
		return
	}
	requested, _ := payload["model"].(string)
	model := NormalizeModel(requested)
	if model == "" {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "missing model", "invalid_request")
		return
	}
	if s.FreeOnly {
		entry, ok := s.Mode.Lookup(model)
		if !ok {
			writeError(w, http.StatusForbidden, "invalid_request_error",
				fmt.Sprintf("unknown model %q; run with --free-only=false to pass unknown models through", model), "model_not_available")
			return
		}
		if !entry.Free() {
			writeError(w, http.StatusForbidden, "invalid_request_error",
				fmt.Sprintf("model %q is not free; free models: %s", model, strings.Join(freeModelIDs(s.Mode), ", ")), "model_not_free")
			return
		}
	}
	payload["model"] = model
	encoded, err := json.Marshal(payload)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error(), "")
		return
	}
	s.forward(w, r, "/chat/completions", encoded)
}

func (s *Server) authorize(w http.ResponseWriter, r *http.Request) bool {
	if len(s.ClientKeys) == 0 {
		return true
	}
	token := bearer(r.Header.Get("Authorization"))
	for _, key := range s.ClientKeys {
		if token != "" && token == key {
			return true
		}
	}
	writeError(w, http.StatusUnauthorized, "authentication_error", "invalid API key", "invalid_api_key")
	return false
}

func (s *Server) forward(w http.ResponseWriter, r *http.Request, path string, body []byte) {
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, s.Upstream+path, bytes.NewReader(body))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error(), "")
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if accept := r.Header.Get("Accept"); accept != "" {
		req.Header.Set("Accept", accept)
	}
	key := s.APIKey
	if override := strings.TrimSpace(r.Header.Get("X-Opencode-Key")); override != "" {
		key = override
	}
	req.Header.Set("Authorization", "Bearer "+key)
	s.applyOpenCodeHeaders(req)

	resp, err := s.HTTP.Do(req)
	if err != nil {
		writeError(w, http.StatusBadGateway, "api_error", "upstream request failed: "+err.Error(), "")
		return
	}
	defer resp.Body.Close()

	if ct := resp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.WriteHeader(resp.StatusCode)
	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 32<<10)
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if _, writeErr := w.Write(buf[:n]); writeErr != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if readErr != nil {
			return
		}
	}
}

// applyOpenCodeHeaders reproduces the headers the opencode CLI attaches to every
// request for its own providers. Without them the zen endpoint answers
// 403 "OpenCode's free tier can only be used from within OpenCode".
func (s *Server) applyOpenCodeHeaders(req *http.Request) {
	userAgent := s.UserAgent
	if userAgent == "" {
		userAgent = DefaultUserAgent
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("x-opencode-client", "cli")
	req.Header.Set("x-opencode-session", randomID())
	req.Header.Set("x-opencode-request", randomID())
	for name, value := range s.ExtraHeaders {
		req.Header.Set(name, value)
	}
}

// randomID returns a short random hex string, used for the per request
// x-opencode-session / x-opencode-request headers.
func randomID() string {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "0000000000000000"
	}
	return hex.EncodeToString(buf[:])
}

// NormalizeModel strips the provider prefixes used by opencode ("opencode/grok-code")
// and by common client aliases ("zen/grok-code").
func NormalizeModel(model string) string {
	model = strings.TrimSpace(model)
	for _, prefix := range []string{"opencode-zen/", "opencode-go/", "opencode/", "zen/", "go/"} {
		if rest, ok := strings.CutPrefix(model, prefix); ok {
			return strings.TrimSpace(rest)
		}
	}
	return model
}

func freeModelIDs(mode Mode) []string {
	models := mode.FreeCatalog()
	ids := make([]string, 0, len(models))
	for _, model := range models {
		ids = append(ids, model.ID)
	}
	return ids
}

func bearer(header string) string {
	value := strings.TrimSpace(header)
	if rest, ok := strings.CutPrefix(value, "Bearer "); ok {
		return strings.TrimSpace(rest)
	}
	return value
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeError(w http.ResponseWriter, status int, kind, message, code string) {
	err := map[string]any{"message": message, "type": kind}
	if code != "" {
		err["code"] = code
	}
	writeJSON(w, status, map[string]any{"error": err})
}
