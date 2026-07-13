package preset

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/tailscale/hujson"
)

// Severity of a validation issue.
type Severity int

const (
	SeverityWarn Severity = iota
	SeverityFail
)

// Issue is one validation finding about a preset against the profile's
// opencode.json (unknown model refs, unreachable local providers, ...).
type Issue struct {
	Severity Severity
	Message  string
}

// HasFailures reports whether any issue is SeverityFail.
func HasFailures(issues []Issue) bool {
	for _, issue := range issues {
		if issue.Severity == SeverityFail {
			return true
		}
	}
	return false
}

// providerConfig is the subset of an opencode.json provider block that
// validation needs.
type providerConfig struct {
	NPM     string `json:"npm"`
	Options struct {
		BaseURL string `json:"baseURL"`
	} `json:"options"`
	Models map[string]json.RawMessage `json:"models"`
}

// opencodeConfig is the subset of opencode.json validation needs.
type opencodeConfig struct {
	Provider map[string]providerConfig `json:"provider"`
}

// opencodeCandidates in load-priority order (.jsonc preferred).
var opencodeCandidates = []string{"opencode.jsonc", "opencode.json"}

// loadOpencodeConfig reads the profile's opencode.json[c]. Returns
// (nil, "", nil) when no config file exists.
func (m *Manager) loadOpencodeConfig() (*opencodeConfig, string, error) {
	for _, name := range opencodeCandidates {
		path := filepath.Join(m.opencodeDir, name)
		data, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, "", fmt.Errorf("read %s: %w", path, err)
		}
		var cfg opencodeConfig
		if err := jsonUnmarshalLoose(data, &cfg); err != nil {
			return nil, "", fmt.Errorf("parse %s: %w", path, err)
		}
		return &cfg, path, nil
	}
	return nil, "", nil
}

func jsonUnmarshalLoose(data []byte, v any) error {
	std, err := hujson.Standardize(data)
	if err != nil {
		return err
	}
	return json.Unmarshal(std, v)
}

// ModelRefs returns every provider/model reference an entry pins: the
// primary model plus any fallback_models given as strings or as objects
// with a "model" key.
func (e Entry) ModelRefs() []string {
	var refs []string
	if model := e.Model(); model != "" {
		refs = append(refs, model)
	}
	raw, ok := e["fallback_models"]
	if !ok {
		return refs
	}

	appendRef := func(data json.RawMessage) {
		var s string
		if err := json.Unmarshal(data, &s); err == nil {
			refs = append(refs, s)
			return
		}
		var obj struct {
			Model string `json:"model"`
		}
		if err := json.Unmarshal(data, &obj); err == nil && obj.Model != "" {
			refs = append(refs, obj.Model)
		}
	}

	var list []json.RawMessage
	if err := json.Unmarshal(raw, &list); err == nil {
		for _, item := range list {
			appendRef(item)
		}
		return refs
	}
	appendRef(raw) // fallback_models may also be a single string
	return refs
}

// modelRefs returns the sorted unique provider/model refs across all of a
// preset's entries and its opencode block.
func (p *Preset) modelRefs() []string {
	seen := map[string]bool{}
	for _, section := range []map[string]Entry{p.Agents, p.Categories} {
		for _, entry := range section {
			for _, ref := range entry.ModelRefs() {
				seen[ref] = true
			}
		}
	}
	for _, raw := range p.Opencode {
		var s string
		if json.Unmarshal(raw, &s) == nil && s != "" {
			seen[s] = true
		}
	}
	refs := make([]string, 0, len(seen))
	for ref := range seen {
		refs = append(refs, ref)
	}
	sort.Strings(refs)
	return refs
}

// splitRef splits "provider/model" at the first slash; model IDs may
// themselves contain slashes (e.g. openrouter paths).
func splitRef(ref string) (provider, model string) {
	if idx := strings.Index(ref, "/"); idx > 0 {
		return ref[:idx], ref[idx+1:]
	}
	return ref, ""
}

// ValidateRefs checks every model reference in the resolved preset against
// the profile's opencode.json provider block:
//
//   - provider declared with a non-empty models map, model absent → Fail
//     (this is the class of error oh-my-openagent only surfaces at runtime)
//   - provider not declared at all → Warn (OpenCode knows many providers
//     natively via auth, so this is suspicious but not definitely wrong)
//   - provider declared with no models map → OK (models are discovered)
//   - no opencode.json[c] present → single Warn, validation skipped
func (m *Manager) ValidateRefs(p *Preset) ([]Issue, error) {
	cfg, path, err := m.loadOpencodeConfig()
	if err != nil {
		return nil, err
	}
	if cfg == nil {
		return []Issue{{
			Severity: SeverityWarn,
			Message:  "no opencode.json[c] in " + m.opencodeDir + " — skipped model reference validation",
		}}, nil
	}

	var issues []Issue
	for _, ref := range p.modelRefs() {
		provider, model := splitRef(ref)
		pc, declared := cfg.Provider[provider]
		if !declared {
			issues = append(issues, Issue{
				Severity: SeverityWarn,
				Message: fmt.Sprintf("%s: provider %q is not declared in %s — OpenCode must know it natively",
					ref, provider, filepath.Base(path)),
			})
			continue
		}
		if len(pc.Models) > 0 {
			if _, ok := pc.Models[model]; !ok {
				issues = append(issues, Issue{
					Severity: SeverityFail,
					Message: fmt.Sprintf("%s: model %q is not declared under provider %q in %s",
						ref, model, provider, filepath.Base(path)),
				})
			}
		}
	}
	return issues, nil
}

// probeTimeout bounds each endpoint probe. Local servers answer (or refuse)
// nearly instantly; anything slower is effectively down.
const probeTimeout = 2 * time.Second

// probeClient is swappable in tests.
var probeClient = &http.Client{Timeout: probeTimeout}

// isLoopbackURL reports whether the URL's host resolves to a loopback
// address (localhost, 127.x, ::1). Only these are probed: they are the
// local-LLM servers that are commonly down, and probing remote providers
// on every apply would be slow and noisy.
func isLoopbackURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return false
	}
	host := u.Hostname()
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// ProbeEndpoints checks that every loopback-hosted provider the preset
// references is answering. Any HTTP response (including 401/404) counts as
// alive; only connection failures are issues. Non-loopback providers are
// skipped.
func (m *Manager) ProbeEndpoints(p *Preset) ([]Issue, error) {
	cfg, _, err := m.loadOpencodeConfig()
	if err != nil {
		return nil, err
	}
	if cfg == nil {
		return nil, nil
	}

	probed := map[string]bool{}
	var issues []Issue
	for _, ref := range p.modelRefs() {
		provider, _ := splitRef(ref)
		if probed[provider] {
			continue
		}
		probed[provider] = true

		pc, declared := cfg.Provider[provider]
		if !declared || pc.Options.BaseURL == "" || !isLoopbackURL(pc.Options.BaseURL) {
			continue
		}
		probeURL := strings.TrimSuffix(pc.Options.BaseURL, "/") + "/models"
		resp, err := probeClient.Get(probeURL)
		if err != nil {
			issues = append(issues, Issue{
				Severity: SeverityFail,
				Message: fmt.Sprintf("provider %q (%s) is not answering — is the local server running?",
					provider, pc.Options.BaseURL),
			})
			continue
		}
		_ = resp.Body.Close()
	}
	return issues, nil
}
