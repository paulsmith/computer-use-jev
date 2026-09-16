# computeruser

Drive macOS applications from Go — with **Jev** ([TypeSafe](https://typesafe.ai)'s
System One decision model) as the decision maker.

Extracted from [herbie](https://github.com/paulsmith/herbie)'s `computer_use`
tool and extended with a typesafe decision layer.

## How it works

- **computeruse** — operates native macOS applications through the
  Accessibility API via a persistent Swift worker (`computeruse/swift/`).
  Actions: `apps`, `windows`, `activate`, `snapshot`, `click`, `fill`, `type`,
  `press`, `screenshot`. Tokens (`a1`, `w2`, `e5`) reference apps, windows,
  and elements discovered at runtime.
- **typesafe** — a minimal client for the TypeSafe System One API
  (`POST /v1/systemone`, model `jev-latest`).
- **decide** — the Jev decision loop. Given a natural-language goal, each
  step gathers real state (app list, windows, last snapshot) and asks Jev one
  batch of typed questions: which action next, which target token, whether
  the goal is satisfied, whether text is needed. Jev's answers are typed —
  code, not the model, maps them to a tool call. If the action confidence
  drops below a threshold, the run stops and reports the distribution rather
  than acting blindly. Closed sets stay closed: targets are chosen only from
  tokens actually present in the current snapshot.

## Install

```sh
go build ./cmd/computeruser
```

macOS 14+. Requires the Accessibility permission (and Screen Recording for
screenshots) granted to the running terminal, plus Xcode Command Line Tools
to compile the Swift worker (built once, then cached).

## Use

Let Jev drive toward a goal (needs `TYPESAFE_API_KEY`):

```sh
computeruser -goal 'in TextEdit, type "hello world"'
computeruser -goal 'open Safari and take a screenshot' -json
computeruser -goal '...' -dry-run        # decide, don't act
computeruser -goal '...' -max-steps 24
```

Or call the tool directly, no Jev, for scripting:

```sh
computeruser apps
computeruser windows -app a1
computeruser snapshot -window w1
computeruser fill -target e3 -text "hi"
```

## Development

```sh
make check    # build, vet, test
make smoke    # live run against Jev + real Accessibility
```

Tests are hermetic: a fake worker binary stands in for the Swift worker, and
an `httptest.Server` stands in for the TypeSafe API.

## License

MIT
