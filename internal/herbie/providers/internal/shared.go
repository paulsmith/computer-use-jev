package internal

import (
	"context"

	"github.com/paulsmith/computer-use-jev/internal/herbie/provider"
)

func Schema(d provider.ToolDef, requireType bool) map[string]any {
	properties := map[string]any{}
	var required []string
	for _, param := range d.Params {
		property := map[string]any{}
		if requireType || param.Type != "" {
			property["type"] = param.Type
		}
		if param.ItemType != "" {
			property["items"] = map[string]any{"type": param.ItemType}
		}
		if param.Description != "" {
			property["description"] = param.Description
		}
		if param.Minimum != 0 {
			property["minimum"] = param.Minimum
		}
		properties[param.Name] = property
		if param.Required {
			required = append(required, param.Name)
		}
	}
	schema := map[string]any{"type": "object", "properties": properties}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

func Or(a, b string) string {
	if a == "" {
		return b
	}
	return a
}

func StreamFast(ctx context.Context, p provider.Provider, validate func(string) error, c provider.Context, model string, cb provider.StreamCallback, tick provider.TickFunc) error {
	if c.Fast {
		if err := validate(model); err != nil {
			return err
		}
	}
	return p.Stream(ctx, c, model, cb, tick)
}
