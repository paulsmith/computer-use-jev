package transport

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"sync"
)

const loopbackCallbackPath = "/auth/callback"

var loopbackCallbackPorts = [...]int{1455, 1457}

type PKCE struct {
	Verifier  string
	Challenge string
}

func NewPKCE() (PKCE, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return PKCE{}, err
	}
	verifier := base64.RawURLEncoding.EncodeToString(b)
	return PKCE{Verifier: verifier, Challenge: pkceChallenge(verifier)}, nil
}

func NewState() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func pkceChallenge(verifier string) string {
	digest := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(digest[:])
}

type LoopbackCallback struct {
	listener  net.Listener
	server    *http.Server
	state     string
	redirect  string
	result    chan loopbackResult
	closeOnce sync.Once
	sendOnce  sync.Once
	closeErr  error
}

type loopbackResult struct {
	query url.Values
	err   error
}

func ListenLoopback(state string) (*LoopbackCallback, error) {
	if state == "" {
		return nil, errors.New("loopback callback state is empty")
	}
	var lastErr error
	for _, port := range loopbackCallbackPorts {
		listener, err := net.Listen("tcp", net.JoinHostPort("localhost", strconv.Itoa(port)))
		if err != nil {
			lastErr = err
			continue
		}
		callback := &LoopbackCallback{
			listener: listener,
			state:    state,
			redirect: "http://" + net.JoinHostPort("localhost", strconv.Itoa(port)) + loopbackCallbackPath,
			result:   make(chan loopbackResult, 1),
		}
		mux := http.NewServeMux()
		mux.HandleFunc(loopbackCallbackPath, callback.handle)
		callback.server = &http.Server{Handler: mux}
		go func() { _ = callback.server.Serve(listener) }()
		return callback, nil
	}
	return nil, fmt.Errorf("could not listen on localhost ports 1455 or 1457: %w", lastErr)
}

func (c *LoopbackCallback) RedirectURI() string { return c.redirect }

func (c *LoopbackCallback) Wait(ctx context.Context) (url.Values, error) {
	select {
	case result := <-c.result:
		_ = c.Close()
		return result.query, result.err
	case <-ctx.Done():
		_ = c.Close()
		return nil, ctx.Err()
	}
}

func (c *LoopbackCallback) Close() error {
	c.closeOnce.Do(func() {
		var errs []error
		if err := c.server.Shutdown(context.Background()); err != nil {
			errs = append(errs, err)
		}
		if err := c.listener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			errs = append(errs, err)
		}
		c.closeErr = errors.Join(errs...)
	})
	return c.closeErr
}

func (c *LoopbackCallback) handle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := validateCallback(r.URL.Path, r.URL.Query(), c.state); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	query := r.URL.Query()
	if query.Get("error") != "" {
		http.Error(w, "Login was cancelled. You can close this window.", http.StatusBadRequest)
	} else {
		_, _ = fmt.Fprintln(w, "Login complete. You can close this window.")
	}
	c.sendOnce.Do(func() { c.result <- loopbackResult{query: query} })
}

func validateCallback(path string, query url.Values, state string) error {
	if path != loopbackCallbackPath {
		return fmt.Errorf("unexpected callback path %q", path)
	}
	if query.Get("state") != state {
		return errors.New("callback state does not match")
	}
	if query.Get("error") == "" && query.Get("code") == "" {
		return errors.New("callback is missing authorization code")
	}
	return nil
}
