// Command computerusehelper is a fake computer_use worker used by the
// computeruse package tests. It speaks the worker NDJSON protocol; responses
// are driven entirely by the COMPUTERUSER_TEST_MODE environment variable:
//
//   - ok:    replies with fixed successful results per method
//   - fail:  replies with a fixed error for every method after initialize
//   - crash: exits after the initialize handshake
package main

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"os"
)

func main() {
	mode := os.Getenv("COMPUTERUSER_TEST_MODE")
	reader := bufio.NewReader(os.Stdin)
	writer := bufio.NewWriter(os.Stdout)
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	respond := func(response any) {
		data, err := json.Marshal(response)
		if err != nil {
			fmt.Fprintln(os.Stderr, "marshal:", err)
			os.Exit(1)
		}
		writer.Write(data)
		writer.WriteByte('\n')
		writer.Flush()
	}
	for scanner.Scan() {
		var request struct {
			ID     int            `json:"id"`
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &request); err != nil {
			respond(map[string]any{"id": 0, "ok": false, "error": map[string]any{"code": "invalid_json", "message": "bad request"}})
			continue
		}
		switch request.Method {
		case "initialize":
			server := "herbie-computer-use"
			if mode == "wrongid" {
				server = "something-else"
			}
			respond(map[string]any{"id": request.ID, "ok": true, "result": map[string]any{"server": server}})
			continue
		}
		if mode == "crash" {
			os.Exit(3)
		}
		if mode == "fail" {
			respond(map[string]any{"id": request.ID, "ok": false, "error": map[string]any{"code": "stale_target", "message": "target is stale"}})
			continue
		}
		result := map[string]any{}
		switch request.Method {
		case "apps":
			result["apps"] = []any{
				map[string]any{"token": "a1", "pid": json.Number("412"), "bundle_id": "com.apple.TextEdit", "name": "TextEdit"},
			}
		case "windows":
			result["windows"] = []any{
				map[string]any{"token": "w1", "title": "Untitled", "main": true, "focused": false, "minimized": false},
				map[string]any{"token": "w2", "title": "Notes", "main": false, "focused": true, "minimized": true},
			}
		case "activate":
			result["app"] = "TextEdit"
			result["window_title"] = "Untitled"
		case "snapshot":
			result["text"] = "- AXWindow \"Untitled\" [e1]\n  - AXTextArea [editable, e2]"
			result["elements"] = json.Number("2")
			result["depth"] = json.Number("1")
			result["truncated"] = request.Params["target"] == "e2"
		case "click", "fill", "type":
			result["role"] = "AXButton"
			result["title"] = "OK"
		case "press":
			result["key"] = "cmd+s"
		case "screenshot":
			result["png_base64"] = base64.StdEncoding.EncodeToString(screenshotPNG())
			result["width"] = json.Number("8")
			result["height"] = json.Number("8")
		default:
			respond(map[string]any{"id": request.ID, "ok": false, "error": map[string]any{"code": "unknown_method", "message": "unknown method"}})
			continue
		}
		respond(map[string]any{"id": request.ID, "ok": true, "result": result})
	}
}

// screenshotPNG encodes a valid 8x8 PNG.
func screenshotPNG() []byte {
	var out bytes.Buffer
	if err := png.Encode(&out, image.NewRGBA(image.Rect(0, 0, 8, 8))); err != nil {
		fmt.Fprintln(os.Stderr, "encode png:", err)
		os.Exit(1)
	}
	return out.Bytes()
}
