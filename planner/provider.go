package planner

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/paulsmith/computer-use-jev/internal/herbie/provider"
	"github.com/paulsmith/computer-use-jev/internal/herbie/providers/anthropic"
	"github.com/paulsmith/computer-use-jev/internal/herbie/providers/openai"
)

func fromEnv() (provider.Provider, string, error) {
	name := strings.ToLower(strings.TrimSpace(os.Getenv("COMPUTER_USE_JEV_PROVIDER")))
	model := strings.TrimSpace(os.Getenv("COMPUTER_USE_JEV_MODEL"))
	if name == "" {
		return nil, "", errors.New("COMPUTER_USE_JEV_PROVIDER is required to split a compound goal")
	}
	if model == "" {
		return nil, "", errors.New("COMPUTER_USE_JEV_MODEL is required to split a compound goal")
	}
	var keyEnv, base string
	wire := openai.Chat
	switch name {
	case "openai":
		keyEnv, base, wire = "OPENAI_API_KEY", "https://api.openai.com/v1", openai.Responses
		if override := strings.TrimSpace(os.Getenv("OPENAI_BASE_URL")); override != "" {
			base = override
		}
	case "anthropic":
		keyEnv, base = "ANTHROPIC_API_KEY", "https://api.anthropic.com/v1"
	case "openrouter":
		keyEnv, base = "OPENROUTER_API_KEY", "https://openrouter.ai/api/v1"
	default:
		return nil, "", fmt.Errorf("unsupported COMPUTER_USE_JEV_PROVIDER %q (use openai, anthropic, or openrouter)", name)
	}
	key := os.Getenv(keyEnv)
	if strings.TrimSpace(key) == "" {
		return nil, "", fmt.Errorf("%s is required for provider %s", keyEnv, name)
	}
	if name == "anthropic" {
		return anthropic.New(anthropic.Options{Name: name, BaseURL: base, APIKey: key, MaxTokens: 4096}), model, nil
	}
	return openai.New(openai.Options{Name: name, BaseURL: base, APIKey: key, Wire: wire, ReasoningFormat: "nested"}), model, nil
}
