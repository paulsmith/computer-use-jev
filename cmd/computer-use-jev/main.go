// Command computer-use-jev runs the macOS accessibility tool directly, or drives
// it toward a natural-language goal with a TypeSafe System One model (Jev)
// deciding one bounded step at a time.
package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/paulsmith/computer-use-jev/computeruse"
	"github.com/paulsmith/computer-use-jev/decide"
	"github.com/paulsmith/computer-use-jev/planner"
	"github.com/paulsmith/computer-use-jev/typesafe"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "computer-use-jev: "+err.Error())
		os.Exit(1)
	}
}

func run() error {
	fs := flag.NewFlagSet("computer-use-jev", flag.ContinueOnError)
	goal := fs.String("goal", "", "natural-language goal for the decider to pursue")
	maxSteps := fs.Int("max-steps", 0, "total decision-step budget across all instructions (default 16)")
	dryRun := fs.Bool("dry-run", false, "classify and print the instruction plan without desktop access")
	asJSON := fs.Bool("json", false, "write an ndjson trace of steps to stdout")
	key := fs.String("key", "", "TypeSafe API key (defaults to TYPESAFE_API_KEY)")
	if err := fs.Parse(os.Args[1:]); err != nil {
		return err
	}

	tool := computeruse.New()
	defer tool.Close()

	if *goal == "" {
		return passthrough(tool, fs.Args(), *asJSON)
	}

	client, err := typesafeClient(*key)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	instructions, err := planner.Plan(ctx, client, *goal)
	if err != nil {
		return err
	}
	if err := printPlan(*goal, instructions, *asJSON); err != nil {
		return err
	}
	if *dryRun {
		return nil
	}
	lastInstruction := 0

	opts := []decide.Option{
		decide.WithStepReporter(func(step decide.Step) {
			if !*asJSON && step.Instruction != lastInstruction {
				fmt.Printf("instruction %d/%d: %s\n", step.Instruction, len(instructions), step.Goal)
				lastInstruction = step.Instruction
			}
			for i, image := range step.Images {
				if path, err := saveImage(step.Number, i, image); err == nil {
					fmt.Fprintf(os.Stderr, "screenshot saved: %s\n", path)
				} else {
					fmt.Fprintf(os.Stderr, "screenshot not saved: %v\n", err)
				}
			}
			if *asJSON {
				// best effort: a step that cannot be encoded is not worth
				// aborting a run that may already have changed the desktop
				_ = writeJSON(step)
				return
			}
			// the decision comes first, so a long window listing cannot hide
			// it from someone watching the run
			fmt.Printf("step %d: %s\n", step.Number, toolInput(step.Args))
			if step.Output != "" {
				fmt.Println(step.Output)
			}
		}),
	}
	if *maxSteps > 0 {
		opts = append(opts, decide.WithMaxSteps(*maxSteps))
	}

	out := decide.NewDecider(client, tool, opts...).PursueSequence(ctx, *goal, instructions)
	if out.Err != nil {
		return out.Err
	}
	if !out.Done {
		return errors.New("goal not reached")
	}
	return nil
}

func typesafeClient(key string) (*typesafe.Client, error) {
	if key != "" {
		return typesafe.New(key), nil
	}
	return typesafe.FromEnv()
}

// passthrough runs one tool call built from the positional arguments, without
// involving the model: `computer-use-jev apps`, `computer-use-jev windows a1`, or a
// verbatim JSON input object such as '{"action":"apps"}'.
func passthrough(tool *computeruse.ComputerUse, args []string, asJSON bool) error {
	if len(args) == 0 {
		return errors.New(`nothing to do: pass a goal with -goal, or a tool call such as 'computer-use-jev apps'`)
	}

	input := strings.Join(args, " ")
	if !strings.HasPrefix(strings.TrimSpace(input), "{") {
		if len(args) > 2 {
			return fmt.Errorf("too many arguments for action %q; expected at most one more (an app, window, or element token)", args[0])
		}
		fields := map[string]string{"action": args[0]}
		if len(args) == 2 {
			fields[tokenField(args[1])] = args[1]
		}
		encoded, err := json.Marshal(fields)
		if err != nil {
			return err
		}
		input = string(encoded)
	}

	result := tool.Run(input, 0)
	if asJSON {
		if err := writeJSON(map[string]any{
			"input":  json.RawMessage(input),
			"output": result.Output,
		}); err != nil {
			return err
		}
	} else {
		fmt.Println(result.Output)
	}
	if detail, ok := strings.CutPrefix(result.Output, "computer_use error: "); ok {
		return errors.New(detail)
	}
	return nil
}

// tokenField maps a rendered token to the argument it belongs in. Tokens are
// namespaced by the tool's own rendering: a1 apps, w1 windows, e1 elements.
func tokenField(token string) string {
	switch {
	case strings.HasPrefix(token, "a"):
		return "app"
	case strings.HasPrefix(token, "w"):
		return "window"
	default:
		return "target"
	}
}

// toolInput renders args the way they are handed to the tool, so a trace line
// can be replayed by copying it.
func toolInput(args computeruse.Args) string {
	fields := map[string]string{}
	for name, value := range map[string]string{
		"action": args.Action,
		"app":    args.App,
		"window": args.Window,
		"target": args.Target,
		"text":   args.Text,
		"key":    args.Key,
	} {
		if value != "" {
			fields[name] = value
		}
	}
	if len(fields) == 0 {
		return "(no action taken)"
	}
	encoded, err := json.Marshal(fields)
	if err != nil {
		return args.Action
	}
	return string(encoded)
}

// saveImage writes one captured screenshot to a timestamped file and returns
// its path. Images ride inside the step even in JSON mode only as metadata;
// the bytes belong on disk where a human can open them.
func saveImage(step, index int, image computeruse.ItemImage) (string, error) {
	data, err := base64.StdEncoding.DecodeString(image.DataB64)
	if err != nil {
		return "", err
	}
	if image.MIME != "" && image.MIME != "image/png" {
		return "", fmt.Errorf("unsupported image type %q", image.MIME)
	}
	name := fmt.Sprintf("screenshot-%02d-%d-%s.png", step, index, time.Now().Format("20060102-150405"))
	path := filepath.Join(os.TempDir(), name)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", err
	}
	return path, nil
}

func writeJSON(v any) error {
	return json.NewEncoder(os.Stdout).Encode(v)
}

func printPlan(goal string, instructions []string, asJSON bool) error {
	if asJSON {
		return writeJSON(struct {
			Type         string   `json:"type"`
			Goal         string   `json:"goal"`
			Instructions []string `json:"instructions"`
		}{"plan", goal, instructions})
	}
	if len(instructions) > 1 {
		fmt.Println("Plan:")
		for i, instruction := range instructions {
			fmt.Printf("  %d. %s\n", i+1, instruction)
		}
	}
	return nil
}
