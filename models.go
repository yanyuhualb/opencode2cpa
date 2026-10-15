package main

// Model is one entry of an OpenCode Zen model catalog.
type Model struct {
	ID         string
	Name       string
	CostInput  float64
	CostOutput float64
	Context    int
}

// Free reports whether the model is usable without an OpenCode subscription.
// This mirrors the rule the opencode CLI applies for anonymous users
// (apiKey "public"): only models with a zero input cost stay enabled.
func (m Model) Free() bool { return m.CostInput == 0 }

// Mode identifies which OpenCode Zen endpoint we talk to.
type Mode string

const (
	// ModeZen is https://opencode.ai/zen/v1 — free models work with the api key "public".
	ModeZen Mode = "zen"
	// ModeGo is https://opencode.ai/zen/go/v1 — the paid OpenCode Go subscription catalog.
	ModeGo Mode = "go"
)

const (
	// OpenCodeVersion is the CLI version this proxy mimics. The zen free tier is
	// gated on the headers the CLI sends (see DefaultUserAgent), so keep it in
	// sync with the installed opencode when you upgrade.
	OpenCodeVersion = "1.18.15"
	// DefaultUserAgent is the User-Agent the opencode CLI sends upstream.
	DefaultUserAgent = "opencode/" + OpenCodeVersion

	ZenBaseURL = "https://opencode.ai/zen/v1"
	GoBaseURL  = "https://opencode.ai/zen/go/v1"
	// PublicKey is what the CLI sends when the user is not logged in.
	PublicKey = "public"
)

// BaseURL returns the default upstream base URL for a mode.
func (m Mode) BaseURL() string {
	if m == ModeGo {
		return GoBaseURL
	}
	return ZenBaseURL
}

// Catalog returns every model known for the mode.
func (m Mode) Catalog() []Model {
	if m == ModeGo {
		return GoModels
	}
	return ZenModels
}

// FreeCatalog returns the models usable without a subscription (zen mode only).
func (m Mode) FreeCatalog() []Model {
	var out []Model
	for _, model := range m.Catalog() {
		if model.Free() {
			out = append(out, model)
		}
	}
	return out
}

// Lookup finds a model by id.
func (m Mode) Lookup(id string) (Model, bool) {
	for _, model := range m.Catalog() {
		if model.ID == id {
			return model, true
		}
	}
	return Model{}, false
}
