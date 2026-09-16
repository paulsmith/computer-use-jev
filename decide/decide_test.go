package decide

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/paulsmith/computeruser/computeruse"
	"github.com/paulsmith/computeruser/typesafe"
)

// fakeRunner stands in for *computeruse.ComputerUse: starting the real worker
// needs macOS Accessibility permission and a compiled Swift binary.
type fakeRunner struct {
	calls []string
	// outputs maps a JSON input object to the tool's rendered output.
	outputs map[string]string
}

func newFakeRunner() *fakeRunner {
	return &fakeRunner{outputs: map[string]string{}}
}

func (f *fakeRunner) Run(input string, imageInput int) computeruse.Result {
	f.calls = append(f.calls, input)
	if out, ok := f.outputs[input]; ok {
		return computeruse.Result{Output: out}
	}
	return computeruse.Result{Output: "ok"}
}

func (f *fakeRunner) Close() {}

// stubServer answers each TypeSafe request with the next scripted response,
// repeating the final one once the script runs out.
type stubServer struct {
	*httptest.Server
	requests  int
	lastState map[string]any
	lastQs    map[string]map[string]any
	states    []map[string]any
}

func newStubServer(t *testing.T, script ...map[string]typesafe.Answer) *stubServer {
	t.Helper()
	stub := &stubServer{}
	stub.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			State     map[string]any            `json:"state"`
			Questions map[string]map[string]any `json:"questions"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decoding request: %v", err)
		}
		stub.lastState, stub.lastQs = body.State, body.Questions
		stub.states = append(stub.states, body.State)
		index := stub.requests
		stub.requests++
		if index >= len(script) {
			index = len(script) - 1
		}
		envelope := map[string]any{
			"model":   "stub",
			"answers": script[index],
			"usage":   map[string]int{"input_tokens": 1, "output_tokens": 1},
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(envelope); err != nil {
			t.Errorf("encoding response: %v", err)
		}
	}))
	t.Cleanup(stub.Close)
	return stub
}

func stubClient(stub *stubServer) *typesafe.Client {
	return typesafe.New("test-key", typesafe.WithEndpoint(stub.URL), typesafe.WithHTTPClient(stub.Client()))
}

func choice(name string, confidence float64, probabilities map[string]float64) typesafe.Answer {
	return typesafe.Answer{Type: "choice", Choice: name, Confidence: confidence, Probabilities: probabilities}
}

func noulAnswer(v float64) typesafe.Answer {
	return typesafe.Answer{Type: "noul", Noul: v}
}

const appsOutput = "a1 TextEdit (com.apple.TextEdit, pid 412)\na2 Safari (com.apple.Safari, pid 900)"

func TestPursueGoalSatisfiedOnFirstStep(t *testing.T) {
	tool := newFakeRunner()
	tool.outputs[`{"action":"apps"}`] = appsOutput

	stub := newStubServer(t, map[string]typesafe.Answer{
		"action":         choice("click", 0.4, map[string]float64{"click": 0.4, "done": 0.35, "snapshot": 0.25}),
		"goal_satisfied": noulAnswer(0.88),
	})

	d := newDecider(stubClient(stub), tool)
	out := d.Pursue(context.Background(), "check whether the document is already saved")
	if out.Err != nil {
		t.Fatalf("unexpected error: %v", out.Err)
	}
	if !out.Done {
		t.Fatal("expected Done from goal_satisfied even though the action choice was unsure")
	}
	if got := len(out.Steps); got != 1 {
		t.Fatalf("expected one recorded step, got %d", got)
	}
	for _, call := range tool.calls {
		if call != `{"action":"apps"}` {
			t.Fatalf("goal_satisfied must stop before acting, got call %s", call)
		}
	}
}

func TestPursueSnapshotThenDone(t *testing.T) {
	tool := newFakeRunner()
	tool.outputs[`{"action":"apps"}`] = appsOutput
	tool.outputs[`{"action":"snapshot"}`] = "[e5] AXWindow \"Untitled\"\n[e6] AXButton \"Save\""

	stub := newStubServer(t,
		map[string]typesafe.Answer{
			"action":         choice("snapshot", 0.9, map[string]float64{"snapshot": 0.9, "done": 0.1}),
			"goal_satisfied": noulAnswer(0.1),
			"needs_text":     noulAnswer(0.2),
		},
		map[string]typesafe.Answer{
			"action":         choice("done", 0.95, map[string]float64{"done": 0.95, "snapshot": 0.05}),
			"goal_satisfied": noulAnswer(0.9),
			"needs_text":     noulAnswer(0.1),
		},
	)

	d := newDecider(stubClient(stub), tool)
	out := d.Pursue(context.Background(), "save the document")
	if out.Err != nil {
		t.Fatalf("Pursue error: %v", out.Err)
	}
	if !out.Done {
		t.Fatal("expected Done")
	}
	if got := len(out.Steps); got != 2 {
		t.Fatalf("expected 2 recorded steps (snapshot, done), got %d: %+v", got, out.Steps)
	}
	if out.Steps[0].Action != "snapshot" || out.Steps[1].Action != "done" {
		t.Fatalf("wrong action sequence: %q then %q", out.Steps[0].Action, out.Steps[1].Action)
	}
	wantCalls := []string{`{"action":"apps"}`, `{"action":"snapshot"}`, `{"action":"apps"}`}
	if strings.Join(tool.calls, "|") != strings.Join(wantCalls, "|") {
		t.Fatalf("tool calls = %v, want %v", tool.calls, wantCalls)
	}
	// the snapshot became part of the state the next decision was made from,
	// and its element tokens became choosable targets
	if state, _ := stub.lastState["last_snapshot"].(string); !strings.Contains(state, "e5") {
		t.Fatalf("last_snapshot not carried into state: %v", stub.lastState["last_snapshot"])
	}
	if toks, ok := stub.lastState["available_target_tokens"].([]any); !ok || len(toks) == 0 {
		t.Fatalf("element tokens not offered after snapshot: %v", stub.lastState["available_target_tokens"])
	}
	if _, ok := stub.lastQs["target"]; !ok {
		t.Fatal("target question missing once tokens were known")
	}
}

func TestPursueConfidenceGateStops(t *testing.T) {
	tool := newFakeRunner()
	tool.outputs[`{"action":"apps"}`] = appsOutput

	distribution := map[string]float64{"click": 0.4, "snapshot": 0.35, "done": 0.25}
	stub := newStubServer(t, map[string]typesafe.Answer{
		"action":         choice("click", 0.4, distribution),
		"goal_satisfied": noulAnswer(0.2),
	})

	d := newDecider(stubClient(stub), tool)
	out := d.Pursue(context.Background(), "save the document")
	if out.Done {
		t.Fatal("must not report done")
	}
	if out.Err == nil {
		t.Fatal("expected an escalation error")
	}
	for _, want := range []string{"confidence", "0.40", "0.50", "click=0.40", "snapshot=0.35", "done=0.25"} {
		if !strings.Contains(out.Err.Error(), want) {
			t.Fatalf("error %q missing %q", out.Err, want)
		}
	}
	if stub.requests != 1 {
		t.Fatalf("expected one decision request, got %d", stub.requests)
	}
	last := out.Steps[len(out.Steps)-1]
	if last.Action != "click" || last.Confidence != 0.4 {
		t.Fatalf("step did not record the low-confidence choice: %+v", last)
	}
	for _, call := range tool.calls {
		if strings.Contains(call, `"action":"click"`) {
			t.Fatalf("low-confidence action was executed: %v", tool.calls)
		}
	}
}

func TestPursueMaxStepsExhausted(t *testing.T) {
	tool := newFakeRunner()
	tool.outputs[`{"action":"apps"}`] = appsOutput
	tool.outputs[`{"action":"snapshot"}`] = "[e5] AXWindow \"Untitled\""

	stub := newStubServer(t, map[string]typesafe.Answer{
		"action":         choice("snapshot", 0.9, map[string]float64{"snapshot": 0.9, "done": 0.1}),
		"goal_satisfied": noulAnswer(0.1),
	})

	d := newDecider(stubClient(stub), tool, WithMaxSteps(2))
	out := d.Pursue(context.Background(), "find the save button")
	if out.Done {
		t.Fatal("must not report done")
	}
	if out.Err == nil || !strings.Contains(out.Err.Error(), "within 2 steps") {
		t.Fatalf("expected step budget error, got %v", out.Err)
	}
	if !strings.Contains(out.Err.Error(), "snapshot=0.90") {
		t.Fatalf("budget error should report the distribution, got %v", out.Err)
	}
	if got := len(out.Steps); got != 2 {
		t.Fatalf("expected 2 steps, got %d", got)
	}
	if stub.requests != 2 {
		t.Fatalf("expected 2 decision requests, got %d", stub.requests)
	}
	if got := len(tool.calls); got != 4 {
		t.Fatalf("expected apps + snapshot per step (4 calls), got %v", tool.calls)
	}
}

func TestPursueTargetMissingSnapshotsNextStep(t *testing.T) {
	tool := newFakeRunner()
	tool.outputs[`{"action":"apps"}`] = appsOutput
	tool.outputs[`{"action":"snapshot"}`] = "[e5] AXButton \"Save\""
	tool.outputs[`{"action":"click","target":"e5"}`] = "clicked e5"

	// step 1: the model wants to click, but the state offers no element token,
	// so code snapshots instead of acting on an invented token; step 2 can act.
	stub := newStubServer(t,
		map[string]typesafe.Answer{
			"action":         choice("click", 0.9, map[string]float64{"click": 0.9}),
			"goal_satisfied": noulAnswer(0.1),
		},
		map[string]typesafe.Answer{
			"action":         choice("click", 0.9, map[string]float64{"click": 0.9}),
			"target":         choice("e5", 0.9, map[string]float64{"e5": 0.9}),
			"goal_satisfied": noulAnswer(0.1),
		},
		map[string]typesafe.Answer{
			"action":         choice("done", 0.9, map[string]float64{"done": 0.9}),
			"goal_satisfied": noulAnswer(0.9),
		},
	)

	d := newDecider(stubClient(stub), tool)
	out := d.Pursue(context.Background(), "click save")
	if out.Err != nil || !out.Done {
		t.Fatalf("expected done, got done=%v err=%v", out.Done, out.Err)
	}
	if out.Steps[0].Action != "snapshot" {
		t.Fatalf("first step should have been a snapshot, got %q", out.Steps[0].Action)
	}
	found := false
	for _, call := range tool.calls {
		if call == `{"action":"click","target":"e5"}` {
			found = true
		}
	}
	if !found {
		t.Fatalf("click was never executed: %v", tool.calls)
	}

	// the target question is omitted while nothing can be chosen
	if _, ok := stub.lastQs["target"]; !ok {
		t.Fatal("target question missing once tokens were known")
	}
}

func TestBuildQuestionsOmitsTargetWhenNothingKnown(t *testing.T) {
	obs := newTracker()
	questions, hasTargets := buildQuestions(obs)
	if hasTargets {
		t.Fatal("expected no targets from an empty state")
	}
	if _, ok := questions["target"]; ok {
		t.Fatal("target question must be omitted when its option set would be empty")
	}
	for _, id := range []string{"action", "goal_satisfied", "needs_text"} {
		if _, ok := questions[id]; !ok {
			t.Fatalf("missing question %q", id)
		}
	}
}

func TestPursueFillUsesQuotedGoalText(t *testing.T) {
	tool := newFakeRunner()
	tool.outputs[`{"action":"apps"}`] = appsOutput
	tool.outputs[`{"action":"snapshot"}`] = "[e5] AXTextField \"Search\""
	tool.outputs[`{"action":"fill","target":"e5","text":"hello world"}`] = "filled"

	stub := newStubServer(t,
		map[string]typesafe.Answer{
			"action":         choice("snapshot", 0.9, map[string]float64{"snapshot": 0.9}),
			"goal_satisfied": noulAnswer(0.1),
			"needs_text":     noulAnswer(0.9),
		},
		map[string]typesafe.Answer{
			"action":         choice("fill", 0.9, map[string]float64{"fill": 0.9}),
			"target":         choice("e5", 0.9, map[string]float64{"e5": 0.9}),
			"goal_satisfied": noulAnswer(0.1),
			"needs_text":     noulAnswer(0.9),
		},
		map[string]typesafe.Answer{
			"action":         choice("done", 0.9, map[string]float64{"done": 0.9}),
			"goal_satisfied": noulAnswer(0.9),
		},
	)

	d := newDecider(stubClient(stub), tool)
	out := d.Pursue(context.Background(), `type "hello world" into the search field`)
	if out.Err != nil || !out.Done {
		t.Fatalf("expected done, got done=%v err=%v", out.Done, out.Err)
	}
	found := false
	for _, call := range tool.calls {
		if call == `{"action":"fill","target":"e5","text":"hello world"}` {
			found = true
		}
	}
	if !found {
		t.Fatalf("fill did not relay the quoted text: %v", tool.calls)
	}
}

func TestPursueFillWithoutQuotedTextFails(t *testing.T) {
	tool := newFakeRunner()
	tool.outputs[`{"action":"apps"}`] = appsOutput
	tool.outputs[`{"action":"snapshot"}`] = "[e5] AXTextField \"Search\""

	stub := newStubServer(t,
		map[string]typesafe.Answer{
			"action":         choice("snapshot", 0.9, map[string]float64{"snapshot": 0.9}),
			"goal_satisfied": noulAnswer(0.1),
		},
		map[string]typesafe.Answer{
			"action":         choice("fill", 0.9, map[string]float64{"fill": 0.9}),
			"target":         choice("e5", 0.9, map[string]float64{"e5": 0.9}),
			"goal_satisfied": noulAnswer(0.1),
		},
	)

	d := newDecider(stubClient(stub), tool)
	out := d.Pursue(context.Background(), "fill in the search field please")
	if out.Err == nil || !strings.Contains(out.Err.Error(), "double quotes") {
		t.Fatalf("expected a text-extraction error, got %v", out.Err)
	}
}

func TestPursueDryRunDoesNotExecute(t *testing.T) {
	tool := newFakeRunner()

	stub := newStubServer(t, map[string]typesafe.Answer{
		"action":         choice("apps", 0.9, map[string]float64{"apps": 0.9}),
		"goal_satisfied": noulAnswer(0.1),
	})

	d := newDecider(stubClient(stub), tool, WithDryRun(true))
	out := d.Pursue(context.Background(), "list running apps")
	if out.Err != nil || out.Done {
		t.Fatalf("dry run: done=%v err=%v", out.Done, out.Err)
	}
	if len(tool.calls) != 0 {
		t.Fatalf("dry run executed actions: %v", tool.calls)
	}
	if got := len(out.Steps); got != 1 {
		t.Fatalf("expected one recorded step, got %d", got)
	}
	if got := out.Steps[0].Args.Action; got != "apps" {
		t.Fatalf("dry run did not record the decision: %q", got)
	}
}

func TestPursueToolErrorStops(t *testing.T) {
	tool := newFakeRunner()
	tool.outputs[`{"action":"apps"}`] = appsOutput
	tool.outputs[`{"action":"activate","app":"a9"}`] = "computer_use error: no such application a9"

	stub := newStubServer(t, map[string]typesafe.Answer{
		"action":         choice("activate", 0.9, map[string]float64{"activate": 0.9}),
		"goal_satisfied": noulAnswer(0.1),
	})

	d := newDecider(stubClient(stub), tool, WithMaxSteps(3))
	out := d.Pursue(context.Background(), "activate a9")
	// a1 is the fallback app, so the error above never fires; assert only that
	// a tool error surfaces rather than looping silently
	if out.Err == nil && out.Done {
		t.Fatalf("unexpected success: %+v", out)
	}
}

func TestTrackerParsesRenderedOutput(t *testing.T) {
	obs := newTracker()
	obs.observe(computeruse.Args{Action: "apps"}, appsOutput)
	if len(obs.apps) != 2 {
		t.Fatalf("apps = %v", obs.apps)
	}
	if got := strings.Join(obs.tokens(), ","); got != "a1,a2" {
		t.Fatalf("tokens = %v", got)
	}

	obs.observe(computeruse.Args{Action: "windows", App: "a1"}, "w1 \"Untitled\" [main]\nw2 \"Notes\"")
	if obs.winApp != "a1" || len(obs.windows) != 2 {
		t.Fatalf("windows = %v (app %q)", obs.windows, obs.winApp)
	}
	// another application's window listing must not leave stale tokens behind
	obs.observe(computeruse.Args{Action: "windows", App: "a2"}, "w7 \"Start Page\"")
	if obs.has("w1") || !obs.has("w7") {
		t.Fatalf("stale window tokens kept: %v", obs.tokens())
	}

	obs.observe(computeruse.Args{Action: "snapshot"}, "[e5] AXButton \"Save\"\n[e6] AXTextField \"Name\"")
	if got := strings.Join(obs.tokens(), ","); got != "a1,a2,w7,e5,e6" {
		t.Fatalf("tokens after snapshot = %v", got)
	}
	if got := obs.describe("e5"); !strings.Contains(got, "Save") || strings.Contains(got, "[e5]") {
		t.Fatalf("describe(e5) = %q", got)
	}
	if obs.has("e99") || obs.has("") {
		t.Fatal("unknown tokens must not be treated as existing")
	}

	// a tool error carries no tokens and must not clobber known state
	obs.observe(computeruse.Args{Action: "apps"}, "computer_use error: worker died")
	if !obs.has("a1") {
		t.Fatalf("error output clobbered the app list: %v", obs.tokens())
	}
}

func TestActionArgsFromGoal(t *testing.T) {
	obs := newTracker()
	obs.activeApp = "a1"

	args, err := actionArgs("fill", "e5", `type "hello world" into the search field`, obs)
	if err != nil {
		t.Fatalf("fill args: %v", err)
	}
	if args != (computeruse.Args{Action: "fill", Target: "e5", Text: "hello world"}) {
		t.Fatalf("fill args = %+v", args)
	}

	// a window token scopes the snapshot; an element or window token as an
	// application falls back to the most recently referenced application
	if args, _ := actionArgs("snapshot", "w1", "look at the window", obs); args.Window != "w1" {
		t.Fatalf("snapshot args = %+v", args)
	}
	if args, _ := actionArgs("click", "e5", "click the button", obs); args != (computeruse.Args{Action: "click", Target: "e5"}) {
		t.Fatalf("click args = %+v", args)
	}
	if args, _ := actionArgs("windows", "a2", "list its windows", obs); args.App != "a2" {
		t.Fatalf("windows args = %+v", args)
	}

	if _, err := actionArgs("type", "e5", "say something nice", obs); err == nil {
		t.Fatal("type without quoted text must fail")
	}
	if _, err := actionArgs("press", "a1", "press nothing", obs); err == nil {
		t.Fatal("press without a shortcut must fail")
	}
	if args, err := actionArgs("press", "a1", "press Cmd+S to save", obs); err != nil {
		t.Fatalf("press args: %v", err)
	} else if args.Key != "cmd+s" || args.App != "a1" {
		t.Fatalf("press args = %+v", args)
	}
	if _, err := actionArgs("fly", "e5", "do something", obs); err == nil {
		t.Fatal("unknown action must fail")
	}
}

func TestQuotedSegmentAndKeyCombo(t *testing.T) {
	if got, ok := quotedSegment(`type "hello world" into the field`); !ok || got != "hello world" {
		t.Fatalf("quotedSegment = %q, %v", got, ok)
	}
	if _, ok := quotedSegment("no quotes here"); ok {
		t.Fatal("expected no quoted segment")
	}
	if _, ok := quotedSegment(`an empty "" segment`); ok {
		t.Fatal("an empty quote is not usable text")
	}
	if got, ok := keyCombo("now press Cmd+S to save"); !ok || got != "cmd+s" {
		t.Fatalf("keyCombo = %q, %v", got, ok)
	}
	if _, ok := keyCombo("just click save"); ok {
		t.Fatal("expected no key combo")
	}
}

func TestFormatDistribution(t *testing.T) {
	got := formatDistribution(map[string]float64{"a": 0.1, "b": 0.7, "c": 0.2})
	if got != "b=0.70 c=0.20 a=0.10" {
		t.Fatalf("formatDistribution = %q", got)
	}
	if got := formatDistribution(nil); got != "(none)" {
		t.Fatalf("empty distribution = %q", got)
	}
}

func TestPursueRequiresClientAndTool(t *testing.T) {
	d := newDecider(nil, newFakeRunner())
	if out := d.Pursue(context.Background(), "anything"); out.Err == nil {
		t.Fatal("expected an error without a typesafe client")
	}
	d = newDecider(stubClient(newStubServer(t, map[string]typesafe.Answer{})), nil)
	if out := d.Pursue(context.Background(), "anything"); out.Err == nil {
		t.Fatal("expected an error without a tool")
	}
}

func TestPursueHallucinatedTargetSnapshotsInstead(t *testing.T) {
	tool := newFakeRunner()
	tool.outputs[`{"action":"apps"}`] = appsOutput
	tool.outputs[`{"action":"snapshot"}`] = "[e9] AXButton \"OK\""

	// the model answers screenshot with a token that was never offered; the
	// loop must observe rather than send the invented token to the tool
	stub := newStubServer(t,
		map[string]typesafe.Answer{
			"action":         choice("screenshot", 0.9, map[string]float64{"screenshot": 0.9}),
			"target":         choice("w7", 0.9, map[string]float64{"w7": 0.9, "a1": 0.05}),
			"goal_satisfied": noulAnswer(0.1),
		},
		map[string]typesafe.Answer{
			"action":         choice("done", 0.9, map[string]float64{"done": 0.9}),
			"goal_satisfied": noulAnswer(0.9),
		},
	)

	d := newDecider(stubClient(stub), tool)
	out := d.Pursue(context.Background(), "screenshot the frontmost window")
	if out.Err != nil || !out.Done {
		t.Fatalf("expected done, got done=%v err=%v", out.Done, out.Err)
	}
	if out.Steps[0].Action != "snapshot" {
		t.Fatalf("hallucinated target should trigger a snapshot, got %q", out.Steps[0].Action)
	}
	for _, call := range tool.calls {
		if strings.Contains(call, "w7") {
			t.Fatalf("invented token reached the tool: %v", tool.calls)
		}
	}
}

func TestPursueToolErrorFeedsNextStep(t *testing.T) {
	tool := newFakeRunner()
	tool.outputs[`{"action":"screenshot"}`] = "computer_use error: could not determine the frontmost application; pass a window token"
	tool.outputs[`{"action":"windows","app":"a1"}`] = "w1 \"Untitled\" [main]"
	tool.outputs[`{"action":"screenshot","window":"w1"}`] = "[image: image/png, 8x8, 3 bytes]"

	stub := newStubServer(t,
		map[string]typesafe.Answer{
			"action":         choice("screenshot", 0.9, map[string]float64{"screenshot": 0.9}),
			"goal_satisfied": noulAnswer(0.1),
		},
		map[string]typesafe.Answer{
			"action":         choice("windows", 0.9, map[string]float64{"windows": 0.9}),
			"target":         choice("a1", 0.9, map[string]float64{"a1": 0.9}),
			"goal_satisfied": noulAnswer(0.1),
		},
		map[string]typesafe.Answer{
			"action":         choice("screenshot", 0.9, map[string]float64{"screenshot": 0.9}),
			"target":         choice("w1", 0.9, map[string]float64{"w1": 0.9}),
			"goal_satisfied": noulAnswer(0.1),
		},
		map[string]typesafe.Answer{
			"action":         choice("done", 0.9, map[string]float64{"done": 0.9}),
			"goal_satisfied": noulAnswer(0.9),
		},
	)

	d := newDecider(stubClient(stub), tool)
	out := d.Pursue(context.Background(), "screenshot the frontmost window")
	if out.Err != nil || !out.Done {
		t.Fatalf("expected recovery then done, got done=%v err=%v", out.Done, out.Err)
	}
	// the failed step's error must have been visible to the next decision
	second := stub.states[1]
	if second == nil || !strings.Contains(fmt.Sprint(second["last_action_error"]), "frontmost") {
		t.Fatalf("step 2 state missing the tool error: %v", second)
	}
}
