# computeruser — design

Extract Herbie's `computer_use` tool into a standalone Go repository and add
**typesafe** support: **Jev** (TypeSafe's System One decision model) as the
*decision maker* that drives the macOS Accessibility worker. Instead of an
LLM generating free-form tool calls, Jev returns typed judgments — which
action to take next, which target to act on, whether the goal is done — and
ordinary Go code executes them against the worker.

## Why Jev fits

The computer-use loop is a decision loop, not a generation loop. Each step
the state is known (apps, windows, last snapshot), the options are closed
sets (9 actions; a handful of named tokens), and what's needed is one narrow,
typed selection with a probability and a confidence. That's exactly what
System One models do: `code owns the workflow; the model supplies programmable
common sense`. Confidence gates escalation (see the TypeSafe skill:
confidence "tells you whether to act").

## Repository layout

```
computeruser/
├── go.mod                        module github.com/paulsmith/computeruser, go 1.26
├── LICENSE                       MIT (copy from herbie)
├── README.md
├── Makefile
├── DESIGN.md                     (this file)
├── computeruse/                  extracted tool, macOS-only pieces guarded
│   ├── computeruse.go            tool entry: parse args, run worker call, render
│   ├── worker.go                 NDJSON worker process mgmt (start/call/close)
│   ├── worker_darwin.go          embedded Swift source, compile-on-demand cache
│   ├── worker_other.go           !darwin stub
│   ├── swift/main.swift          the Accessibility worker (verbatim from herbie)
│   ├── render.go                 model-facing output rendering + screenshot image
│   ├── image.go                  PNG sniff/validate helpers (from tools/image_sniff.go, pruned)
│   ├── args.go                   JSON arg parsing helpers (parseObject, jsonString, …)
│   ├── cap.go                    output cap/line-wrap helpers (self-contained)
│   ├── spawn.go                  process-group helpers (from system/, darwin+linux)
│   ├── computeruse_test.go       tests, ported
│   └── testdata/computerusehelper/  fake worker for tests
├── typesafe/
│   └── client.go                 minimal TypeSafe System One client (HTTP, Jev)
├── decide/
│   ├── decide.go                 Jev decision loop: goal → action plan → execute
│   └── decide_test.go
└── cmd/computeruser/main.go      CLI driver
```

## Package boundaries

### computeruse (extracted from `~/projects/herbie/tools`)

Nearly verbatim extraction of:

- `tools/computer_use.go` → `computeruse.go` (split for clarity, same logic)
- `tools/computer_use_darwin.go` → `worker_darwin.go`
- `tools/computer_use_other.go` → `worker_other.go`
- `tools/computeruse/main.swift` → `swift/main.swift` (byte-identical)
- `tools/computer_use_test.go` → `computeruse_test.go`
- `tools/testdata/computerusehelper/main.go` → unchanged

Herbie dependencies to inline (they're tiny, self-contained):

- `provider.ItemImage` / `provider.ImagePlaceholder` / `provider.ToolDef` /
  `provider.ToolParam` → defined locally in the package (only what's used)
- `system.NewProcessGroup` / `system.SignalProcessGroup` → `spawn.go`
- `parseObject` / `jsonString` / `jsonNull` / `validateJSONStrings` /
  `duplicateField` → `args.go`
- `capToolText` / `capLineLengths` / `outputCapBytes` → `cap.go` (env var
  renamed `COMPUTERUSER_TOOL_OUTPUT_CAP`; constants kept)
- `text.SanitizeUTF8` / `text.TruncateUTF8` → inlined into `cap.go`
- `decodeImageB64` / `sniffImage` / imageInfo → `image.go`, pruned to
  PNG-only support (only format the worker emits) — keep gif/webp/jpeg checks
  if trivially cheap, else drop. **Drop the gif budget machinery entirely**;
  keep `readImageMaxSide`/`readImageMaxBytes` constants.
- `formatDuration` / `tailBuffer` / `browserStderrMax` → inlined into
  `worker.go`

API changes for standalone use:

- Export: `ComputerUse` type (was `computerUse`), `New()`, `Run(input string,
  imageInput int) Result`, `RunContext`, `Close`. Export `computerUseResult`
  as `Result` and `computerUseToolDef` as `ToolDef()` accessor (returns the
  provider-neutral tool definition for callers that want to present it to an
  LLM).
- Worker identity string stays `"herbie-computer-use"` (protocol-compat with
  the Swift worker's hardcoded identity check — **do not rename**; it's a
  handshake field, see main.swift dispatch: `["server": "herbie-computer-use"]`).
- Compile cache dir: `os.UserCacheDir()/computeruser/computer-use/<sha256>`
  (was `herbie/computer-use/...`).

### typesafe (new)

Minimal client for `POST https://api.typesafe.ai/v1/systemone`. No SDK
dependency — the API is a single endpoint; a hand-rolled client is smaller
and matches herbie's zero-dep style.

```go
type Client struct{ apikey string; model string; httpc *http.Client }
func New(apikey string, opts ...Option) *Client          // default model "jev-latest"
type Question struct { ... }                            // type: noul | choice | score
type Answer struct { Type string; Noul float64; Choice string; Score float64; Confidence float64; Probabilities map[string]float64 }
func (c *Client) Ask(ctx context.Context, state any, questions map[string]Question) (map[string]Answer, error)
```

- State can be a string or structured value (docs: "named JSON fields when
  context has several parts").
- Errors: non-200 → error with status + body; decode failure → error.
- Model configurable via option (docs use `jev-latest`; cookbook pins
  `jev-1.12`; we default to `jev-latest`).
- API key from `TYPESAFE_API_KEY` (env), matching SDK convention. An explicit
  key argument can override.

### decide (new — the Jev decision maker)

The heart of the typesafe integration. A bounded loop:

```
goal (natural language, from CLI user)
  │
  ▼
┌─────────────────────────────────────────────────┐
│ Step: gather state                               │
│   apps list (always), windows of active app,    │
│   last snapshot text (if any)                    │
├─────────────────────────────────────────────────┤
│ Step: one TypeSafe request, questions in parallel│
│   q1 (choice): which action next?               │
│       options: apps, windows, activate, snapshot,│
│                click, fill, type, press,        │
│                screenshot, done                 │
│   q2 (choice): which target token? (options from │
│        current snapshot's element list — a      │
│        closed set by construction)              │
│   q3 (noul):   is the goal already satisfied by │
│        what the state shows?                    │
│   q4 (noul):   does the goal require text input?│
│        (speculative; consumed only when relevant)│
├─────────────────────────────────────────────────┤
│ Step: act per typed answers (code, not model)   │
│   done?          → stop, report                 │
│   confidence low → escalate: print the         │
│                    distribution, stop           │
│   action needs a target but none given →        │
│        snapshot first, re-ask (bounded retries)│
│   otherwise execute via computeruse.Run         │
└─────────────────────────────────────────────────┘
  (loop, max N steps, N=16 default)
```

Design rules straight from the TypeSafe skill:

- **Ask independent questions together** in one request (they run in
  parallel, can't see each other's answers). Speculative ones (text needed?)
  are fine — code consumes only what's relevant.
- **Closed sets stay closed**: targets are chosen only from tokens present
  in the current snapshot; Jev never invents an element id. Apps/windows
  tokens likewise.
- **Confidence gates action**: if `confidence < threshold` (default 0.5) on
  the action choice, don't act blindly — print the probability distribution
  and the considered options, and stop. (Skill: "confidence tells you
  whether to act".)
- **State each speculative premise explicitly**; question IDs are for code
  only, not sent meaning to the model — instructions carry the full
  meaning ("complete meaning in the question").
- Code owns the workflow: loop bounds, retry-on-missing-target, token
  bookkeeping, all deterministic.

What Jev does NOT do here: it doesn't type text (that's a free-form value —
the user supplies it in the goal and code extracts/relays it), it doesn't
plan multi-step (it decides one bounded step at a time given fresh state —
skill: "fresh judgments guide the next bounded step"). Free text for fill/
type actions: parse from the goal via simple conventions (`type "hello"
into the search field` → code passes the quoted string verbatim; no model
generation involved).

**Escalation path (fallback for hard cases):** when the confidence gate
trips twice in a row or the loop exhausts its step budget without `done`,
the CLI prints a suggested next action from the highest-probability option
alongside its distribution so a human (or a reasoning LLM elsewhere) can
take over. Jev stays the decision maker; it never fakes certainty.

### cmd/computeruser (CLI driver)

```
computeruser -goal "open TextEdit and type hello world"
computeruser -goal "..." -max-steps 24      # loop bound
computeruser -goal "..." -dry-run           # print decisions, execute nothing
computeruser -goal "..." -json              # ndjson trace of steps
computeruser apps                            # direct passthrough (no Jev)
```

- Flags: `-goal`, `-max-steps`, `-dry-run`, `-json`, `-key` (else env).
- Passthrough mode (no `-goal`): positional `action` + flags → runs the tool
  directly, useful for scripting and testing the extraction.
- Reads `TYPESAFE_API_KEY` from env or `.envrc`-adjacent environment.

## Tests

- Port herbie's `computer_use_test.go` suite (worker protocol, arg parsing,
  crash-restart, transport vs action errors, image validation) — all against
  the fake helper binary, no Accessibility needed, no network.
- `typesafe`: tests against a stub `httptest.Server` (no network in tests).
- `decide`: fake TypeSafe server + fake computer use (already have the
  helper-worker pattern) driving the full loop; assert step sequencing,
  confidence gating, done detection.

## Non-goals

- No MCP server, no LLM provider integration, no session management — this
  repo is the tool + Jev decision layer only.
- Not keeping `reset()`/dispatcher integration from herbie's tools package.
- No Windows/Linux GUI support (worker is macOS Accessibility; other
  platforms get a clear stub error).

## Verification

`make build test` → `go build ./...`, `go vet ./...`, `go test ./...`.
Live smoke: `computeruser -goal "list running apps"` against the real Jev
API (needs `TYPESAFE_API_KEY`) and the real worker (needs Accessibility
grant for the terminal).
