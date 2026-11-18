# Runtime execution policies (Milestones 4–5)

[Project overview](../README.md) · [Architecture](architecture.md) · [Security model](security-model.md)

Milestone 4 evaluates correlated execution records against explicit workload
allowlists and emits explainable **dry-run** JSON. It has no enforcement mode.
A `suspicious` outcome means a policy deviation, not proof of malicious intent.

## Boundaries and data flow

```text
Tetragon JSON → existing normalization → verified correlation
  → immutable workload policy + coverage snapshot
  → pure allowlist evaluation → sanitized JSON audit stream
```

[executionpolicy](../executionpolicy/policy.go) uses standard-library value types
and performs no I/O or clock reads. It imports no Docker, OpenBao, Tetragon, or
execution interfaces. Its compiled policies contain private copied allowlists.
Workload identity is an opaque validated identifier in the evaluator; Docker ID
format and prefix checks stay in the adapter/correlator. Evaluation is deterministic
for identical inputs and does not maintain process
history or duplicate the correlator's deduplication cache.

[policyaudit](../policyaudit/adapter.go) adapts trusted event/correlation results,
checks workload-policy bindings, hashes the event reference, and writes safe JSON.
It receives no credential client, revoker token, database manager, or workload
launcher. Correlation remains the existing component's responsibility; event-provided
job IDs are never used. Audit-write failures return a fixed error and stop the
observation loop, without changing workloads or leases.

`would_revoke_lease` is an audit string only. No action interface, callback,
revocation client, termination capability, or enforce flag exists in the evaluator.
Normal test-owned container teardown and expiry are independent of decisions.

## Typed configuration and immutable bindings

Policies are Go structures, not YAML or an interpreted policy language:

```go
policy := executionpolicy.Policy{
    ID: "worker", Version: "v1",
    AllowedExecutables: []string{"/bin/sleep", "/bin/echo"},
}
compiled, err := executionpolicy.Compile(policy)
// Handle err before admitting a workload. compiled is immutable.
```

Compilation rejects invalid identifiers, empty/oversized allowlists, duplicate
paths, noncanonical/relative paths, control characters, and unstable descriptor
references. Up to 64 exact paths are accepted. There are no wildcards, basename
matches, path rewriting, filesystem lookup, or binary-integrity checks.

The trusted live coordinator compiles all profiles before launch, binds each
policy ID/version to a unique job/full Docker ID **before Docker Start**, and
rejects missing/conflicting bindings. Catalog versions and bindings cannot be
updated or hot-reloaded. The adapter is bounded to 64 catalog entries and 64
bindings; it refuses admission at capacity rather than replacing an old policy.

The CLI's explicit demo catalog has two profiles:

| ID / version | Exact allowed paths |
|---|---|
| `demo-shell` / `v1` | `/usr/bin/tail`, `/bin/sh`, `/bin/sleep` |
| `demo-no-shell` / `v1` | `/usr/bin/tail`, `/bin/sleep` |

The same harmless `/bin/sh` execution is legitimate under the first profile and
an explicit deviation under the second. Custom programmatic policies use the
same Go API; the CLI does not load arbitrary policy definitions from files.

## Decisions and uncertainty

Decisions include mode, verified job/workload IDs where available, policy
ID/version, a SHA-256 event reference, execution timestamp, correlation status
and reason, verification method, outcome, rule ID, reason code, coverage status,
gap generation/history indicator, and separate terminated/out-of-order flags.
The event reference hashes node plus execution ID, not PID. Decision time comes
from `process.start_time`; the evaluator never invents a wall-clock timestamp.
Missing timestamps remain the zero value with an unknown outcome.

No decision field stores lease IDs, credentials, tokens, arguments, environment,
connection strings, raw event payloads, or the observed executable path. Fixed
reason codes and the immutable policy version explain which rule was evaluated.
Identifiers must be non-sensitive trusted labels. The writer serializes decision
and coverage structures only, never raw correlation results containing lease IDs.

| Condition | Outcome / behavior |
|---|---|
| Verified active event, exact permitted path | `allowed`, for this observed execution only |
| Verified active event, stable path outside the allowlist | `suspicious`; audit-only `would_revoke_lease` |
| Missing/ambiguous/conflicting identity | `unknown`, with no targeted job recommendation |
| Missing policy | `unknown`; correlation confidence remains separate |
| Invalid/conflicting configured policy | Admission error before workload launch; no permissive fallback |
| Missing/noncanonical execution metadata | `unknown` |
| Unstable `/proc/...` or `/dev/fd/...` reference | `unknown`, `unstable_executable_reference`, no recommendation |
| Duplicate | `not_applicable`, no repeated targeted recommendation |
| Active out-of-order event | Evaluate the immutable policy; preserve `out_of_order` |
| Terminated workload's historical event | `unknown`, `terminated_workload`, no recommendation |
| Unavailable/currently interrupted coverage | `unknown`; queued events crossing a gap cannot claim current coverage |
| Resumed observation with known previous gaps | Event-local evaluation resumes; `had_gaps` remains true |
| Unsupported event type | Pure evaluator returns `not_applicable`; the consumer otherwise counts non-exec types without evaluating them |
| No received event | No decision and no claim of safety |

The correlator retains its existing `Late` field for compatibility and now also
reports `Terminated` and `OutOfOrder` independently. Children require their own
verified container identity; parent metadata cannot supply a missing identity.

### Coverage under queue pressure

The consumer freezes a coverage snapshot on each notice. Its authoritative
counter/status getter remains available even when a gap notice is dropped from
the bounded queue. Effective coverage combines that snapshot with current state:
a queued event spanning a new gap receives unknown coverage and the latest gap
count. Fresh post-reconnect events can be evaluated with the prior gap history
visible. The CLI and policy harness also poll snapshots to audit changes without
waiting for an event or relying on every notification being delivered.

Delivered gaps get their own timestamped, reason-coded audit records. Periodic
coverage records retain increasing counts when individual gap records are lost.
There is no durable audit store, unlimited gap history, historical event replay,
or exactly-once delivery guarantee. `allowed` never means gap-free or globally safe.

## CLI and live demonstration

Default correlation-only behavior is unchanged:

```bash
go run ./cmd/observe-jobs /path/to/private-bindings.json
```

Policy evaluation must be selected explicitly:

```bash
go run ./cmd/observe-jobs --dry-run /path/to/private-bindings.json
```

For dry-run, each existing private binding also needs `policy_id` and
`policy_version` matching the typed catalog. Unknown profiles and conflicts are
rejected before observation. The file remains controller-only metadata and may
contain the lease ID required by the existing correlator; that ID is never
serialized to the dry-run output. The CLI attaches to existing workloads and
trusts admission metadata from their coordinator; it cannot attest when that
coordinator historically selected the policy. It starts no workloads and offers
no policy-reload or enforcement option.

Run the complete admission-before-start scenario against the disposable lab:

```bash
./scripts/test-runtime-policy.sh
# Presentation wrapper for the same suite:
./scripts/demo.sh policy
```

The harness reuses the Milestone 3 setup, creates no revoker token/policy, and
uses a non-revoking stub for unexpected database cleanup attempts. A's stable
workload executions must be allowed. B's shell must be suspicious under its own
profile. Unknown events get no targeted recommendation. The scenario interrupts
and reconnects observation and verifies that both original PostgreSQL credentials
still work. Owned containers are removed as ordinary teardown; credentials and
the issuer token expire naturally before temporary bootstrap configuration removal.
In this retained Milestone 4 demonstration, PostgreSQL queries run in the
controller. The Milestone 5 demo below runs them inside monitored containers.

## Verification and limits

On 2026-11-18, both ordinary and race-instrumented live policy runs passed,
including post-evaluation database access and natural-expiry teardown.

The live scenario verified `/usr/bin/tail`, `/bin/sh`, and `/bin/sleep` from actual
Tetragon records. It also exposed `/proc/self/fd/6` startup records. Such a reference
cannot identify a stable executable for this path-only policy: it stays unknown
rather than being added to the allowlist or silently allowed. This is a blind spot,
not evidence that descriptor-based execution is safe. More complete executable
identity would require a future policy type.

Exact paths can produce false positives for legitimate helpers or alternate
paths, and cannot detect changed binary contents or malicious behavior inside an
allowed executable. Shells are not inherently malicious. Existing prefix-resolution,
lineage, volatile-state, and event-loss limits still apply. Policy bindings rely on
trusted controller admission and remain in memory.

## Milestone 5 database worker profile

`containerjob.Policies()` supplies the immutable `container-reader:v1` profile for
the new explicit containerized database demo. Its exact paths are
`/runawaybao-db-worker` and `/runawaybao-netprobe`. The latter is a fixed,
credential-free laboratory connectivity diagnostic. B alone runs the harmless
`/runawaybao-unexpected` child, which is absent from the allowlist. The initial
bootstrap can produce unstable descriptor references; those retain the existing
unknown semantics. The gate then re-execs the worker after policy/identity binding.

Both workers perform real database reads before and after evaluation. Findings
are audit strings only: the executor has no decision input, revoker or policy-driven
stop path. Ordinary finish/cancellation/resource teardown are separate lifecycle
operations. The new live path constructs an issuance-only manager and waits for
natural expiry; the older controller-query policy demo is preserved unchanged.

The new profile is selected by the live coordinator, not a default change to the
standalone observer CLI catalog. That CLI keeps its existing Milestone 4 profiles
and correlation-only default. Attaching it to the new profile would require an
explicit catalog addition and trusted admission bindings.
