# Contributing to RunawayBao

RunawayBao is an experimental local security lab. Start with the
[architecture](docs/architecture.md), [security model](docs/security-model.md), and
[testing guide](docs/testing.md).

## Scope and review

Open an issue or describe the concrete problem in a pull request. Keep changes
small, idiomatic Go, and focused on one responsibility. Explain the resulting
behavior, tests performed, and remaining limitations. Label planned functionality
clearly; do not present mocked outcomes as live evidence.

Changes to identity attribution, credential delivery, policy semantics, or
revocation need an explicit design discussion. Preserve the separation between
observation and enforcement. Never weaken identity or authorization checks to
make a demonstration pass.

## Local checks

```bash
go test ./...
go test -race ./...
go vet ./...
go test -tags=integration -run '^$' ./...
go vet -tags=integration ./...
for script in scripts/*.sh postgres/init.sh; do
  bash -n "$script"
done
git diff --check
```

Use `gofmt` on changed Go files. Add tests for meaningful behavior changes,
particularly failures, cancellation, concurrency, and uncertain identity. Unit
checks use local sockets but need no `.env`, Docker, or running services.

Run relevant live tests only against a disposable local lab, sequentially. State
exactly which live suites ran, which skipped, and which scenarios remain
unverified. Standard CI compiles live tests without executing them.

## Credentials and test resources

Keep `.env`, private bindings, tokens, passwords, unredacted event payloads, and
connection strings out of commits, issues, recordings, and logs. Do not enable
shell tracing around bootstrap or live tests. OpenBao development logs may print
the root token; resolved Compose configuration can also contain secrets.

Use fake fixtures only. Clean up only resources owned by your test/controller;
never prune unrelated Docker resources or bypass another agent's PID guard.
Review terminal recordings locally before sharing them.

For security-sensitive reports, omit secret values and exploit details from
public issues. Use GitHub private vulnerability reporting if enabled; no private
reporting channel is configured or promised by this document.

Execution policy changes must keep the evaluator pure, preserve immutable versions,
and distinguish unknown from allowed. Do not add enforcement capabilities or
serialize raw event/correlation structures into audit output. Run the policy live
suite when changing normalization, profile paths, or live policy wiring.
