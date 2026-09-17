# Goal planning implementation plan

Approved design: a Jev classifier precedes desktop execution. Single goals pass through unchanged; compound goals are split once by a provider-neutral LLM into a bounded ordered JSON array. Execute in a shared worker session, stop on failure, retain original-goal context, and refresh observations between instructions. The splitting LLM never receives tools or controls the desktop.

## Configuration and source boundary

Require COMPUTER_USE_JEV_PROVIDER and COMPUTER_USE_JEV_MODEL only on the splitting branch. Resolve provider keys using Herbie's environment names, including OPENAI_API_KEY, ANTHROPIC_API_KEY, OPENROUTER_API_KEY, and LUNAROUTE_API_KEY. Copy Herbie's provider interface, OpenAI/Anthropic wire implementations, and their transitive production dependencies into internal/herbie with rewritten imports and attribution. No imports of the Herbie agent runtime or personal config. Lunaroute uses its configured gateway endpoint, never a copied credential.

## Implementation

- [x] Copy and attribute the 29-file production dependency closure; retain upstream tests where self-contained.
- [x] planner/: test single-goal bypass, classifier failure/ambiguity, lazy env configuration, and validated split responses (nonempty instructions, bounded count/size, no extra JSON).
- [x] planner/: implement classification plus streamed text aggregation through provider.Provider; fail before desktop activity on invalid configuration or output. Preserve user text, ordering, targets, and references in the split prompt. Unknown providers error explicitly.
- [x] decide/: execute a sequence through the same runner; keep original goal and completed instructions as context, reset per-instruction history, refresh the prior scoped window between instructions, and stop on failed instructions. Apply max-steps across the entire goal. Dry-run never executes later instructions against an unchanged desktop.
- [x] decide/: select semantic shortcuts from a closed set (select-all, bold, italic, underline, copy, paste, save, undo, redo, none). Explicit shortcuts in the goal retain precedence; reject missing/unknown/low-confidence choices before input.
- [x] cmd/: plan first, report instruction boundaries in text/JSON, then execute; direct passthrough remains unchanged. Document env vars and examples.
- [x] Verify full build/vet/tests/race checks and rebuild build/computer-use-jev. Do not commit, push, or mutate real desktop documents during tests.

## Risks addressed

A splitter can invent instructions: demand JSON-only faithful decomposition and validate shape/limits; it is not a source of authority for extra tasks. Classification near 0.5 fails closed rather than silently guessing. Fresh UI state may differ from prior snapshots: observations refresh between instructions. Command success does not prove a later command completed; each instruction has separate completion state. Secrets remain in env and are excluded from diagnostics and copied sources.

## Verification results

Build, vet, unit/integration tests, and race tests pass. Tests cover all four
provider wire/credential combinations, CLI JSON dry-run, shortcut gating,
shared-session execution, fresh state/window scope, stopping, and total budgets.
Live Jev classified a single formatting instruction as single and the requested
TextEdit sequence as compound. Live LLM splitting was not attempted because
COMPUTER_USE_JEV_PROVIDER, COMPUTER_USE_JEV_MODEL, and LUNAROUTE_API_KEY are unset.
Full golangci-lint reports seven pre-existing issues in typesafe/ and computeruse/;
new planner, sequencing, and copied-provider code introduce no lint findings.
