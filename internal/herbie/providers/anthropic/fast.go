package anthropic

import (
	"context"
	"fmt"
	"strings"

	"github.com/paulsmith/computer-use-jev/internal/herbie/provider"
	providerinternal "github.com/paulsmith/computer-use-jev/internal/herbie/providers/internal"
)

type OfficialProvider struct{ *Provider }

func supportsFastMode(model string) bool {
	for _, family := range []string{"claude-opus-5", "claude-opus-4-8"} {
		if model == family || strings.HasPrefix(model, family+"-") {
			return true
		}
	}
	return false
}

func (p *OfficialProvider) ValidateFastMode(model string) error {
	if !supportsFastMode(model) {
		return fmt.Errorf("anthropic model %q does not support fast mode", model)
	}
	return nil
}

func (*OfficialProvider) FastModeRates(_ string, base provider.ModelInfo) provider.ModelInfo {
	scale := func(r *provider.Rates) {
		for _, value := range []*float64{&r.CostInput, &r.CostOutput, &r.CostCacheRead, &r.CostCacheWrite, &r.CostCacheWrite1H} {
			if *value >= 0 {
				*value *= 2
			}
		}
	}
	scale(&base.Rates)
	for i := range base.Tiers {
		scale(&base.Tiers[i].Rates)
	}
	return base
}

func (p *OfficialProvider) Stream(ctx context.Context, c provider.Context, model string, cb provider.StreamCallback, tick provider.TickFunc) error {
	return providerinternal.StreamFast(ctx, p.Provider, p.ValidateFastMode, c, model, cb, tick)
}
