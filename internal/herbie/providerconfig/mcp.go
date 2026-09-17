package providerconfig

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"sort"

	"github.com/paulsmith/computer-use-jev/internal/herbie/config"
)

// MCPServer is one resolved mcp.<name> block. Error is set when the block is
// configured but unusable; the tool reports it instead of calling the server.
type MCPServer struct {
	Name          string
	URL           string
	Headers       http.Header
	SessionHeader string
	Error         string
}

var mcpNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

// mcpReservedHeaders are owned by the transport and dropped from extra_headers.
var mcpReservedHeaders = []string{"Content-Type", "Accept", "Mcp-Session-Id", "Mcp-Protocol-Version", "Content-Length", "Host"}

// MCPServers resolves every mcp.<name> block, usable or not, sorted by name.
func MCPServers(c *config.Config) []MCPServer {
	if c == nil {
		return nil
	}
	names := c.ObjectKeys("mcp")
	sort.Strings(names)
	var out []MCPServer
	for _, name := range names {
		if name == "timeout" || name == "timeout_max" {
			continue
		}
		out = append(out, resolveMCPServer(c, name))
	}
	return out
}

func resolveMCPServer(c *config.Config, name string) MCPServer {
	prefix := "mcp." + name + "."
	server := MCPServer{Name: name, URL: c.AnyString(prefix + "url")}
	if !mcpNamePattern.MatchString(name) {
		server.Error = fmt.Sprintf("mcp server name %q must match %s", name, mcpNamePattern)
		return server
	}
	if err := validateMCPURL(server.URL); err != nil {
		server.Error = fmt.Sprintf("mcp.%s.url: %v", name, err)
		return server
	}
	credential, err := mcpCredential(c, prefix, name)
	if err != nil {
		server.Error = err.Error()
		return server
	}
	server.Headers = make(http.Header)
	for header, values := range ExtraHeaders(c, prefix) {
		canonical := http.CanonicalHeaderKey(header)
		if slices.Contains(mcpReservedHeaders, canonical) || (canonical == "Authorization" && credential != "") {
			warn("mcp.%s.extra_headers: %s is set by herbie; ignoring it", name, header)
			continue
		}
		for _, v := range values {
			server.Headers.Add(header, v)
		}
	}
	if credential != "" {
		server.Headers.Set("Authorization", "Bearer "+credential)
	}
	if session := c.AnyString(prefix + "session_header"); session != "" {
		canonical := http.CanonicalHeaderKey(session)
		if slices.Contains(mcpReservedHeaders, canonical) || canonical == "Authorization" || !validHeaderName(session) {
			server.Error = fmt.Sprintf("mcp.%s.session_header: %q is not a usable header name", name, session)
			return server
		}
		server.SessionHeader = session
	}
	return server
}

// mcpCredential applies the precedence api_key/api_key_env, then provider:, then none.
// A credential that is named but resolves empty is an error; none at all is fine.
func mcpCredential(c *config.Config, prefix, name string) (string, error) {
	_, apiKeySet := c.AnySet(prefix + "api_key")
	_, apiKeyEnvSet := c.AnySet(prefix + "api_key_env")
	if apiKeySet || apiKeyEnvSet {
		if key := APIKey(c, prefix, ""); key != "" {
			return key, nil
		}
		return "", fmt.Errorf("mcp.%s: credential is configured but resolves to an empty value", name)
	}
	if provider := c.AnyString(prefix + "provider"); provider != "" {
		if !slices.Contains(c.ProviderNames(), provider) {
			return "", fmt.Errorf("mcp.%s.provider: no providers.%s block", name, provider)
		}
		if key := APIKey(c, "providers."+provider+".", ""); key != "" {
			return key, nil
		}
		return "", fmt.Errorf("mcp.%s: credential from providers.%s resolves to an empty value", name, provider)
	}
	return "", nil
}

func validateMCPURL(raw string) error {
	if raw == "" {
		return fmt.Errorf("url is required")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return fmt.Errorf("url %q is not an absolute URL", raw)
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		host := u.Hostname()
		if host == "localhost" {
			return nil
		}
		if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
			return nil
		}
		return fmt.Errorf("url must use https unless the host is loopback")
	}
	return fmt.Errorf("url scheme %q must be https", u.Scheme)
}
