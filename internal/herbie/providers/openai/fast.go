package openai

import (
	"context"
	"fmt"
	"strings"

	"github.com/paulsmith/computer-use-jev/internal/herbie/provider"
	providerinternal "github.com/paulsmith/computer-use-jev/internal/herbie/providers/internal"
)

type OfficialProvider struct{ *Provider }

var priorityRates = map[string]provider.ModelInfo{
	"gpt-5.6-sol":       fastInfo(10, 1, 12.5, 60, provider.CatalogTier{ContextThreshold: 272000, Rates: rates(20, 2, 25, 90)}),
	"gpt-5.6-terra":     fastInfo(4, .4, 5, 24, provider.CatalogTier{ContextThreshold: 272000, Rates: rates(8, .8, 10, 36)}),
	"gpt-5.6-luna":      fastInfo(.4, .04, .5, 2.4, provider.CatalogTier{ContextThreshold: 272000, Rates: rates(.8, .08, 1, 3.6)}),
	"gpt-5.5":           fastInfo(12.5, 1.25, -1, 75),
	"gpt-5.4":           fastInfo(5, .5, -1, 30),
	"gpt-5.4-mini":      fastInfo(1.5, .15, -1, 9),
	"gpt-5.2":           fastInfo(3.5, .35, -1, 28),
	"gpt-5.1":           fastInfo(2.5, .25, -1, 20),
	"gpt-5":             fastInfo(2.5, .25, -1, 20),
	"gpt-5-mini":        fastInfo(.45, .045, -1, 3.6),
	"gpt-4.1":           fastInfo(3.5, .875, -1, 14),
	"gpt-4.1-mini":      fastInfo(.7, .175, -1, 2.8),
	"gpt-4.1-nano":      fastInfo(.2, .05, -1, .8),
	"gpt-4o":            fastInfo(4.25, 2.125, -1, 17),
	"gpt-4o-2024-05-13": fastInfo(8.75, -1, -1, 26.25),
	"gpt-4o-mini":       fastInfo(.25, .125, -1, 1),
	"o3":                fastInfo(3.5, .875, -1, 14),
	"o4-mini":           fastInfo(2, .5, -1, 8),
}

func rates(input, cached, write, output float64) provider.Rates {
	return provider.Rates{CostInput: input, CostOutput: output, CostCacheRead: cached, CostCacheWrite: write, CostCacheWrite1H: -1}
}

func fastInfo(input, cached, write, output float64, tiers ...provider.CatalogTier) provider.ModelInfo {
	out := provider.NewModelInfo()
	out.Rates = rates(input, cached, write, output)
	out.Tiers = tiers
	return out
}

func priorityFamily(model string) string {
	if model == "gpt-4o-2024-05-13" {
		return model
	}
	for family := range priorityRates {
		if model == family || strings.HasPrefix(model, family+"-") && datedSuffix(strings.TrimPrefix(model, family+"-")) {
			return family
		}
	}
	return ""
}

func datedSuffix(s string) bool {
	if len(s) != 10 || s[4] != '-' || s[7] != '-' {
		return false
	}
	for i, c := range s {
		if i == 4 || i == 7 {
			continue
		}
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func PrioritySupported(model string) bool { return priorityFamily(model) != "" }

func PriorityRates(model string, base provider.ModelInfo) provider.ModelInfo {
	info, ok := priorityRates[priorityFamily(model)]
	if !ok {
		return base
	}
	info.ID = base.ID
	info.Context = base.Context
	info.MaxOutput = base.MaxOutput
	info.ImageInput = base.ImageInput
	info.Tools = base.Tools
	info.FastMode = base.FastMode
	info.Efforts = base.Efforts
	return info
}

func (p *OfficialProvider) ValidateFastMode(model string) error {
	if priorityFamily(model) == "" {
		return fmt.Errorf("openai model %q does not support fast mode", model)
	}
	return nil
}

func (*OfficialProvider) FastModeRates(model string, base provider.ModelInfo) provider.ModelInfo {
	return PriorityRates(model, base)
}

func (p *OfficialProvider) Stream(ctx context.Context, c provider.Context, model string, cb provider.StreamCallback, tick provider.TickFunc) error {
	return providerinternal.StreamFast(ctx, p.Provider, p.ValidateFastMode, c, model, cb, tick)
}
