// Command opencode-zen-proxy exposes the free models of OpenCode Zen
// (https://opencode.ai/zen/v1) as an OpenAI compatible API so that CLIProxyAPI
// and other OpenAI clients can use them.
//
// Typical use with CLIProxyAPI: see cliproxyapi/config.example.yaml.
package main

import (
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

func main() {
	var (
		addr      = flag.String("addr", envOr("ZEN_PROXY_ADDR", "127.0.0.1:8320"), "listen address")
		mode      = flag.String("mode", envOr("ZEN_PROXY_MODE", "zen"), "upstream catalog: zen (free models) or go (OpenCode Go subscription)")
		upstream  = flag.String("upstream", envOr("ZEN_PROXY_UPSTREAM", ""), "override upstream base URL (default: the one belonging to --mode)")
		key       = flag.String("key", "", "opencode API key (default: $OPENCODE_API_KEY, then opencode's auth.json)")
		authFile  = flag.String("auth-file", envOr("ZEN_PROXY_AUTH_FILE", DefaultAuthFile()), "path to opencode auth.json")
		freeOnly  = flag.Bool("free-only", envOr("ZEN_PROXY_FREE_ONLY", "true") != "false", "zen mode: reject models that are not free")
		clientKey = flag.String("client-key", envOr("ZEN_PROXY_KEY", ""), "optional: comma separated bearer tokens clients must present")
		check     = flag.Bool("check", false, "print the resolved configuration and exit (no listening)")
	)
	flag.Parse()

	parsedMode := Mode(strings.ToLower(strings.TrimSpace(*mode)))
	if parsedMode != ModeZen && parsedMode != ModeGo {
		log.Fatalf("invalid --mode %q: want zen or go", *mode)
	}
	apiKey, source := ResolveKey(*key, *authFile)
	if parsedMode == ModeGo && source == "public" {
		log.Printf("warning: go mode without an API key; the go endpoint rejects anonymous requests")
	}
	server := NewServer(parsedMode, *upstream, apiKey, source, *freeOnly, splitList(*clientKey))

	if *check {
		fmt.Printf("mode:        %s\n", parsedMode)
		fmt.Printf("upstream:    %s\n", server.Upstream)
		fmt.Printf("free_only:   %v\n", *freeOnly)
		fmt.Printf("key_source:  %s\n", source)
		fmt.Printf("key:         %s\n", maskKey(apiKey))
		fmt.Printf("auth_file:   %s\n", *authFile)
		fmt.Printf("models:      %d free / %d total\n", len(parsedMode.FreeCatalog()), len(parsedMode.Catalog()))
		return
	}

	httpServer := &http.Server{
		Addr:              *addr,
		Handler:           logRequests(server.Handler()),
		ReadHeaderTimeout: 30 * time.Second,
	}
	log.Printf("opencode-zen-proxy listening on http://%s", *addr)
	log.Printf("  mode=%s upstream=%s free_only=%v key_source=%s", parsedMode, server.Upstream, *freeOnly, source)
	if source == "public" {
		log.Printf("  no API key found: only the free models of the zen catalog will work (upstream key %q)", PublicKey)
	}
	if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("server error: %v", err)
	}
}

func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(recorder, r)
		log.Printf("%s %s -> %d (%s)", r.Method, r.URL.Path, recorder.status, time.Since(start).Round(time.Millisecond))
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(status int) {
	s.status = status
	s.ResponseWriter.WriteHeader(status)
}

// Flush keeps streaming responses streaming through the wrapper.
func (s *statusRecorder) Flush() {
	if flusher, ok := s.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

// maskKey keeps the head of a key so logs stay useful but not leaky.
func maskKey(key string) string {
	if len(key) <= 8 {
		return key
	}
	return key[:6] + "..." + key[len(key)-4:]
}

func envOr(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func splitList(value string) []string {
	var out []string
	for _, item := range strings.Split(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}
