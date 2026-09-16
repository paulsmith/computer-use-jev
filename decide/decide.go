// Package decide drives a macOS Accessibility tool from a natural-language goal
// by asking a TypeSafe System One model (Jev) for one typed decision per step.
//
// The model only ever selects among closed sets that code built from the
// current state — which action, which target token, whether the goal is
// already satisfied. Code owns the loop, the token bookkeeping, and the
// execution; it never lets the model invent an identifier or a free-form
// value such as text to type (those are extracted from the goal).
package decide

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/paulsmith/computeruser/computeruse"
	"github.com/paulsmith/computeruser/typesafe"
)

const (
	defaultMaxSteps            = 16
	defaultConfidenceThreshold = 0.5

	// a noul above this counts as "yes"
	goalSatisfiedThreshold = 0.5

	actionDone = "done"
)

// runner is the slice of *computeruse.ComputerUse the decider needs. It is an
// unexported interface so tests can substitute a fake tool: starting the real
// worker requires macOS Accessibility permission.
type runner interface {
	Run(input string, imageInput int) computeruse.Result
	Close()
}

// the production tool must satisfy the runner the decider asks for
var _ runner = (*computeruse.ComputerUse)(nil)

// Decider runs a goal against a computer-use tool using a TypeSafe client.
type Decider struct {
	client              *typesafe.Client
	tool                runner
	maxSteps            int
	confidenceThreshold float64
	dryRun              bool
	reporter            func(Step)
}

// Option configures a Decider.
type Option func(*Decider)

// WithMaxSteps bounds the number of decision steps.
func WithMaxSteps(n int) Option {
	return func(d *Decider) {
		if n > 0 {
			d.maxSteps = n
		}
	}
}

// WithConfidenceThreshold sets the choice-confidence below which the decider
// escalates instead of acting.
func WithConfidenceThreshold(f float64) Option {
	return func(d *Decider) {
		if f >= 0 && f <= 1 {
			d.confidenceThreshold = f
		}
	}
}

// WithDryRun makes the decider report the decision it would take without
// running the tool.
func WithDryRun(on bool) Option {
	return func(d *Decider) { d.dryRun = on }
}

// WithStepReporter calls fn as each step completes, so a caller can stream the
// trace instead of waiting for Pursue to return.
func WithStepReporter(fn func(Step)) Option {
	return func(d *Decider) { d.reporter = fn }
}

// NewDecider returns a Decider that asks client for each decision and executes
// accepted decisions with tool.
func NewDecider(client *typesafe.Client, tool *computeruse.ComputerUse, opts ...Option) *Decider {
	return newDecider(client, tool, opts...)
}

// newDecider takes the narrower runner interface so tests can drive the loop
// without starting a worker, which needs macOS Accessibility permission.
func newDecider(client *typesafe.Client, tool runner, opts ...Option) *Decider {
	d := &Decider{
		client:              client,
		tool:                tool,
		maxSteps:            defaultMaxSteps,
		confidenceThreshold: defaultConfidenceThreshold,
	}
	for _, opt := range opts {
		opt(d)
	}
	return d
}

// Step is one recorded decision of the loop, including the state it was made
// from and the output of the action it produced.
type Step struct {
	Number     int                        `json:"number"`
	Goal       string                     `json:"goal"`
	State      map[string]any             `json:"state,omitempty"`
	Answers    map[string]typesafe.Answer `json:"answers,omitempty"`
	Action     string                     `json:"action,omitempty"`
	Args       computeruse.Args           `json:"args"`
	Output     string                     `json:"output,omitempty"`
	Images     []computeruse.ItemImage    `json:"images,omitempty"`
	Confidence float64                    `json:"confidence"`
}

// Outcome is the result of pursuing a goal.
type Outcome struct {
	Done  bool
	Steps []Step
	Err   error
}

// Pursue loops until the model reports the goal satisfied, the confidence gate
// trips, the step budget runs out, or an action fails to be constructed.
func (d *Decider) Pursue(ctx context.Context, goal string) Outcome {
	if d.client == nil {
		return Outcome{Err: errors.New("decide: no typesafe client configured")}
	}
	if d.tool == nil {
		return Outcome{Err: errors.New("decide: no computer use tool configured")}
	}

	obs := newTracker()
	var steps []Step

	var lastChoice string
	var lastProbabilities map[string]float64

	for number := 1; number <= d.maxSteps; number++ {
		obs.gather(d)

		state := obs.state(goal)
		questions, hasTargets := buildQuestions(obs)

		resp, err := d.client.Ask(ctx, state, questions)
		if err != nil {
			return Outcome{Steps: steps, Err: fmt.Errorf("decide: step %d: typesafe request failed: %w", number, err)}
		}
		action, ok := resp.Answers["action"]
		if !ok || action.Choice == "" {
			return Outcome{Steps: steps, Err: fmt.Errorf("decide: step %d: typesafe response has no \"action\" answer", number)}
		}
		if !knownAction(action.Choice) {
			return Outcome{Steps: steps, Err: fmt.Errorf("decide: step %d: typesafe chose unknown action %q", number, action.Choice)}
		}

		lastChoice, lastProbabilities = action.Choice, action.Probabilities
		step := Step{
			Number:     number,
			Goal:       goal,
			State:      state,
			Answers:    resp.Answers,
			Action:     action.Choice,
			Confidence: action.Confidence,
		}

		if action.Choice == actionDone || goalSatisfied(resp) {
			step.Output = "goal satisfied; no action taken"
			d.record(&steps, step)
			return Outcome{Done: true, Steps: steps}
		}
		// harmless observations may pass on a weak preference: several
		// acceptable alternatives spread probability without indicating
		// danger, so only state-changing actions need sure footing
		if isMutating(action.Choice) && action.Confidence < d.confidenceThreshold {
			step.Output = "not executed: confidence below threshold"
			d.record(&steps, step)
			return Outcome{Steps: steps, Err: lowConfidenceError(action, d.confidenceThreshold)}
		}

		target := ""
		if answer, ok := resp.Answers["target"]; ok {
			target = answer.Choice
		}
		if target != "" && !obs.has(target) {
			// a token outside the offered set is a hallucination, never a
			// reference: observe instead of acting on it
			step.Action = "snapshot"
			step.Args = computeruse.Args{Action: "snapshot"}
			d.record(&steps, d.execute(obs, step))
			continue
		}
		if actionNeedsTarget(action.Choice) && (!hasTargets || target == "") {
			// no token to act on yet: observe instead of guessing one, then
			// decide again next step (bounded by the step budget)
			step.Action = "snapshot"
			step.Args = computeruse.Args{Action: "snapshot"}
			d.record(&steps, d.execute(obs, step))
			continue
		}

		args, err := actionArgs(action.Choice, target, goal, obs)
		if err != nil {
			step.Output = "decide error: " + err.Error()
			d.record(&steps, step)
			return Outcome{Steps: steps, Err: err}
		}
		step.Args = args

		if d.dryRun {
			// nothing executes, so state cannot change and a second decision
			// would repeat this one verbatim
			step.Output = "dry run: not executed"
			d.record(&steps, step)
			return Outcome{Steps: steps}
		}

		d.record(&steps, d.execute(obs, step))
		// a tool error is information for the next decision, not a reason
		// to stop: the tool's messages say how to recover, and the budget
		// still bounds the loop
	}

	return Outcome{Steps: steps, Err: stepBudgetError(d.maxSteps, lastChoice, lastProbabilities)}
}

// record appends a completed step to the trace and hands it to the reporter.
func (d *Decider) record(steps *[]Step, step Step) {
	*steps = append(*steps, step)
	if d.reporter != nil {
		d.reporter(step)
	}
}

// gather refreshes the state the questions are built from: the running
// applications (always, since tokens must stay current), and the windows of the
// most recently referenced application when one is known. Neither run is part
// of the decision trace: they gather state, they do not act on the goal.
func (t *tracker) gather(d *Decider) {
	if d.dryRun {
		return
	}
	t.refresh(computeruse.Args{Action: "apps"}, d.tool.Run(argsJSON(computeruse.Args{Action: "apps"}), 0).Output)
	if t.activeApp != "" {
		args := computeruse.Args{Action: "windows", App: t.activeApp}
		t.refresh(args, d.tool.Run(argsJSON(args), 0).Output)
	}
}

// execute runs one action with the tool and folds its output into the
// observations. Tool errors arrive as output text, never as a Go error.
// imageInput is 1: a standalone caller has no LLM to gate images on, so
// screenshots are always captured and returned as data.
func (d *Decider) execute(obs *tracker, step Step) Step {
	res := d.tool.Run(argsJSON(step.Args), 1)
	step.Output = res.Output
	step.Images = res.Images
	obs.observe(step.Args, res.Output)
	if isToolError(res.Output) {
		obs.lastError = fmt.Sprintf("%s (step %d)", strings.TrimSpace(res.Output), step.Number)
	} else {
		obs.lastError = ""
	}
	return step
}

// argsJSON renders Args as the tool's JSON input object.
func argsJSON(a computeruse.Args) string {
	fields := map[string]string{}
	for key, value := range map[string]string{
		"action": a.Action,
		"app":    a.App,
		"window": a.Window,
		"target": a.Target,
		"text":   a.Text,
		"key":    a.Key,
	} {
		if value != "" {
			fields[key] = value
		}
	}
	out, err := json.Marshal(fields)
	if err != nil {
		return fmt.Sprintf(`{"action":%q}`, a.Action)
	}
	return string(out)
}

func isToolError(output string) bool {
	return strings.HasPrefix(strings.TrimSpace(output), "computer_use error:")
}

// buildQuestions assembles the one parallel request per step. Question ids are
// for code only, so every instruction carries complete meaning on its own. The
// target question is omitted when no token exists: its option set would be
// empty, and a closed set stays closed.
func buildQuestions(obs *tracker) (map[string]typesafe.Question, bool) {
	questions := map[string]typesafe.Question{
		"action": typesafe.Choice(actionInstructions, actionCriteria),
		"goal_satisfied": typesafe.Noul(
			"Is the goal described by the state field `goal` already fully satisfied by what the current state shows in `applications`, `windows`, and `last_snapshot`, together with the record of `actions_already_taken`? Answer yes only if the goal is already achieved and no further action is needed; an action the goal asks for that already appears in `actions_already_taken` need not be repeated.",
			map[string]string{
				"true":  "the state and the actions already taken show the goal achieved; no further action is needed",
				"false": "at least one more action is required to achieve the goal",
			}),
		"needs_text": typesafe.Noul(
			"Does completing the goal described by the state field `goal` require entering free-form text whose exact characters are not already quoted in the goal? Answer yes when the goal asks to enter, fill, or type content that the goal itself does not spell out in double quotes.",
			map[string]string{
				"true":  "the goal needs text that the goal does not quote, so the user must supply it",
				"false": "the goal needs no unquoted text, or it already quotes the exact text",
			}),
	}

	tokens := obs.tokens()
	if len(tokens) == 0 {
		return questions, false
	}
	criteria := make(map[string]string, len(tokens))
	for _, tok := range tokens {
		criteria[tok] = obs.describe(tok)
	}
	questions["target"] = typesafe.Choice(
		"Which single token from the state field `available_target_tokens` should the next action act on? Choose only a token that appears in `available_target_tokens`; tokens that are not listed do not exist, so do not answer with anything else. Prefer the token whose description names the object the goal refers to. For windows, prefer one flagged [main] or [focused] and avoid one flagged [minimized], which cannot be screenshotted.",
		criteria)
	return questions, true
}

const actionInstructions = `A macOS GUI automation agent must choose its single next computer-use action toward the goal in the state field "goal".
The state field "applications" lists the running applications, "windows" lists the windows of the application used most recently, and "last_snapshot" is the most recent accessibility tree read from a window.
The state field "actions_already_taken" records what the agent already did, and "last_action_error" names the most recent failure: do not repeat an action that failed the same way; follow the error's recovery hint instead.
Choose from "criteria" the one action that makes the best progress toward the goal from exactly this state. Each option's criteria entry says what that action does and what it needs.
Prefer an observing action ("apps", "windows", "snapshot") when the state needed to act is not yet known, and choose "done" only when the state already satisfies the goal or no listed action can advance it.`

var actionCriteria = map[string]string{
	"apps":       "list the running applications, to discover application tokens such as a1",
	"windows":    "list an application's windows, to discover window tokens such as w1; needs a known application",
	"activate":   "bring an application or window to the front so it can receive input; needs an application or window token",
	"snapshot":   "read the accessibility tree of a window as a list of elements with tokens such as e5; the only way to discover element tokens",
	"click":      "click one element of the most recent snapshot; needs an element token",
	"fill":       "replace the value of one text field of the most recent snapshot with text quoted in the goal; needs an element token and quoted text",
	"type":       "send text quoted in the goal to one element of the most recent snapshot; needs an element token and quoted text",
	"press":      "send a keyboard shortcut such as cmd+s to an application; needs an application token and a shortcut named in the goal",
	"screenshot": "capture an image of a window; needs a window token or nothing",
	"done":       "the goal is already satisfied or cannot be advanced; stop the agent",
}

func knownAction(action string) bool {
	_, ok := actionCriteria[action]
	return ok
}

// isMutating reports whether an action can change application state, as
// opposed to merely observing it.
func isMutating(action string) bool {
	switch action {
	case "click", "fill", "type", "press", "activate":
		return true
	}
	return false
}

// actionNeedsTarget reports whether an action cannot run without a token.
func actionNeedsTarget(action string) bool {
	switch action {
	case "windows", "activate", "click", "fill", "type", "press":
		return true
	}
	return false
}

// actionArgs maps the model's typed choice onto a concrete tool call. Text and
// key values come from the goal, never from the model.
func actionArgs(action, target, goal string, obs *tracker) (computeruse.Args, error) {
	switch action {
	case "apps":
		return computeruse.Args{Action: "apps"}, nil
	case "windows":
		return computeruse.Args{Action: "windows", App: appToken(target, obs)}, nil
	case "activate":
		args := computeruse.Args{Action: "activate"}
		if isWindowToken(target) {
			args.Window = target
		} else {
			args.App = appToken(target, obs)
		}
		return args, nil
	case "snapshot":
		return computeruse.Args{Action: "snapshot", Window: windowToken(target)}, nil
	case "screenshot":
		return computeruse.Args{Action: "screenshot", Window: windowToken(target)}, nil
	case "click":
		return computeruse.Args{Action: "click", Target: target}, nil
	case "fill", "type":
		text, ok := quotedSegment(goal)
		if !ok {
			return computeruse.Args{}, fmt.Errorf("action %q needs literal text: put the text in double quotes in the goal, e.g. type \"hello world\" into the search field", action)
		}
		return computeruse.Args{Action: action, Target: target, Text: text}, nil
	case "press":
		key, ok := keyCombo(goal)
		if !ok {
			return computeruse.Args{}, errors.New("action \"press\" needs a key combination; name one in the goal, e.g. cmd+s")
		}
		return computeruse.Args{Action: "press", App: appToken(target, obs), Key: key}, nil
	}
	return computeruse.Args{}, fmt.Errorf("unknown action choice %q", action)
}

// appToken prefers the chosen application token and otherwise falls back to the
// application referenced most recently.
func appToken(target string, obs *tracker) string {
	if strings.HasPrefix(target, "a") {
		return target
	}
	return obs.activeApp
}

func isWindowToken(target string) bool { return strings.HasPrefix(target, "w") }

func windowToken(target string) string {
	if isWindowToken(target) {
		return target
	}
	return ""
}

// goalSatisfied reads the noul that reports the goal already met.
func goalSatisfied(resp typesafe.Response) bool {
	answer, ok := resp.Answers["goal_satisfied"]
	return ok && answer.Noul > goalSatisfiedThreshold
}

func lowConfidenceError(action typesafe.Answer, threshold float64) error {
	return fmt.Errorf(
		"action choice %q has confidence %.2f below the %.2f threshold; the model is not sure enough to act. probabilities: %s",
		action.Choice, action.Confidence, threshold, formatDistribution(action.Probabilities))
}

func stepBudgetError(maxSteps int, lastChoice string, probabilities map[string]float64) error {
	message := fmt.Sprintf("goal not reached within %d steps", maxSteps)
	if lastChoice == "" {
		return errors.New(message)
	}
	return fmt.Errorf("%s; suggested next action: %s. probabilities: %s",
		message, lastChoice, formatDistribution(probabilities))
}

// formatDistribution renders a choice distribution deterministically, highest
// probability first, for humans taking over after an escalation.
func formatDistribution(probabilities map[string]float64) string {
	if len(probabilities) == 0 {
		return "(none)"
	}
	options := make([]string, 0, len(probabilities))
	for option := range probabilities {
		options = append(options, option)
	}
	sort.Slice(options, func(i, j int) bool {
		if probabilities[options[i]] != probabilities[options[j]] {
			return probabilities[options[i]] > probabilities[options[j]]
		}
		return options[i] < options[j]
	})
	parts := make([]string, 0, len(options))
	for _, option := range options {
		parts = append(parts, fmt.Sprintf("%s=%.2f", option, probabilities[option]))
	}
	return strings.Join(parts, " ")
}

// quotedSegment returns the first double-quoted segment of the goal. Literal
// values such as text to type are relayed verbatim from the goal, never
// generated by the model.
func quotedSegment(goal string) (string, bool) {
	m := quotedSegmentRe.FindStringSubmatch(goal)
	if m == nil || m[1] == "" {
		return "", false
	}
	return m[1], true
}

var quotedSegmentRe = regexp.MustCompile(`"([^"]*)"`)

// keyCombo returns a keyboard shortcut named in the goal, lowercased.
func keyCombo(goal string) (string, bool) {
	m := keyComboRe.FindString(goal)
	if m == "" {
		return "", false
	}
	return strings.ToLower(m), true
}

var keyComboRe = regexp.MustCompile(`(?i)\b(?:cmd|command|ctrl|control|opt|option|alt|shift)\+(?:[a-z0-9]+(?:\+[a-z0-9]+)*)\b`)

var (
	appTokenRe     = regexp.MustCompile(`^(a[0-9]+)\b`)
	windowTokenRe  = regexp.MustCompile(`^(w[0-9]+)\b`)
	elementTokenRe = regexp.MustCompile(`\[(e[0-9]+)\]`)
)

// tracker is the best-effort picture of the desktop, rebuilt by parsing the
// tool's rendered output. Nothing here is model-produced. It holds the closed
// sets the target question may offer: applications, the windows of the most
// recently referenced application, and the elements of the last snapshot.
type tracker struct {
	apps      []string
	appDesc   map[string]string
	windows   []string
	winDesc   map[string]string
	winApp    string
	snapshot  string
	elems     []string
	elemDesc  map[string]string
	activeApp string
	lastError string
	// history records the actions already taken, so the model can tell a
	// goal that asks for one screenshot from one that asks for another
	history []string
}

func newTracker() *tracker {
	return &tracker{
		appDesc:  map[string]string{},
		winDesc:  map[string]string{},
		elemDesc: map[string]string{},
	}
}

// observe folds an executed step's output into the observations and records
// the action in the history the model sees. Tool errors carry no tokens, so
// they must not clobber state that is still good.
func (t *tracker) observe(args computeruse.Args, output string) {
	if !isToolError(output) {
		t.history = append(t.history, args.Action)
	}
	t.refresh(args, output)
}

// refresh folds a run's output into the observations without recording it as
// a goal action: gather runs land here, so state stays current while the
// history shows only what the decider chose to do.
func (t *tracker) refresh(args computeruse.Args, output string) {
	if args.App != "" {
		t.activeApp = args.App
	}
	if isToolError(output) {
		return
	}
	switch args.Action {
	case "apps":
		t.observeApps(output)
	case "windows":
		t.observeWindows(output, args.App)
	case "snapshot":
		t.observeSnapshot(output)
	}
}

func (t *tracker) observeApps(output string) {
	apps := make([]string, 0, 8)
	desc := map[string]string{}
	for _, line := range outputLines(output) {
		apps = append(apps, line)
		if m := appTokenRe.FindStringSubmatch(line); m != nil {
			desc[m[1]] = line
		}
	}
	t.apps, t.appDesc = apps, desc
}

func (t *tracker) observeWindows(output, app string) {
	if t.winApp != app {
		// another application's windows: the old tokens no longer exist
		t.windows, t.winDesc = nil, map[string]string{}
		t.winApp = app
	}
	windows := make([]string, 0, 8)
	desc := map[string]string{}
	for _, line := range outputLines(output) {
		windows = append(windows, line)
		if m := windowTokenRe.FindStringSubmatch(line); m != nil {
			desc[m[1]] = line
		}
	}
	t.windows, t.winDesc = windows, desc
}

func (t *tracker) observeSnapshot(output string) {
	t.snapshot = output
	t.elems, t.elemDesc = nil, map[string]string{}
	for _, line := range outputLines(output) {
		for _, m := range elementTokenRe.FindAllStringSubmatch(line, -1) {
			if _, seen := t.elemDesc[m[1]]; seen {
				continue
			}
			t.elems = append(t.elems, m[1])
			t.elemDesc[m[1]] = line
		}
	}
}

// has reports whether token belongs to any currently existing closed set.
func (t *tracker) has(token string) bool {
	if token == "" {
		return false
	}
	if _, ok := t.appDesc[token]; ok {
		return true
	}
	if _, ok := t.winDesc[token]; ok {
		return true
	}
	_, ok := t.elemDesc[token]
	return ok
}

func (t *tracker) describe(token string) string {
	if line, ok := t.appDesc[token]; ok {
		return fmt.Sprintf("application token: %s", line)
	}
	if line, ok := t.winDesc[token]; ok {
		return fmt.Sprintf("window token: %s", line)
	}
	if line, ok := t.elemDesc[token]; ok {
		return fmt.Sprintf("element token: %s", strings.TrimSpace(strings.Replace(line, "["+token+"]", "", 1)))
	}
	return "token " + token
}

// tokens lists the currently existing tokens in a stable order: applications,
// then windows of the last used application, then snapshot elements.
func (t *tracker) tokens() []string {
	out := make([]string, 0, len(t.appDesc)+len(t.winDesc)+len(t.elems))
	for _, line := range t.apps {
		if m := appTokenRe.FindStringSubmatch(line); m != nil {
			out = append(out, m[1])
		}
	}
	for _, line := range t.windows {
		if m := windowTokenRe.FindStringSubmatch(line); m != nil {
			out = append(out, m[1])
		}
	}
	return append(out, t.elems...)
}

// state is the structured value handed to the model each step.
func (t *tracker) state(goal string) map[string]any {
	return map[string]any{
		"goal":                    goal,
		"applications":            orNotGathered(strings.Join(t.apps, "\n")),
		"windows":                 orNotGathered(strings.Join(t.windows, "\n")),
		"last_snapshot":           orNotGathered(t.snapshot),
		"actions_already_taken":   t.history,
		"last_action_error":       orNone(t.lastError),
		"available_target_tokens": t.tokens(),
	}
}

func orNotGathered(s string) string {
	if s == "" {
		return "(not gathered yet)"
	}
	return s
}

func orNone(s string) string {
	if s == "" {
		return "(none)"
	}
	return s
}

func outputLines(output string) []string {
	var out []string
	for _, line := range strings.Split(output, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
	}
	return out
}
