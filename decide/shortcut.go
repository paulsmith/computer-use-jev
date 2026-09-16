package decide

import (
	"errors"
	"fmt"
	"math"

	"github.com/paulsmith/computeruser/typesafe"
)

var shortcutCriteria = map[string]string{
	"none":        "No listed shortcut safely expresses the current instruction, or the intended shortcut is unclear",
	"cmd+a":       "Select all text/content in the focused document or field",
	"cmd+b":       "Toggle bold formatting for the current text selection",
	"cmd+i":       "Toggle italic formatting for the current text selection",
	"cmd+u":       "Toggle underline formatting for the current text selection",
	"cmd+c":       "Copy the current selection to the clipboard",
	"cmd+v":       "Paste the existing clipboard contents at the current selection",
	"cmd+s":       "Save the current document",
	"cmd+z":       "Undo the last edit",
	"cmd+shift+z": "Redo the last undone edit",
}

func shortcutFor(goal string, resp typesafe.Response, threshold float64) (string, error) {
	if key, ok := keyCombo(goal); ok {
		return key, nil
	}
	a, ok := resp.Answers["shortcut"]
	if !ok || a.Type != typesafe.TypeChoice {
		return "", errors.New("press needs an explicit shortcut or a typed shortcut choice")
	}
	if _, ok := shortcutCriteria[a.Choice]; !ok || a.Choice == "none" {
		return "", errors.New("no supported shortcut matches the instruction; name the exact key combination")
	}
	if math.IsNaN(a.Confidence) || a.Confidence < threshold || a.Confidence > 1 {
		return "", fmt.Errorf("shortcut %q confidence %.2f is insufficient", a.Choice, a.Confidence)
	}
	return a.Choice, nil
}
