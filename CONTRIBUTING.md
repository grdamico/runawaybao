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
observation and enforcement. Milestones 1–6B are live-verified in the local
lab. Dry-run remains the default and the separate enforcement test requires
operator confirmation. Preserve identity and authorization checks in demonstrations.

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

Use disposable fixture data only. Treat generated credentials as secrets. Clean up
only resources owned by your test/controller;
never prune unrelated Docker resources or bypass another agent's PID guard.
Review terminal recordings locally before sharing them.

For security-sensitive reports, omit secret values and exploit details from
public issues. Use GitHub private vulnerability reporting if enabled; no private
reporting channel is configured or promised by this document.

Execution policy changes must keep the evaluator pure, preserve immutable versions,
and distinguish unknown from allowed. Keep enforcement in the trusted
controller and raw event and correlation structures out of audit output. Run the policy live
suite when changing normalization, profile paths, or live policy wiring.

For containerized database changes, run `./scripts/test-container-database.sh`
against the disposable lab. Preserve bounded attached-stdin delivery, startup
identity/policy binding, independent owned networks and retryable cleanup metadata.
Keep its results separate from the legacy controller-query demonstrations. Validate
Markdown file links and heading anchors, demo mode names, and Mermaid diagrams
when updating documentation. Review tracked, staged and non-ignored new files
before committing; `.env.example` is the intentional placeholder-only exception
for environment configuration. Keep the private enforcement journal outside the
repository.

Before every commit and push, verify that documentation accurately reflects
implemented and verified functionality. Update the changelog and release notes
when preparing a release.
