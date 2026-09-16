//go:build darwin

package computeruse

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

// The Swift worker source is embedded and compiled on first use, keeping the
// Go distribution self-contained. The compile cache is keyed by the source
// hash so an upgrade rebuilds it exactly once.
//
//go:embed swift/main.swift
var computerUseSwiftFS embed.FS

// computerUseToolchain reports whether the Swift toolchain needed to build
// the worker is installed. Checking via xcode-select avoids invoking the
// /usr/bin/swiftc stub, which pops an install dialog when the Command Line
// Tools are missing.
var computerUseToolchain = sync.OnceValue(func() bool {
	return exec.Command("xcode-select", "-p").Run() == nil
})

func computerUseSupported() bool { return computerUseToolchain() }

// computerUseWorkerPath returns the compiled worker binary, building it from
// the embedded source when the cache is cold.
func computerUseWorkerPath() (string, error) {
	source, err := computerUseSwiftFS.ReadFile("swift/main.swift")
	if err != nil {
		return "", fmt.Errorf("read embedded Swift worker: %w", err)
	}
	sum := sha256.Sum256(source)
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("locate cache directory: %w", err)
	}
	dir := filepath.Join(cache, "computeruser", "computer-use", hex.EncodeToString(sum[:]))
	binary := filepath.Join(dir, "herbie-computer")
	if info, statErr := os.Stat(binary); statErr == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
		return binary, nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create worker cache directory: %w", err)
	}
	sourcePath := filepath.Join(dir, "main.swift")
	if err := os.WriteFile(sourcePath, source, 0o600); err != nil {
		return "", fmt.Errorf("write worker source: %w", err)
	}
	// A process-unique staging name lets concurrent processes compile without
	// clobbering each other; the final rename is atomic.
	staged := fmt.Sprintf("%s.staging.%d", binary, os.Getpid())
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	compile := exec.CommandContext(ctx, "swiftc", "-O", "-module-name", "herbie_computer", "-o", staged, sourcePath)
	if output, err := compile.CombinedOutput(); err != nil {
		_ = os.Remove(staged)
		if ctx.Err() == context.DeadlineExceeded {
			return "", fmt.Errorf("compiling the computer_use worker timed out")
		}
		return "", fmt.Errorf("compile Swift worker (install Xcode Command Line Tools if this fails):\n%s", tail(output, 2048))
	}
	if err := os.Chmod(staged, 0o755); err != nil {
		_ = os.Remove(staged)
		return "", fmt.Errorf("stage worker binary: %w", err)
	}
	if err := os.Rename(staged, binary); err != nil {
		_ = os.Remove(staged)
		return "", fmt.Errorf("publish worker binary: %w", err)
	}
	return binary, nil
}

func tail(data []byte, limit int) string {
	if len(data) > limit {
		data = data[len(data)-limit:]
	}
	return string(data)
}
