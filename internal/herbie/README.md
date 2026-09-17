# Copied Herbie provider code

Source: Paul Smith's local `~/projects/herbie`, working-copy revision
`81dac099a93d4fc605aca83e6544cb45e5b2f8f4`, copied September 16, 2026.

This directory contains Herbie's provider-neutral conversation/stream interface,
OpenAI Chat Completions/Responses and Anthropic Messages implementations, and
only their transitive production dependencies (29 Go files). Provider, text,
and version tests were copied as well. Production changes consist of import
and linker-path rewrites from `github.com/paulsmith/herbie/` to
`github.com/paulsmith/computer-use-jev/internal/herbie/`, plus omission of seven
unused private helpers used only by upstream tests/constructors: `loadState`,
`atomicJSON`, `body`, `newEvents`, `newCompatible`, `newOpenAI`, and `postJSON`.

The splitter uses `provider.Provider.Stream`; it does not import Herbie's agent,
CLI, tool runtime, or personal configuration. `planner/provider.go` chooses
providers and resolves credentials from the environment. The MIT license and
upstream copyright notices are retained in LICENSE.
