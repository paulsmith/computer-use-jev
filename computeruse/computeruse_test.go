package computeruse

import (
	"bytes"
	"context"
	"encoding/base64"
	"image"
	"image/gif"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// computerUseHelperBinary is a standalone fake worker built from
// testdata/computerusehelper. Like browserhelper it avoids spawning the
// race-instrumented test binary on every worker start.
var computerUseHelperBinary struct {
	once sync.Once
	path string
	err  error
}

func buildComputerUseHelper(t *testing.T) string {
	t.Helper()
	computerUseHelperBinary.once.Do(func() {
		root, err := filepath.Abs("..")
		if err != nil {
			computerUseHelperBinary.err = err
			return
		}
		dir, err := os.MkdirTemp("", "computer-use-jev-helper-*")
		if err != nil {
			computerUseHelperBinary.err = err
			return
		}
		out := filepath.Join(dir, "computerusehelper")
		cmd := exec.Command("go", "build", "-o", out, "./computeruse/testdata/computerusehelper")
		cmd.Dir = root
		if _, err := cmd.CombinedOutput(); err != nil {
			computerUseHelperBinary.err = err
			_ = os.RemoveAll(dir)
			return
		}
		computerUseHelperBinary.path = out
	})
	if computerUseHelperBinary.err != nil {
		t.Fatalf("build computerusehelper: %v", computerUseHelperBinary.err)
	}
	return computerUseHelperBinary.path
}

func newTestComputerUse(t *testing.T, mode string) *ComputerUse {
	t.Helper()
	t.Setenv("COMPUTER_USE_JEV_TEST_MODE", mode)
	return &ComputerUse{resolve: func() (string, error) {
		return buildComputerUseHelper(t), nil
	}}
}

func TestComputerUseRunContextCancellation(t *testing.T) {
	computerUse := newTestComputerUse(t, "ok")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result := computerUse.RunContext(ctx, `{"action":"apps"}`, 0)
	if !strings.Contains(result.Output, context.Canceled.Error()) {
		t.Fatalf("output = %q", result.Output)
	}
}

func TestParseComputerUseArgs(t *testing.T) {
	tests := []struct {
		input string
		want  computerUseArgs
	}{
		{`{"action":"apps"}`, computerUseArgs{action: "apps"}},
		{`{"action":"windows","app":"a1"}`, computerUseArgs{action: "windows", app: "a1"}},
		{`{"action":"activate","app":"a1","window":"w2"}`, computerUseArgs{action: "activate", app: "a1", window: "w2"}},
		{`{"action":"snapshot"}`, computerUseArgs{action: "snapshot"}},
		{`{"action":"snapshot","window":"w1"}`, computerUseArgs{action: "snapshot", window: "w1"}},
		{`{"action":"snapshot","target":"e2"}`, computerUseArgs{action: "snapshot", target: "e2"}},
		{`{"action":"click","target":"e3"}`, computerUseArgs{action: "click", target: "e3"}},
		{`{"action":"fill","target":"e3","text":""}`, computerUseArgs{action: "fill", target: "e3"}},
		{`{"action":"type","target":"e3","text":"hi\n"}`, computerUseArgs{action: "type", target: "e3", text: "hi\n"}},
		{`{"action":"press","app":"a1","key":"cmd+s"}`, computerUseArgs{action: "press", app: "a1", key: "cmd+s"}},
		{`{"action":"screenshot"}`, computerUseArgs{action: "screenshot"}},
	}
	for _, test := range tests {
		got, err := parseComputerUseArgs(test.input)
		if err != nil {
			t.Fatalf("%s: %v", test.input, err)
		}
		if got != test.want {
			t.Errorf("%s = %#v, want %#v", test.input, got, test.want)
		}
	}
	errors := []string{
		`{}`,
		`{"action":"nope"}`,
		`{"action":"apps","app":"a1"}`,
		`{"action":"apps","app":1}`,
		`{"action":"apps","app":"a1","app":"a2"}`,
		`{"action":"windows"}`,
		`{"action":"windows","app":""}`,
		`{"action":"activate"}`,
		`{"action":"click"}`,
		`{"action":"fill","target":"e1"}`,
		`{"action":"fill","text":"x"}`,
		`{"action":"type","target":"","text":"x"}`,
		`{"action":"press","app":"a1"}`,
		`{"action":"press","key":"cmd+s"}`,
		`{"action":"press","app":"a1","key":""}`,
		`{"action":"snapshot","text":"x"}`,
		`{"action":"screenshot","target":"e1"}`,
		`not json`,
	}
	for _, input := range errors {
		if _, err := parseComputerUseArgs(input); err == nil {
			t.Errorf("%s: expected an error", input)
		}
	}
}

func TestComputerUseRunActions(t *testing.T) {
	computerUse := newTestComputerUse(t, "ok")
	tests := []struct {
		action     string
		arguments  string
		want       string
		wantImage  bool
		imageInput int
	}{
		{action: "apps", arguments: `{"action":"apps"}`, want: "a1 TextEdit (com.apple.TextEdit, pid 412)"},
		{action: "windows", arguments: `{"action":"windows","app":"a1"}`, want: `w1 "Untitled" [main]` + "\n" + `w2 "Notes" [focused, minimized]`},
		{action: "activate", arguments: `{"action":"activate","app":"a1","window":"w1"}`, want: `Activated TextEdit (window "Untitled"). Take a snapshot to inspect it.`},
		{action: "snapshot", arguments: `{"action":"snapshot"}`, want: "- AXWindow \"Untitled\" [e1]\n  - AXTextArea [editable, e2]\n\n(2 elements, depth 1)"},
		{action: "click", arguments: `{"action":"click","target":"e1"}`, want: `Pressed AXButton "OK". Take a new snapshot to see the result.`},
		{action: "fill", arguments: `{"action":"fill","target":"e2","text":"x"}`, want: `Sent text to AXButton "OK". Take a new snapshot to see the result.`},
		{action: "type", arguments: `{"action":"type","target":"e2","text":"x"}`, want: `Sent text to AXButton "OK". Take a new snapshot to see the result.`},
		{action: "press", arguments: `{"action":"press","app":"a1","key":"cmd+s"}`, want: "Sent cmd+s. Take a new snapshot to see the result."},
		{action: "screenshot", arguments: `{"action":"screenshot"}`, wantImage: true, imageInput: 1},
	}
	for _, test := range tests {
		result := computerUse.Run(test.arguments, test.imageInput)
		if result.Output == "" || !strings.Contains(result.Output, test.want) {
			t.Errorf("%s output = %q, want %q", test.action, result.Output, test.want)
		}
		if test.wantImage && len(result.Images) != 1 {
			t.Errorf("%s images = %#v", test.action, result.Images)
		}
		if !test.wantImage && len(result.Images) != 0 {
			t.Errorf("%s images = %#v", test.action, result.Images)
		}
	}
	computerUse.Close()
}

func TestComputerUseScreenshotNeedsImageInput(t *testing.T) {
	computerUse := newTestComputerUse(t, "ok")
	result := computerUse.Run(`{"action":"screenshot"}`, 0)
	if len(result.Images) != 0 || !strings.Contains(result.Output, "does not accept image input") {
		t.Fatalf("screenshot without image input = %q, %#v", result.Output, result.Images)
	}
	computerUse.Close()
}

func TestComputerUseWorkerError(t *testing.T) {
	computerUse := newTestComputerUse(t, "fail")
	result := computerUse.Run(`{"action":"click","target":"e1"}`, 0)
	// A worker-reported action error is not a transport failure: the worker
	// must stay alive and the restart note must not appear.
	if !strings.Contains(result.Output, "computer_use error: target is stale") || strings.Contains(result.Output, "restarted") {
		t.Fatalf("error output = %q", result.Output)
	}
	if computerUse.worker == nil {
		t.Fatal("worker dropped after a non-transport error")
	}
	computerUse.Close()
}

func TestComputerUseWorkerRestartAfterCrash(t *testing.T) {
	computerUse := newTestComputerUse(t, "crash")
	first := computerUse.Run(`{"action":"apps"}`, 0)
	if !strings.Contains(first.Output, "computer_use error:") {
		t.Fatalf("crashed worker output = %q", first.Output)
	}
	computerUse.Close()
	t.Setenv("COMPUTER_USE_JEV_TEST_MODE", "ok")
	second := computerUse.Run(`{"action":"apps"}`, 0)
	if !strings.Contains(second.Output, "a1 TextEdit") {
		t.Fatalf("restarted worker output = %q", second.Output)
	}
	computerUse.Close()
}

func TestComputerUseWorkerIdentity(t *testing.T) {
	computerUse := newTestComputerUse(t, "wrongid")
	result := computerUse.Run(`{"action":"apps"}`, 0)
	if !strings.Contains(result.Output, "worker identity") {
		t.Fatalf("identity output = %q", result.Output)
	}
	computerUse.Close()
}

func TestComputerUseImage(t *testing.T) {
	encoded := base64.StdEncoding.EncodeToString(testImage("png", 8, 8))
	image, err := computerUseImage(encoded)
	if err != nil || image.MIME != "image/png" || image.Width != 8 || image.Height != 8 {
		t.Fatalf("image = %#v, %v", image, err)
	}
	for _, bad := range []string{"", "not base64!!", base64.StdEncoding.EncodeToString(testImage("gif", 8, 8))} {
		if _, err := computerUseImage(bad); err == nil {
			t.Errorf("computerUseImage(%q) succeeded", bad)
		}
	}
}

func TestCapLineLengths(t *testing.T) {
	for _, tc := range []struct {
		in   string
		max  int
		want string
	}{
		{"hello\nworld\n", 100, "hello\nworld\n"}, {"", 100, ""},
		{"xxxxxxxxxx\n", 4, "xxxx...[6 bytes elided]\n"},
		{"before\n" + string(makeBytes('y', 2500)) + "\nafter\n", 1000, "before\n" + string(makeBytes('y', 1000)) + "...[1500 bytes elided]\nafter\n"},
		{"zzzzzzzz", 3, "zzz...[5 bytes elided]"},
	} {
		if got := string(capLineLengths([]byte(tc.in), tc.max)); got != tc.want {
			t.Errorf("CapLineLengths(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func makeBytes(c byte, n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = c
	}
	return b
}

func TestOutputCapBytes(t *testing.T) {
	t.Setenv("COMPUTER_USE_JEV_TOOL_OUTPUT_CAP", "")
	if outputCapBytes() != 50*1024 {
		t.Fatal("default")
	}
	t.Setenv("COMPUTER_USE_JEV_TOOL_OUTPUT_CAP", "2m")
	if outputCapBytes() != 2*1024*1024 {
		t.Fatal("m")
	}
	t.Setenv("COMPUTER_USE_JEV_TOOL_OUTPUT_CAP", "bad")
	if outputCapBytes() != 50*1024 {
		t.Fatal("invalid")
	}
}

// testImage encodes a solid image; only PNG is supported by the pruned sniffer.
func testImage(format string, width, height int) []byte {
	var out bytes.Buffer
	m := image.NewRGBA(image.Rect(0, 0, width, height))
	var err error
	switch format {
	case "png":
		err = png.Encode(&out, m)
	case "gif":
		err = gif.Encode(&out, m, nil)
	default:
		panic("unsupported test image format")
	}
	if err != nil {
		panic(err)
	}
	return out.Bytes()
}
