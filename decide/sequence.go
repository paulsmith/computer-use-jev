package decide

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/paulsmith/computeruser/computeruse"
)

// PursueSequence shares the worker session and overall step budget, but gives
// each instruction fresh completion evidence instead of reusing earlier success.
func (d *Decider) PursueSequence(ctx context.Context, original string, instructions []string) Outcome {
	if len(instructions) == 0 {
		return Outcome{Err: errors.New("empty instruction sequence")}
	}
	for _, instruction := range instructions {
		if strings.TrimSpace(instruction) == "" {
			return Outcome{Err: errors.New("empty instruction in sequence")}
		}
	}
	var steps []Step
	var completed []string
	var activeApp, previousResult, scopeTarget string
	for i, goal := range instructions {
		if err := ctx.Err(); err != nil {
			return Outcome{Steps: steps, Err: err}
		}
		remaining := d.maxSteps - len(steps)
		if remaining <= 0 {
			return Outcome{Steps: steps, Err: errors.New("overall goal step budget exhausted before all instructions completed")}
		}
		obs := newTracker()
		obs.originalGoal = original
		obs.completedInstructions = append([]string{}, completed...)
		obs.instruction = i + 1
		obs.stepOffset = len(steps)
		obs.activeApp = activeApp
		obs.previousResult = previousResult
		obs.scopeTarget = scopeTarget
		out := d.pursue(ctx, goal, obs, remaining)
		steps = append(steps, out.Steps...)
		if out.Err != nil {
			return Outcome{Steps: steps, Err: fmt.Errorf("instruction %d (%s): %w", i+1, goal, out.Err)}
		}
		if !out.Done || d.dryRun {
			return Outcome{Steps: steps}
		}
		completed = append(completed, goal)
		activeApp = obs.activeApp
		previousResult = obs.lastResult
		if len(obs.elems) > 0 {
			scopeTarget = obs.elems[0]
		} else {
			scopeTarget = ""
		}
	}
	return Outcome{Done: true, Steps: steps}
}

func (t *tracker) refreshInstruction(d *Decider) error {
	args := computeruse.Args{Action: "snapshot"}
	if t.scopeTarget != "" {
		args.Target = t.scopeTarget
	} else if t.activeApp != "" {
		if _, ok := t.appDesc[t.activeApp]; !ok {
			return errors.New("previous application's token is stale; rediscover the application")
		}
		// Listing windows mints fresh tokens. Prefer the focused/main window, but
		// never change focus: that would destroy a selection between instructions.
		for _, token := range t.tokens() {
			line, ok := t.winDesc[token]
			if !ok || strings.Contains(line, "minimized") {
				continue
			}
			if args.Window == "" {
				args.Window = token
			}
			if strings.Contains(line, "focused") {
				args.Window = token
				break
			}
			if strings.Contains(line, "main") {
				args.Window = token
			}
		}
		if args.Window == "" {
			return errors.New("previous application has no observable window")
		}
	}
	res := d.tool.Run(argsJSON(args), 0)
	if isToolError(res.Output) {
		return fmt.Errorf("refresh instruction state: %s", res.Output)
	}
	t.refresh(args, res.Output)
	return nil
}
