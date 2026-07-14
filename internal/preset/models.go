package preset

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// ProviderModels is the discoverable model inventory of one provider.
type ProviderModels struct {
	Provider string
	BaseURL  string
	Loopback bool
	Online   *bool    // probe result for loopback providers; nil when not probed
	Declared []string // model IDs declared under provider.<name>.models in opencode.json
	Served   []string // model IDs the live endpoint reports (loopback providers only)
}

// Ref returns the full provider/model reference for a model ID.
func (pm ProviderModels) Ref(model string) string { return pm.Provider + "/" + model }

// openaiModelList is the OpenAI-compatible GET /models response shape,
// which ollama, LM Studio, vLLM, and llama.cpp all serve.
type openaiModelList struct {
	Data []struct {
		ID string `json:"id"`
	} `json:"data"`
}

// ListModels enumerates the models actually available per provider: the
// ones declared in the profile's opencode.json, plus — for loopback-hosted
// providers — a live query of the server's /models endpoint so the listing
// reflects what is really loaded right now. Remote providers are never
// queried (that would need API keys and network round-trips); providers
// declared without a models map are listed so users know they exist, with
// discovery left to OpenCode.
func (m *Manager) ListModels() ([]ProviderModels, error) {
	cfg, _, err := m.loadOpencodeConfig()
	if err != nil {
		return nil, err
	}
	if cfg == nil {
		return nil, fmt.Errorf("no opencode.json[c] in %s — providers are declared there", m.opencodeDir)
	}

	names := make([]string, 0, len(cfg.Provider))
	for name := range cfg.Provider {
		names = append(names, name)
	}
	sort.Strings(names)

	var out []ProviderModels
	for _, name := range names {
		pc := cfg.Provider[name]
		pm := ProviderModels{Provider: name, BaseURL: pc.Options.BaseURL}

		for model := range pc.Models {
			pm.Declared = append(pm.Declared, model)
		}
		sort.Strings(pm.Declared)

		if pc.Options.BaseURL != "" && isLoopbackURL(pc.Options.BaseURL) {
			pm.Loopback = true
			online := false
			if served, err := fetchServedModels(pc.Options.BaseURL); err == nil {
				online = true
				pm.Served = served
			}
			pm.Online = &online
		}
		out = append(out, pm)
	}
	return out, nil
}

// fetchServedModels queries an OpenAI-compatible /models endpoint and
// returns the sorted model IDs it serves.
func fetchServedModels(baseURL string) ([]string, error) {
	resp, err := probeClient.Get(strings.TrimSuffix(baseURL, "/") + "/models")
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}

	var list openaiModelList
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		return nil, err
	}
	models := make([]string, 0, len(list.Data))
	for _, m := range list.Data {
		if m.ID != "" {
			models = append(models, m.ID)
		}
	}
	sort.Strings(models)
	return models, nil
}

// CreateAll generates a preset assigning one model to every agent and
// category entry present in the live oh-my-openagent config — the
// "set everything to X" one-liner. The ref must be provider/model form;
// deeper validation runs on `preset use` as usual.
func (m *Manager) CreateAll(name, ref string, force bool) (*Preset, string, error) {
	if !strings.Contains(ref, "/") {
		return nil, "", fmt.Errorf("model %q must use provider/model form", ref)
	}
	lf, err := m.LiveFile()
	if err != nil {
		return nil, "", err
	}
	if !lf.Exists {
		return nil, "", fmt.Errorf("no oh-my-openagent config found in %s — nothing to enumerate agents from", m.opencodeDir)
	}
	state, _, err := m.readLiveState(lf)
	if err != nil {
		return nil, "", err
	}

	rawRef, err := json.Marshal(ref)
	if err != nil {
		return nil, "", err
	}
	assignAll := func(live map[string]map[string]json.RawMessage) map[string]Entry {
		if len(live) == 0 {
			return nil
		}
		out := map[string]Entry{}
		for entryName := range live {
			out[entryName] = Entry{"model": rawRef}
		}
		return out
	}

	p := &Preset{
		Name:        name,
		Description: "all agents and categories on " + ref,
		Agents:      assignAll(state.agents),
		Categories:  assignAll(state.categories),
	}
	if p.EntryCount() == 0 {
		return nil, "", fmt.Errorf("%s has no agent or category entries to assign", lf.Path)
	}
	path, err := m.Save(p, force)
	if err != nil {
		return nil, "", err
	}
	return p, path, nil
}
