// Package registry loads the model registry (models.json) that maps public
// model IDs to a provider, an upstream ID, and their supported reasoning
// efforts. It is the single source of truth for what the wrapper advertises
// and how each model handles thinking/reasoning -- replacing the model and
// THINKING_* env lists that used to live in config.
package registry

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// Provider names understood by the wrapper.
const (
	ProviderAnthropic = "anthropic"
	ProviderOpenAI    = "openai"
)

// Thinking modes (anthropic only). They encode how a model treats an explicit
// "off" reasoning effort:
//
//	always-on  -- thinking cannot be disabled; "off" is ignored (e.g. Fable 5).
//	default-on -- thinks unless disabled; "off" sends an explicit disable (Sonnet 5).
//	opt-in     -- no thinking unless an effort is requested (Opus 4.8).
const (
	ModeAlwaysOn  = "always-on"
	ModeDefaultOn = "default-on"
	ModeOptIn     = "opt-in"
)

// Reasoning holds a model's supported effort ladder.
type Reasoning struct {
	Efforts []string `json:"efforts"`
	Default string   `json:"default"`
	Mode    string   `json:"mode,omitempty"`
}

// Model is one entry in the registry.
type Model struct {
	ID               string    `json:"id"`
	Provider         string    `json:"provider"`
	UpstreamID       string    `json:"upstream_id"`
	Aliases          []string  `json:"aliases,omitempty"`
	Reasoning        Reasoning `json:"reasoning"`
	DefaultMaxTokens int       `json:"default_max_tokens,omitempty"`
}

// AllowsEffort reports whether effort is in the model's ladder (case-insensitive).
func (m Model) AllowsEffort(effort string) bool {
	e := strings.ToLower(strings.TrimSpace(effort))
	for _, v := range m.Reasoning.Efforts {
		if strings.ToLower(v) == e {
			return true
		}
	}
	return false
}

// Registry is the loaded, indexed model list.
type Registry struct {
	models []Model
	byName map[string]int // id + aliases (lowercased) -> index into models
}

type file struct {
	Models []Model `json:"models"`
}

// Load reads and validates the registry JSON at path.
func Load(path string) (*Registry, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading models config %q: %w", path, err)
	}
	var f file
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("parsing models config %q: %w", path, err)
	}
	if len(f.Models) == 0 {
		return nil, fmt.Errorf("models config %q defines no models", path)
	}

	reg := &Registry{byName: make(map[string]int)}
	for _, m := range f.Models {
		if strings.TrimSpace(m.ID) == "" {
			return nil, fmt.Errorf("models config has an entry with an empty id")
		}
		switch m.Provider {
		case ProviderAnthropic, ProviderOpenAI:
		default:
			return nil, fmt.Errorf("model %q has unknown provider %q", m.ID, m.Provider)
		}
		if m.UpstreamID == "" {
			m.UpstreamID = m.ID
		}
		idx := len(reg.models)
		reg.models = append(reg.models, m)
		reg.index(m.ID, idx)
		for _, a := range m.Aliases {
			reg.index(a, idx)
		}
	}
	return reg, nil
}

func (r *Registry) index(name string, idx int) {
	key := strings.ToLower(strings.TrimSpace(name))
	if key == "" {
		return
	}
	if _, exists := r.byName[key]; !exists {
		r.byName[key] = idx
	}
}

// Lookup resolves a public model ID or alias (case-insensitive).
func (r *Registry) Lookup(id string) (Model, bool) {
	idx, ok := r.byName[strings.ToLower(strings.TrimSpace(id))]
	if !ok {
		return Model{}, false
	}
	return r.models[idx], true
}

// Public returns the models whose provider is enabled, in registry order.
func (r *Registry) Public(enabled map[string]bool) []Model {
	out := make([]Model, 0, len(r.models))
	for _, m := range r.models {
		if enabled[m.Provider] {
			out = append(out, m)
		}
	}
	return out
}

// First returns the first model whose provider is enabled (used to pick a
// default model when DEFAULT_MODEL is unset). Returns false if none match.
func (r *Registry) First(enabled map[string]bool) (Model, bool) {
	for _, m := range r.models {
		if enabled[m.Provider] {
			return m, true
		}
	}
	return Model{}, false
}
