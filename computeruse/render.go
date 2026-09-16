package computeruse

import (
	"encoding/json"
	"fmt"
	"strings"
)

// render turns a worker result into model-facing output.
func (c *ComputerUse) render(args computerUseArgs, result map[string]any) Result {
	switch args.action {
	case "apps":
		var lines []string
		for _, raw := range resultList(result, "apps") {
			app, _ := raw.(map[string]any)
			lines = append(lines, fmt.Sprintf("%s %s (%s, pid %d)", jsonStringField(app, "token"), jsonStringField(app, "name"), jsonStringField(app, "bundle_id"), jsonNumberField(app, "pid")))
		}
		if len(lines) == 0 {
			return Result{Output: "No running GUI applications."}
		}
		return Result{Output: strings.Join(lines, "\n")}
	case "windows":
		var lines []string
		for _, raw := range resultList(result, "windows") {
			window, _ := raw.(map[string]any)
			line := fmt.Sprintf("%s %q", jsonStringField(window, "token"), jsonStringField(window, "title"))
			var flags []string
			for _, name := range []string{"main", "focused", "minimized"} {
				if jsonBoolField(window, name) {
					flags = append(flags, name)
				}
			}
			if len(flags) != 0 {
				line += " [" + strings.Join(flags, ", ") + "]"
			}
			lines = append(lines, line)
		}
		if len(lines) == 0 {
			return Result{Output: "The application has no windows."}
		}
		return Result{Output: strings.Join(lines, "\n")}
	case "activate":
		return Result{Output: fmt.Sprintf("Activated %s (window %q). Take a snapshot to inspect it.", jsonStringField(result, "app"), jsonStringField(result, "window_title"))}
	case "snapshot":
		output := jsonStringField(result, "text")
		if output == "" {
			return Result{Output: "The snapshot is empty."}
		}
		footer := fmt.Sprintf("(%d elements, depth %d", jsonNumberField(result, "elements"), jsonNumberField(result, "depth"))
		if jsonBoolField(result, "truncated") {
			footer += ", truncated: use snapshot with a target to inspect a smaller subtree"
		}
		footer += ")"
		return Result{Output: capToolText(output+"\n\n"+footer, 0, "use snapshot with a target to inspect a smaller subtree")}
	case "click":
		return Result{Output: fmt.Sprintf("Pressed %s %q. Take a new snapshot to see the result.", jsonStringField(result, "role"), jsonStringField(result, "title"))}
	case "fill", "type":
		return Result{Output: fmt.Sprintf("Sent text to %s %q. Take a new snapshot to see the result.", jsonStringField(result, "role"), jsonStringField(result, "title"))}
	case "press":
		return Result{Output: fmt.Sprintf("Sent %s. Take a new snapshot to see the result.", jsonStringField(result, "key"))}
	case "screenshot":
		image, err := computerUseImage(jsonStringField(result, "png_base64"))
		if err != nil {
			return Result{Output: "computer_use error: " + err.Error()}
		}
		return Result{Output: ImagePlaceholder(image), Images: []ItemImage{image}}
	default:
		return Result{Output: "computer_use error: unknown action " + args.action}
	}
}

func resultList(result map[string]any, name string) []any {
	list, _ := result[name].([]any)
	return list
}

func jsonStringField(object map[string]any, name string) string {
	value, _ := object[name].(string)
	return value
}

func jsonNumberField(object map[string]any, name string) int {
	value, _ := object[name].(json.Number)
	number, _ := value.Int64()
	return int(number)
}

func jsonBoolField(object map[string]any, name string) bool {
	value, _ := object[name].(bool)
	return value
}
