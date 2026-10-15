package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// roundTripFunc lets the tests stub the upstream without binding a socket.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func stubServer(mode Mode, key string, freeOnly bool, clientKeys []string, transport roundTripFunc) *Server {
	server := NewServer(mode, "https://stub.invalid/v1", key, "stub", freeOnly, clientKeys)
	server.HTTP = &http.Client{Transport: transport}
	return server
}

func stubResponse(status int, contentType, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{contentType}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func postChat(server *Server, body string, headers map[string]string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	return recorder
}

func TestNormalizeModel(t *testing.T) {
	cases := map[string]string{
		"opencode/grok-code":      "grok-code",
		"opencode-zen/glm-5-free": "glm-5-free",
		"opencode-go/kimi-k3":     "kimi-k3",
		"zen/glm-5-free":          "glm-5-free",
		"grok-code":               "grok-code",
		"  glm-5-free  ":          "glm-5-free",
	}
	for in, want := range cases {
		if got := NormalizeModel(in); got != want {
			t.Errorf("NormalizeModel(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCatalog(t *testing.T) {
	free := ModeZen.FreeCatalog()
	if len(free) == 0 {
		t.Fatal("zen catalog has no free models")
	}
	for _, model := range free {
		if !model.Free() {
			t.Errorf("%s reported as free but costs %v", model.ID, model.CostInput)
		}
	}
	if _, ok := ModeZen.Lookup("grok-code"); !ok {
		t.Error("grok-code missing from the zen catalog")
	}
	if _, ok := ModeGo.Lookup("kimi-k3"); !ok {
		t.Error("kimi-k3 missing from the go catalog")
	}
	for _, model := range ModeGo.Catalog() {
		if model.Free() {
			t.Errorf("%s is listed in the go catalog as free", model.ID)
		}
	}
}

func TestResolveKey(t *testing.T) {
	dir := t.TempDir()
	authFile := filepath.Join(dir, "auth.json")
	write := func(body string) {
		if err := os.WriteFile(authFile, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	t.Setenv("OPENCODE_API_KEY", "")
	if key, source := ResolveKey("explicit", authFile); key != "explicit" || source != "flag" {
		t.Errorf("flag key not honoured: %q %q", key, source)
	}

	write(`{"opencode-go":{"type":"api","key":"go-key"},"opencode":{"type":"api","key":"zen-key"}}`)
	if key, source := ResolveKey("", authFile); key != "zen-key" || source != "auth.json:opencode" {
		t.Errorf("zen key should win: %q %q", key, source)
	}

	write(`{"opencode-go":{"type":"api","key":"go-key"}}`)
	if key, source := ResolveKey("", authFile); key != "go-key" || source != "auth.json:opencode-go" {
		t.Errorf("go key not picked up: %q %q", key, source)
	}

	t.Setenv("OPENCODE_API_KEY", "env-key")
	if key, source := ResolveKey("", authFile); key != "env-key" || source != "env:OPENCODE_API_KEY" {
		t.Errorf("env key should beat auth.json: %q %q", key, source)
	}
	t.Setenv("OPENCODE_API_KEY", "")

	if key, source := ResolveKey("", filepath.Join(dir, "missing.json")); key != PublicKey || source != "public" {
		t.Errorf("missing auth.json should fall back to %q: %q %q", PublicKey, key, source)
	}
}

func TestModelsEndpointListsOnlyFreeModels(t *testing.T) {
	server := stubServer(ModeZen, PublicKey, true, nil, nil)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/models", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body)
	}
	var payload struct {
		Data []struct {
			ID   string `json:"id"`
			Free bool   `json:"free"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Data) != len(ModeZen.FreeCatalog()) {
		t.Errorf("got %d models, want %d", len(payload.Data), len(ModeZen.FreeCatalog()))
	}
	for _, model := range payload.Data {
		if !model.Free {
			t.Errorf("%s is exposed although it is not free", model.ID)
		}
	}
	if strings.Contains(recorder.Body.String(), "claude-sonnet-4-6") {
		t.Error("paid model leaked into the free model list")
	}
}

func TestChatCompletionsForwardsWithRewrittenModel(t *testing.T) {
	var (
		gotPath   string
		gotAuth   string
		gotAccept string
		gotBody   map[string]any
	)
	server := stubServer(ModeZen, PublicKey, true, nil, func(r *http.Request) (*http.Response, error) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotAccept = r.Header.Get("Accept")
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		return stubResponse(http.StatusOK, "application/json",
			`{"id":"chatcmpl-1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"hi"}}]}`), nil
	})

	recorder := postChat(server,
		`{"model":"opencode/grok-code","stream":true,"messages":[{"role":"user","content":"hello"}],"temperature":0.3}`,
		map[string]string{"Accept": "text/event-stream"})

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body)
	}
	if gotPath != "/v1/chat/completions" {
		t.Errorf("upstream path = %q", gotPath)
	}
	if gotAuth != "Bearer "+PublicKey {
		t.Errorf("upstream auth = %q", gotAuth)
	}
	if gotAccept != "text/event-stream" {
		t.Errorf("Accept header not forwarded: %q", gotAccept)
	}
	if gotBody["model"] != "grok-code" {
		t.Errorf("model not rewritten: %v", gotBody["model"])
	}
	if gotBody["temperature"] != 0.3 {
		t.Errorf("payload not passed through: %v", gotBody)
	}
	if !strings.Contains(recorder.Body.String(), "chatcmpl-1") {
		t.Errorf("upstream response not returned: %s", recorder.Body)
	}
}

func TestUpstreamErrorIsPassedThrough(t *testing.T) {
	server := stubServer(ModeZen, PublicKey, true, nil, func(r *http.Request) (*http.Response, error) {
		return stubResponse(http.StatusTooManyRequests, "application/json",
			`{"error":{"message":"FreeUsageLimitError","type":"rate_limit_error"}}`), nil
	})
	recorder := postChat(server, `{"model":"grok-code","messages":[]}`, nil)

	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", recorder.Code)
	}
	if !strings.Contains(recorder.Body.String(), "FreeUsageLimitError") {
		t.Errorf("upstream error body not forwarded: %s", recorder.Body)
	}
}

func TestChatCompletionsRejectsPaidModel(t *testing.T) {
	server := stubServer(ModeZen, PublicKey, true, nil, func(r *http.Request) (*http.Response, error) {
		t.Error("upstream must not be called for a rejected model")
		return nil, nil
	})
	recorder := postChat(server, `{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"hello"}]}`, nil)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (body %s)", recorder.Code, recorder.Body)
	}
	if !strings.Contains(recorder.Body.String(), "model_not_free") {
		t.Errorf("unexpected error body: %s", recorder.Body)
	}
}

func TestUnknownModelRejectedInFreeOnlyMode(t *testing.T) {
	server := stubServer(ModeZen, PublicKey, true, nil, nil)
	recorder := postChat(server, `{"model":"gpt-9-turbo","messages":[]}`, nil)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", recorder.Code)
	}
	if !strings.Contains(recorder.Body.String(), "model_not_available") {
		t.Errorf("unexpected error body: %s", recorder.Body)
	}
}

func TestFreeOnlyCanBeDisabled(t *testing.T) {
	var gotModel any
	server := stubServer(ModeZen, "real-key", false, nil, func(r *http.Request) (*http.Response, error) {
		raw, _ := io.ReadAll(r.Body)
		var payload map[string]any
		_ = json.Unmarshal(raw, &payload)
		gotModel = payload["model"]
		return stubResponse(http.StatusOK, "application/json", `{"ok":true}`), nil
	})
	recorder := postChat(server, `{"model":"claude-sonnet-4-6","messages":[]}`, nil)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body)
	}
	if gotModel != "claude-sonnet-4-6" {
		t.Errorf("model = %v", gotModel)
	}
}

func TestClientKeyEnforcement(t *testing.T) {
	server := stubServer(ModeZen, PublicKey, true, []string{"cpa-secret"}, func(r *http.Request) (*http.Response, error) {
		return stubResponse(http.StatusOK, "application/json", `{"ok":true}`), nil
	})
	body := `{"model":"grok-code","messages":[]}`

	if recorder := postChat(server, body, nil); recorder.Code != http.StatusUnauthorized {
		t.Fatalf("missing client key: status = %d, want 401", recorder.Code)
	}
	if recorder := postChat(server, body, map[string]string{"Authorization": "Bearer cpa-secret"}); recorder.Code != http.StatusOK {
		t.Fatalf("valid client key: status = %d, body = %s", recorder.Code, recorder.Body)
	}
}

func TestUpstreamKeyOverride(t *testing.T) {
	var gotAuth string
	server := stubServer(ModeZen, PublicKey, true, nil, func(r *http.Request) (*http.Response, error) {
		gotAuth = r.Header.Get("Authorization")
		return stubResponse(http.StatusOK, "application/json", `{"ok":true}`), nil
	})
	recorder := postChat(server, `{"model":"grok-code","messages":[]}`, map[string]string{"X-Opencode-Key": "sk-live"})

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	if gotAuth != "Bearer sk-live" {
		t.Errorf("upstream auth = %q", gotAuth)
	}
}

func TestStreamingResponseIsPassedThrough(t *testing.T) {
	server := stubServer(ModeZen, PublicKey, true, nil, func(r *http.Request) (*http.Response, error) {
		return stubResponse(http.StatusOK, "text/event-stream",
			"data: {\"a\":1}\n\ndata: {\"b\":2}\n\ndata: [DONE]\n\n"), nil
	})
	recorder := postChat(server, `{"model":"grok-code","stream":true,"messages":[]}`, nil)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	if got := recorder.Header().Get("Content-Type"); got != "text/event-stream" {
		t.Errorf("content type = %q", got)
	}
	want := "data: {\"a\":1}\n\ndata: {\"b\":2}\n\ndata: [DONE]\n\n"
	if recorder.Body.String() != want {
		t.Errorf("stream body = %q, want %q", recorder.Body.String(), want)
	}
}

func TestHealthz(t *testing.T) {
	server := stubServer(ModeZen, PublicKey, true, nil, nil)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	if !bytes.Contains(recorder.Body.Bytes(), []byte(`"mode":"zen"`)) {
		t.Errorf("unexpected health payload: %s", recorder.Body)
	}
}

func TestOpenCodeHeadersAreSentUpstream(t *testing.T) {
	var got http.Header
	server := stubServer(ModeZen, PublicKey, true, nil, func(r *http.Request) (*http.Response, error) {
		got = r.Header.Clone()
		return stubResponse(http.StatusOK, "application/json", `{"ok":true}`), nil
	})
	server.ExtraHeaders = map[string]string{"x-custom": "1"}

	// The client's own User-Agent must not leak through: the zen free tier gate
	// looks at the opencode one.
	recorder := postChat(server, `{"model":"grok-code","messages":[]}`, map[string]string{"User-Agent": "curl/8.5.0"})
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body)
	}
	if ua := got.Get("User-Agent"); ua != DefaultUserAgent {
		t.Errorf("User-Agent = %q, want %q", ua, DefaultUserAgent)
	}
	if client := got.Get("x-opencode-client"); client != "cli" {
		t.Errorf("x-opencode-client = %q, want cli", client)
	}
	if got.Get("x-opencode-session") == "" || got.Get("x-opencode-request") == "" {
		t.Errorf("missing session/request headers: %v", got)
	}
	if got.Get("x-custom") != "1" {
		t.Errorf("extra header not applied: %v", got)
	}
}

func TestPerRequestHeaderIDs(t *testing.T) {
	ids := make([]string, 0, 2)
	server := stubServer(ModeZen, PublicKey, true, nil, func(r *http.Request) (*http.Response, error) {
		ids = append(ids, r.Header.Get("x-opencode-session"))
		return stubResponse(http.StatusOK, "application/json", `{"ok":true}`), nil
	})
	for range 2 {
		postChat(server, `{"model":"grok-code","messages":[]}`, nil)
	}
	if len(ids) != 2 || ids[0] == "" || ids[0] == ids[1] {
		t.Errorf("session ids should be non-empty and unique, got %v", ids)
	}
}

func TestParseHeaders(t *testing.T) {
	headers, err := parseHeaders([]string{"X-A=1", " x-b = 2 "})
	if err != nil {
		t.Fatal(err)
	}
	if headers["X-A"] != "1" || headers["x-b"] != "2" {
		t.Errorf("unexpected headers: %v", headers)
	}
	if _, err := parseHeaders([]string{"oops"}); err == nil {
		t.Error("expected an error for a header without =")
	}
	if headers, err := parseHeaders(nil); err != nil || headers != nil {
		t.Errorf("nil input should stay nil: %v %v", headers, err)
	}
}
