package config

import (
	"fmt"
	"slices"
	"strings"

	"github.com/paulsmith/computer-use-jev/internal/herbie/text"
)

type presetConfig struct {
	Name, Provider, Model, Effort, Description string
	EffortSet                                  bool
	Fast                                       bool
	FastSet                                    bool
}

type Selection struct {
	Provider, Model, Effort string
	EffortSet               bool
	Fast                    bool
	FastSet                 bool
}

func validPresetName(name string) bool {
	return text.IsIdentName(name, '.', '-', '_') && text.IsIdentName(name[:1])
}

func validatePreset(name string, p map[string]any) (presetConfig, error) {
	if !validPresetName(name) {
		return presetConfig{}, fmt.Errorf("%q can't be a preset name", name)
	}
	allowed := map[string]struct{}{"description": {}, "tint": {}, "provider": {}, "model": {}, "effort": {}, "fast": {}, "system_prompt": {}, "system_prompt_append": {}}
	for k, v := range p {
		if _, ok := allowed[k]; !ok {
			return presetConfig{}, fmt.Errorf("preset %q: %q is not presettable", name, k)
		}
		if _, ok := v.(string); !ok {
			return presetConfig{}, fmt.Errorf("preset %q: %q must be a scalar", name, k)
		}
	}
	provider, _ := p["provider"].(string)
	if provider == "" {
		return presetConfig{}, fmt.Errorf("preset %q must name a provider", name)
	}
	if tint, _ := p["tint"].(string); tint != "" && !ValueValid(settings["tint"], tint) {
		return presetConfig{}, fmt.Errorf("preset %q: unknown tint %q", name, tint)
	}
	description, _ := p["description"].(string)
	model, _ := p["model"].(string)
	effort, effortSet := p["effort"].(string)
	fastValue, fastSet := p["fast"].(string)
	fast, valid := parseBool(fastValue)
	if fastSet && !valid {
		return presetConfig{}, fmt.Errorf("preset %q: invalid fast value %q", name, fastValue)
	}
	return presetConfig{Name: name, Provider: provider, Model: model, Effort: effort, EffortSet: effortSet, Fast: fast, FastSet: fastSet, Description: description}, nil
}

func (c *Config) SavePreset(name, provider, model, effort string, fast bool, tint string) error {
	if !validPresetName(name) || provider == "" {
		return fmt.Errorf("preset name and provider are required")
	}
	if tint != "" && !ValueValid(settings["tint"], tint) {
		return fmt.Errorf("unknown tint %q", tint)
	}
	if _, ok := valueObject(c.state, "presets", name); ok {
		return fmt.Errorf("preset %q is defined in state.json", name)
	}
	n := clone(c.file)
	root, exists := n["presets"]
	if exists {
		var ok bool
		root, ok = root.(map[string]any)
		if !ok {
			return fmt.Errorf("presets must be an object")
		}
	} else {
		root = map[string]any{}
		n["presets"] = root
	}
	presets := root.(map[string]any)
	p := map[string]any{"provider": provider, "fast": "off"}
	if fast {
		p["fast"] = "on"
	}
	if model != "" {
		p["model"] = model
	}
	if effort != "" {
		p["effort"] = effort
	}
	if tint != "" {
		p["tint"] = tint
	}
	if old, ok := presets[name].(map[string]any); ok {
		if description, ok := old["description"].(string); ok {
			p["description"] = description
		}
	}
	presets[name] = p
	nextOrder := cloneOrder(c.fileOrder)
	if nextOrder == nil {
		nextOrder = map[string][]string{}
	}
	completeObjectOrder(n, "", nextOrder)
	if err := atomicJSONWithOrder(configPath("config.json"), n, nextOrder); err != nil {
		return err
	}
	c.file = n
	c.fileOrder = nextOrder
	return nil
}

func (c *Config) PresetTint(name string) string {
	p, ok := c.preset(name)
	if !ok {
		return ""
	}
	tint, _ := p["tint"].(string)
	return tint
}

func (c *Config) Presets() []presetConfig {
	seen := map[string]struct{}{}
	var out []presetConfig
	for _, tier := range []map[string]any{c.state, c.file} {
		for name, value := range tier {
			if after, ok := strings.CutPrefix(name, "presets."); ok {
				name = after
				if _, dup := seen[name]; dup {
					continue
				}
				if p, ok := value.(map[string]any); ok {
					if preset, err := validatePreset(name, p); err == nil {
						out = append(out, preset)
						seen[name] = struct{}{}
					}
				}
			}
		}
		root, _ := tier["presets"].(map[string]any)
		for name, value := range root {
			if _, dup := seen[name]; dup {
				continue
			}
			if p, ok := value.(map[string]any); ok {
				if preset, err := validatePreset(name, p); err == nil {
					out = append(out, preset)
					seen[name] = struct{}{}
				}
			}
		}
	}
	slices.SortFunc(out, func(a, b presetConfig) int { return strings.Compare(a.Name, b.Name) })
	return out
}
