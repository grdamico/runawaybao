# Demonstration and recording guide

[Back to the project overview](../README.md)

The demo presents two independent capabilities. The same process identity chain
is relevant to future responses, but **Tetragon does not currently trigger
credential revocation**. The separate policy mode adds dry-run allowlist findings; no security enforcement
is performed.

## Prepare the disposable lab

Follow [local setup](testing.md#getting-started), then run:

```bash
./scripts/status.sh
./scripts/demo.sh --help
```

The wrapper requires an explicit mode: `credentials`, `correlation`, `policy`, or `all`.
It changes to the repository root, disables shell tracing, and runs the existing
live integration scripts sequentially. It adds headings and intent, keeps their
real output, and exits nonzero on failure. A success heading appears only after
the corresponding suite succeeds; no results are fabricated or substituted.

## A. Credential isolation

```bash
./scripts/demo.sh credentials
```

This delegates to [test-database.sh](../scripts/test-database.sh). It starts the
project's OpenBao and PostgreSQL services if needed and creates a temporary
OpenBao database mount, connection, role, and separate issuer/revoker policies
and tokens. Checkpoints in the actual test output confirm:

1. A and B obtain distinct credentials and leases and complete the fixed read.
2. OpenBao privilege boundaries and PostgreSQL permission denials hold.
3. Explicit exact-lease revocation rejects A's original credentials while B reads.
4. Repeated revocation, expiry, and cleanup work.

This is a trusted-controller lifecycle demonstration. It is not a response to a
Tetragon event. Credentials and connection strings are not printed.

## B. Runtime correlation

```bash
./scripts/demo.sh correlation
```

This delegates to [test-runtime-correlation.sh](../scripts/test-runtime-correlation.sh).
It starts all project services, pulls `alpine:3.22`, and creates temporary bootstrap
configuration plus a restricted issuer token. It creates no revoker token or policy.

Actual checkpoints confirm stream readiness before launch; independent A/B
credentials and restricted containers; shell/child attribution to the correct
job and lease; an unregistered workload remaining uncorrelated; interruption,
gap reporting, and reconnection; and continued access using both original database
credentials. Owned containers are removed, and leases/token expire naturally
before configuration teardown. Allow about two minutes for this expiry wait.

PostgreSQL queries run in the controller. The observed containers are secret-free
and have no network access. Children correlate by their own verified container
identity; full ancestry reconstruction remains unverified on the tested host.
See [identity limitations](security-model.md#reliability-and-limitations).

The combined `./scripts/demo.sh all` workflow was verified against the live local
lab on 2026-11-17. Both delegated suites passed, including continued database
access during observation and natural-expiry teardown. This was a terminal run,
not a generated recording.

## Cleanup and failure handling

`./scripts/demo.sh all` runs A then B and stops at the first failure. Successful
suites clean their own temporary resources but leave Compose services running.
Use `./scripts/stop.sh` afterward; it destroys this disposable lab's in-memory data.
A forced stop or failed connection can prevent cleanup. Inspect only test-owned
resources; never run broad Docker cleanup commands as part of a recording.

When a live check fails, report the failure rather than editing it into a pass.
Inspect raw development logs only privately and redact sensitive diagnostics.
Do not record `.env`, `docker inspect` environment output, Compose's resolved
configuration, manual secret-read scripts, or OpenBao startup logs.

## Optional local terminal recording

No recording tool is required by RunawayBao or CI. The following workflow targets
[asciinema 2.x](https://github.com/asciinema/asciinema/tree/v2.4.0#usage), whose
`rec --command` option runs a specific command. Install it separately if wanted;
check your installed version's help before using another major version.

After verifying the lab and running the demo successfully, record only the demo
command, not an interactive shell containing private setup commands:

```bash
umask 077
asciinema rec --command './scripts/demo.sh all' /tmp/runawaybao-demo.cast
asciinema play /tmp/runawaybao-demo.cast
```

This creates a local recording of actual execution. It can take several minutes,
including expiry waits. Do not enable stdin recording or pass any `--env` options;
review recorder configuration as well as command output for sensitive data.
Review the entire file locally, including its metadata, before publishing.
No upload command is included and nothing is published by this workflow.

Recordings are intentionally outside the repository. They are not CI artifacts
and are not evidence that a future run will pass. No GIF, screenshot, or terminal
recording is bundled or claimed as generated. Optional conversion/sharing is a
separate manual step after review.

## C. Dry-run execution policy evaluation

```bash
./scripts/demo.sh policy
```

This runs [test-runtime-policy.sh](../scripts/test-runtime-policy.sh). Policies are
validated and bound before each workload starts. A permits the shell; B explicitly
does not. Verified stable paths yield `allowed` for A and `suspicious` plus the
text recommendation `would_revoke_lease` for B. That recommendation is audit only.

Both jobs retain database access afterward. Unknown/ambiguous attribution cannot
target a recommendation; unstable descriptor-path records stay unknown. Coverage
gaps remain explicit across reconnection. Audit JSON is validated for absence of
credentials, lease IDs, arguments, environment, and connection strings. Normal
owned-container cleanup and natural credential expiry remain separate from decisions.

`all` continues to run the original A/B demonstrations only; choose `policy`
explicitly. To record just this mode with asciinema 2.x, use the same reviewed
recording workflow with `--command './scripts/demo.sh policy'`. No recording is
bundled or claimed as generated by this milestone.
