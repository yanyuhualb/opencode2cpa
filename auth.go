package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// AuthEntry is one record of opencode's auth.json.
type AuthEntry struct {
	Type string `json:"type"`
	Key  string `json:"key"`
}

// DefaultAuthFile returns the path of opencode's auth.json, honouring XDG_DATA_HOME.
func DefaultAuthFile() string {
	if dir := os.Getenv("XDG_DATA_HOME"); dir != "" {
		return filepath.Join(dir, "opencode", "auth.json")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "auth.json"
	}
	return filepath.Join(home, ".local", "share", "opencode", "auth.json")
}

// ReadAuthFile loads auth.json. A missing file is not an error: it just means the
// user never ran `opencode auth login`, which is fine for the free tier.
func ReadAuthFile(path string) (map[string]AuthEntry, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var entries map[string]AuthEntry
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, err
	}
	return entries, nil
}

// ResolveKey decides which API key to send upstream and where it came from.
//
// Order: explicit flag, $OPENCODE_API_KEY, then auth.json ("opencode" before
// "opencode-go"). No key at all is valid: the CLI falls back to the literal key
// "public", which unlocks the free models only.
func ResolveKey(explicit, authFile string) (key, source string) {
	if k := strings.TrimSpace(explicit); k != "" {
		return k, "flag"
	}
	if k := strings.TrimSpace(os.Getenv("OPENCODE_API_KEY")); k != "" {
		return k, "env:OPENCODE_API_KEY"
	}
	entries, err := ReadAuthFile(authFile)
	if err == nil {
		for _, name := range []string{"opencode", "opencode-go"} {
			if entry, ok := entries[name]; ok && strings.TrimSpace(entry.Key) != "" {
				return strings.TrimSpace(entry.Key), "auth.json:" + name
			}
		}
	}
	return PublicKey, "public"
}
