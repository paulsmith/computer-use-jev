package computeruse

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

// callTimeout bounds one worker request; the worker bounds its own
// Accessibility operations, so this only guards against a wedged process.
const callTimeout = 2 * time.Minute

// frameMax bounds one worker response line; screenshots are the largest
// payloads.
const frameMax = 8 * 1024 * 1024

const stderrMax = 8 * 1024

// transportError marks a failure that leaves the worker process unhealthy, as
// opposed to an ordinary action error reported by a healthy worker.
type transportError struct{ err error }

func (e transportError) Error() string { return e.err.Error() }
func (e transportError) Unwrap() error { return e.err }

// workerProcess manages the persistent Swift worker subprocess.
type workerProcess struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Scanner
	stderr *tailBuffer
	nextID int
}

// startWorker resolves the worker binary, starts it, and completes the
// initialize handshake.
func startWorker(ctx context.Context, resolve func() (string, error)) (*workerProcess, error) {
	path, err := resolve()
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(path)
	cmd.Args[0] = "herbie-computer"
	cmd.SysProcAttr = newProcessGroup()
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, err
	}
	stderr := &tailBuffer{max: stderrMax}
	cmd.Stderr = stderr
	process := &workerProcess{cmd: cmd, stdin: stdin, stdout: bufio.NewScanner(stdout), stderr: stderr}
	process.stdout.Buffer(make([]byte, frameMax), frameMax)
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return nil, fmt.Errorf("start computer_use worker: %w", err)
	}
	go func() { _ = cmd.Wait() }()
	result, err := process.call(ctx, "initialize", nil)
	if err != nil {
		process.close()
		if tail := strings.TrimSpace(process.stderr.String()); tail != "" {
			err = fmt.Errorf("%w (worker stderr: %s)", err, tail)
		}
		return nil, err
	}
	if jsonStringField(result, "server") != "herbie-computer-use" {
		process.close()
		return nil, fmt.Errorf("unexpected computer_use worker identity %q", jsonStringField(result, "server"))
	}
	return process, nil
}

// call writes one request and reads one response. The caller serializes calls.
func (p *workerProcess) call(ctx context.Context, method string, params map[string]any) (map[string]any, error) {
	p.nextID++
	request := struct {
		ID     int            `json:"id"`
		Method string         `json:"method"`
		Params map[string]any `json:"params,omitempty"`
	}{ID: p.nextID, Method: method, Params: params}
	line, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	if _, err := p.stdin.Write(append(line, '\n')); err != nil {
		return nil, transportError{fmt.Errorf("write to worker: %w", err)}
	}
	type readResult struct {
		text string
		err  error
	}
	read := make(chan readResult, 1)
	go func() {
		if p.stdout.Scan() {
			read <- readResult{text: p.stdout.Text()}
		} else {
			read <- readResult{err: p.stdout.Err()}
		}
	}()
	select {
	case result := <-read:
		if result.err != nil {
			return nil, transportError{fmt.Errorf("read from worker: %w", result.err)}
		}
		// The worker never writes empty lines, so a clean EOF here means the
		// process exited before responding.
		if result.text == "" {
			return nil, transportError{fmt.Errorf("worker exited before responding")}
		}
		return p.decode(result.text)
	case <-ctx.Done():
		return nil, transportError{ctx.Err()}
	case <-time.After(callTimeout):
		return nil, transportError{fmt.Errorf("worker did not respond within %s", formatDuration(callTimeout))}
	}
}

// decode validates one response frame.
func (p *workerProcess) decode(line string) (map[string]any, error) {
	var dynamic struct {
		ID     int            `json:"id"`
		OK     bool           `json:"ok"`
		Result map[string]any `json:"result"`
		Error  *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	decoder := json.NewDecoder(strings.NewReader(line))
	decoder.UseNumber()
	if err := decoder.Decode(&dynamic); err != nil {
		return nil, transportError{fmt.Errorf("malformed worker response: %w", err)}
	}
	if decoder.More() {
		return nil, transportError{fmt.Errorf("malformed worker response: trailing data")}
	}
	if dynamic.ID != p.nextID {
		return nil, transportError{fmt.Errorf("worker response id %d does not match request id %d", dynamic.ID, p.nextID)}
	}
	if dynamic.Error != nil {
		return nil, fmt.Errorf("%s", dynamic.Error.Message)
	}
	if !dynamic.OK {
		return nil, transportError{fmt.Errorf("worker reported failure without an error")}
	}
	return dynamic.Result, nil
}

// close terminates the worker process group.
func (p *workerProcess) close() {
	_ = p.stdin.Close()
	if p.cmd.Process != nil {
		_ = signalProcessGroup(p.cmd.Process.Pid, syscall.SIGKILL)
	}
}

type tailBuffer struct {
	sync.Mutex
	max  int
	data []byte
}

func (b *tailBuffer) Write(data []byte) (int, error) {
	b.Lock()
	defer b.Unlock()
	original := len(data)
	if len(data) >= b.max {
		b.data = append(b.data[:0], data[len(data)-b.max:]...)
		return original, nil
	}
	if extra := len(b.data) + len(data) - b.max; extra > 0 {
		copy(b.data, b.data[extra:])
		b.data = b.data[:len(b.data)-extra]
	}
	b.data = append(b.data, data...)
	return original, nil
}

func (b *tailBuffer) String() string {
	b.Lock()
	defer b.Unlock()
	return string(b.data)
}

func formatDuration(d time.Duration) string {
	seconds := int64((d + 500*time.Millisecond) / time.Second)
	if seconds < 60 {
		return itoa(int(seconds)) + "s"
	}
	if seconds < 3600 && seconds%60 == 0 {
		return itoa(int(seconds/60)) + "m"
	}
	if seconds < 3600 {
		return itoa(int(seconds/60)) + "m " + itoa(int(seconds%60)) + "s"
	}
	if seconds%3600 == 0 {
		return itoa(int(seconds/3600)) + "h"
	}
	return itoa(int(seconds/3600)) + "h " + itoa(int(seconds%3600/60)) + "m"
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
