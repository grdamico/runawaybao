# Local setup, testing, and verification

[Back to the project overview](../README.md)

## Getting started

### Prerequisites

- Go 1.22 or newer; the PostgreSQL client uses `pgx/v5`. Initial module downloads
  require network access. Offline tests need no Docker or `.env`, but open local
  HTTP fixture sockets. The race detector requires a supported Go/CGO environment.
- Docker Engine or Docker Desktop with Compose v2, accessible as your user.
- Bash, `curl`, and standard Linux utilities for the scripts.
- For Tetragon: a compatible Linux Docker host with eBPF/BTF support and the
  kernel, BPF, and runtime mounts used in [compose.yaml](../compose.yaml).

On Docker Desktop/WSL2, enable integration for the WSL distribution and run from
that distribution. Tetragon observes the Docker Linux VM's processes, not Windows
processes. Other host/kernel combinations require validation.

### Configure and start the lab

From the repository root:

```bash
go version
docker info
test -f .env || cp .env.example .env
chmod 600 .env
./scripts/start.sh
./scripts/status.sh
docker compose -p runawaybao -f compose.yaml ps
```

[.env.example](../.env.example) contains disposable development placeholders. Use only
fake data in this lab. `.env` is ignored by Git; never force-add it. Scripts source
it as trusted shell code.

| Variable | Purpose / default |
|---|---|
| `BAO_TOKEN` | Bootstrap development root token; not a normal job token |
| `BAO_PORT` | OpenBao loopback host port; defaults to `8200` |
| `POSTGRES_PORT` | PostgreSQL loopback host port; defaults to `15432` |
| `POSTGRES_PASSWORD` | Disposable PostgreSQL administrator bootstrap password |
| `DB_MANAGER_PASSWORD` | Disposable OpenBao database management-account password |

Change conflicting ports in `.env`; the verified lab used OpenBao port `18200`.
Do not stop unrelated containers to free a port. Scripts pin Compose project
`runawaybao` and the repository's configuration file. Reserve that project name
for one lab; separate clones share the same name.

`start.sh` creates/starts all three services and polls readiness. PostgreSQL's
init script creates its schema and roles on initialization. Starting services
alone does not configure the OpenBao database engine: each live test creates its
own temporary engine mount, connection, roles, and policies.

### Run the demonstrations

```bash
# Fixed JSON examples: success, denial, simulated failure, unknown action.
go run ./cmd/mock-runner

# Live restricted KV access and ACL checks; requires the lab already running.
./scripts/test-openbao-acl.sh

# Live per-job PostgreSQL credentials and explicit selective lease revocation.
./scripts/test-database.sh

# Live Docker/Tetragon correlation; does not request job lease revocation.
./scripts/test-runtime-correlation.sh

# Explicit dry-run policy evaluation using the retained controller-query scenario.
./scripts/test-runtime-policy.sh

# Real monitored PostgreSQL workers with bounded stdin delivery and owned networks.
./scripts/test-container-database.sh
```

Run live suites sequentially against the disposable lab, with shell tracing off.
The database script starts OpenBao/PostgreSQL if needed. The runtime script starts
all services and pulls `alpine:3.22`. Both configure temporary OpenBao resources.
The runtime test launches A, B, and an owned but unregistered container, waits for
observations, interrupts/reconnects the stream, verifies continued database access,
and removes its containers. Natural credential/token expiry makes it take about
two minutes; event delivery can be delayed on this host.

Optional manual tools are also available:

| Tool | Purpose / resource effects |
|---|---|
| `scripts/put-demo-secret.sh` | Uses the bootstrap token to write the fixed dummy KV fixture |
| `scripts/read-demo-secret.sh` | Uses the bootstrap token and prints the dummy response; never use with real secret data |
| `scripts/test-process.sh` | Compact, text-filtered manual event viewer; not the structured correlation consumer |
| `cmd/observe-jobs` | Attaches the observer to existing trusted bindings; does not provision jobs or credentials |

The standalone observer accepts a private JSON array with `job_id`, `workload_id`
(full Docker ID), `lease_id`, and `started_at` (exact Docker `StartedAt`) fields.
A trusted coordinator must supply these records. Keep the file mode `0600` and
outside version control. With a valid file already prepared:

```bash
go run ./cmd/observe-jobs /path/to/private-bindings.json
```

It verifies container generations before attaching and prints correlation status,
reason, job ID, late-event indicator, and observation generation. It omits lease
IDs and raw event payloads. The retained two-phase scenario and the Milestone 5 containerized coordinator
live in integration harnesses, not this attachment-only CLI.

### Cleanup and troubleshooting

Successful live suites remove their temporary mounts/policies and fixture data
as applicable. The database suite explicitly cleans up leases and test tokens;
the runtime suite waits for natural expiry before removing configuration. The
workload manager removes only recorded container IDs with its random ownership
label. Services remain running after tests.

```bash
./scripts/stop.sh
```

This removes the `runawaybao` Compose containers and network. PostgreSQL uses
tmpfs; stopping its container loses its data and generated logins. Stopping
OpenBao's dev server loses secrets, tokens, policies, and lease records. After
state loss, recreate the disposable lab and rerun setup/tests.

Forced termination or connectivity loss can prevent cleanup. Inspect only this
project's test-owned resources before recovery; do not use broad Docker pruning.
Changing initialization SQL or bootstrap passwords requires fresh PostgreSQL
initialization.

If startup fails, first check Docker access, WSL integration, port conflicts, and
kernel/mount compatibility. Health checks do not prove event delivery; run the
live correlation test. Inspect OpenBao logs privately: development startup logs
can expose the root token. Never publish unredacted logs or resolved Compose
configuration. If another Tetragon instance owns the host instrumentation,
coordinate its shutdown rather than bypassing its PID-file guard.

## Live verification

Live validation completed on **2026-11-17** using Docker Desktop 29.7.2, OpenBao
2.7.1, PostgreSQL 17.12, and Tetragon 1.7.0 in the local WSL laboratory. These are
observed test results, not production guarantees or benchmarks.

| Area | Verified behavior | Validation |
|---|---|---|
| Runner | Authorization, duplicate handling, cancellation, and safe failure results | Unit / HTTP fixture / race tests |
| OpenBao KV | Restricted reads succeed; updates and reads of another existing key receive 403; revoked token is rejected | Live `TestLiveACL` |
| PostgreSQL | A/B receive distinct usernames and lease IDs; both perform the fixed read | Live `TestLiveDatabase` |
| Database authorization | Writes, restricted-table reads, DDL, and role creation are denied | Live `TestLiveDatabase` |
| Revocation | Exact revocation of A rejects fresh authentication; B remains functional; repeats and expiry are handled | Live `TestLiveDatabase` |
| Tetragon | Structured execution events from restricted containers are received | Live source and runtime tests |
| Correlation | A/B shells and controlled children map to their own containers, jobs, and leases | Live `TestLiveRuntime` |
| Unknown events | Unregistered-container and unrelated events remain uncorrelated | Live `TestLiveRuntime` |
| Stream interruptions | Gap recorded, reconnection succeeds, and subsequent A execution correlates | Live `TestLiveRuntime` |
| Credential preservation | Both original database credentials still read after observation | Live `TestLiveRuntime` |
| Cleanup | Owned containers removed; leases expire; post-test inspections found no temporary test mounts, policies, or generated roles | Live suites and cleanup inspection |

Ambiguous/conflicting identities, PID reuse, restart invalidation, duplicates,
out-of-order events, and processing limits are covered by unit tests. Those
synthetic cases are not claimed as live fault-injection results. Standalone
source tests also verified reader cleanup; the restricted lifecycle test verified
create/start/child execution/stop/remove.

Full parent-graph reconstruction remains unverified; child attribution uses its
own verified container identity. Fresh-clone portability to other hosts and the
standalone observer CLI's complete workflow have not been established by these
live suites.

## Testing

Run from the repository root:

```bash
go test ./...
go test -race ./...
go vet ./...
go vet -tags=integration ./...
```

These checks passed during milestone verification. Tests use Go's standard
`testing` package, local HTTP fixtures, and injected dependencies. They cover
request authorization, credential-client behavior, isolated lease state, cleanup
failures/retries, cancellation, identity resolution, process deduplication, and
bounded stream processing.

The live suites require Docker, the local services, `.env`, and disposable data:

```bash
./scripts/test-openbao-acl.sh
./scripts/test-database.sh
./scripts/test-runtime-correlation.sh
```

The scripts enable the `integration` build tag and the corresponding explicit
opt-in variables. Compiling or running tagged tests alone does not enable them:

```bash
go test -tags=integration ./...
```

Without opt-in, live tests **skip**. A skipped suite is not live verification.

For focused, credential-free diagnostics after the lab is running and
`alpine:3.22` is available locally:

```bash
RUNAWAYBAO_TETRA_SOURCE_INTEGRATION=1 go test -tags=integration ./runtimeevents -run '^TestLive' -count=1 -v
RUNAWAYBAO_WORKLOAD_INTEGRATION=1 go test -tags=integration ./workload -run '^TestLiveLifecycle$' -count=1 -v
```

These opt-ins observe Tetragon and/or create restricted test-owned containers;
they do not provision OpenBao credentials. See the source tests for timeouts and
cleanup behavior.

## Standard GitHub Actions CI

[The workflow](../.github/workflows/ci.yml) runs on pushes and pull requests on
Ubuntu 24.04. It selects Go from `go.mod`, enables dependency/build caching using
`go.sum`, and uses commit-pinned checkout/setup actions. Permissions are limited
to `contents: read`; checkout credentials are not persisted.

CI runs ordinary unit tests, race tests with CGO enabled, vet, shell syntax, and
whitespace checks. Local HTTP fixtures use loopback sockets on the hosted runner;
no socket tests are skipped. Integration-tagged tests are compiled with
`go test -tags=integration -run '^$' ./...` and vetted with the same tag. The empty
test selection executes no test bodies, and all live opt-ins are explicitly
set to `0`. No repository secrets, `.env`, Docker services, privileged workloads,
or eBPF setup are used by this workflow.

The existing CI is operational; this changed revision still needs its next hosted
run after push to establish GitHub runner verification. Local execution of the same Go commands validates the code paths,
not GitHub's action orchestration. Live demonstrations stay opt-in outside CI;
see the [presentation and recording guide](demo.md).

## Milestone 4 policy verification

```bash
./scripts/test-runtime-policy.sh
```

This opts into `TestLivePolicy` using `RUNAWAYBAO_POLICY_INTEGRATION=1`. Standard CI
sets that opt-in to `0`, compiles the tagged tests without running their bodies,
and runs policy unit/race/vet checks with the other packages. The live test uses
only an issuer token and waits for natural expiry; it performs no policy-triggered
revocation or termination. In this retained Milestone 4 suite, database operations run in the controller.

The live A/B scenario verified exact emitted workload paths, A allowed, B shell
suspicious, unknown events untargeted, reconnection/gap reporting, retained original
PostgreSQL access, and secret-free audit JSON. Unit tests cover missing/invalid or
conflicting policies, immutable versions, deterministic output, correlation
uncertainty, duplicate/out-of-order/historical events, dropped gap notices, unsafe
metadata, audit failures, and the default CLI's compatibility. Synthetic ambiguity
and queue-pressure cases are not claimed as live fault injection.

See the [policy guide](policy.md) and [demo guide](demo.md). Existing Milestones 1–3
live suites remain separate regression checks.

## Milestone 5 containerized database verification

```bash
./scripts/test-container-database.sh
# Same test with presentation labels:
./scripts/demo.sh container-database
# Optional instrumented live coordinator:
GOFLAGS=-race ./scripts/test-container-database.sh
```

This explicitly enables `RUNAWAYBAO_CONTAINER_DATABASE_INTEGRATION=1` and runs
`TestLiveContainerDatabase`. The script builds `worker/Dockerfile` with a narrow
`.dockerignore` context (no `.env`, Git metadata or controller code), resolves the
image to an immutable ID and uses the existing disposable lab. Standard CI sets
the opt-in to `0`; tagged compilation is not live execution. Go 1.22+ is still the
module baseline. Docker must support bridge gateway mode `isolated` (verified
locally with Engine 29.7.2); older engines fail rather than weaken isolation.

Expected live checks:

- Structured event readiness precedes admission. A/B receive unique leases,
  credentials, full container IDs, and independently owned internal networks.
- Each pgx read originates inside its monitored container. Private administrator
  queries match `pg_stat_activity` backend PID, username and `client_addr` to the
  trusted Docker network address, before and after policy evaluation. Passwords,
  connection strings and server rows are never printed.
- Both dynamic users receive PostgreSQL permission error `42501` for a write.
- Tetragon's exact `/runawaybao-db-worker` path is allowed for A/B. Only B launches
  the fixed `/runawaybao-unexpected` child; its own container identity must resolve
  to B and produce a suspicious dry-run recommendation. Parent metadata is not
  used as a substitute for a child's container identity.
- Unknown events remain untargeted. A deliberate reader interruption reconnects,
  with the coverage generation preserved in periodic audit records.
- A/B make fresh authenticated reads with their original credentials afterward.
  No policy record can revoke credentials or terminate a worker.
- Full Docker inspection, both worker log streams, actual raw Tetragon JSON before
  decoding, ordinary job results and safe audit records are checked privately for
  sensitive values. The raw scanner retains only a bounded 16 KiB suffix and a
  match flag; no raw capture is published. Literal and JSON-escaped forms are
  checked, not every possible transformation.
- A deliberately nonexistent immutable image fails after network attachment and
  credential issuance; its lease and owned-resource journal remain available.
  Lost delivery acknowledgement is simulated after a real stdin transfer; the
  outcome stays uncertain and cannot be resent. Cancellation and repeated cleanup
  remove only owned resources and keep the cleanup journal.
- Credentials and issuer token expire naturally before temporary bootstrap
  configuration teardown. No revoker policy/token exists in this path.

The test allows up to 70 seconds for delayed execution records. An initial
35-second window missed the child event while worker/probe events were visible;
a credential-free longer-window probe confirmed the emitted child path and
31-character container prefix. A separate two-second child binary makes the
laboratory execution predictable; it does not change attribution requirements.

### Network evidence and limits

The suite checks the internal/isolated IPv4-only network settings and restricted
container configuration. Each bridge must contain only its worker and PostgreSQL.
PostgreSQL TCP/5432 is reachable. Each worker starts an owned diagnostic listener
on TCP/9099; local probes must reach it, while probes to the other worker in both
directions must fail. Additional worker probes target OpenBao on its lab IP:8200,
external `1.1.1.1:443`, and `host.docker.internal:18200`. Each denial counts as
confirmed only if a control in PostgreSQL's network namespace reaches that same
target. An unavailable control is printed as `UNCONFIRMED`, never converted to a
passing isolation claim.

These are selected TCP/IPv4 reachability observations, not proof of a universal
firewall. Other host interfaces/services, DNS forwarding and UDP are not exhaustively
verified. The Docker daemon, host, privileged monitor and shared PostgreSQL server
remain trusted. The diagnostic helper receives no credentials.

Unit fixtures separately test strict/oversized/duplicate frames, safe worker EOF
and invalid operations, delivery ambiguity, cancellation, independent lease state,
blocked session cancellation, tainted-session reuse refusal, foreign container or
network ownership, uncertain Docker discovery, cleanup retries and admission
serialization. A fixture CLI is not a live Docker failure test. The explicit lost
acknowledgement injection does not simulate every daemon/network failure or a
controller crash. Existing Milestones 1–4 suites remain separate regressions.

### Recorded local results (2026-11-18)

The ordinary live Milestone 5 run passed, followed by a race-instrumented live
coordinator run (worker binaries remain the normal scratch-image builds). Both
verified A/B origin, exact-path policy outcomes, fresh post-policy authentication,
private credential-surface checks, failure injection, owned cleanup and natural
expiry. The final race run additionally exercised explicit stream interruption and
reconnection with retained gap coverage. No live test was skipped in these runs.

| Network check | Observed result |
|---|---|
| Worker → PostgreSQL TCP/5432 | Reachable for A and B |
| Worker A ↔ worker B TCP/9099 | Blocked in both directions; local listener controls reachable |
| Worker → OpenBao lab IP TCP/8200 | Blocked for A/B; PostgreSQL-namespace control reachable |
| Worker → `1.1.1.1:443` | Blocked for A/B; control reachable |
| Worker → `host.docker.internal:18200` | Blocked for A/B; control reachable |
| Other host services/interfaces, DNS forwarding, UDP | Not exhaustively verified |
| Privileged host/Docker daemon access | Outside the confidentiality/isolation claim |

The host port above is the tested local configuration; changing lab ports can
make that selected control unavailable, which the suite reports as unconfirmed.
The checks establish these selected boundaries, not complete network confinement.
Hosted CI for this revision still requires a push; local CI-equivalent checks do
not establish that a GitHub Actions run has executed.

Milestones 1–4 were rerun successfully using `demo.sh all`,
`test-runtime-policy.sh`, and `test-openbao-acl.sh`: selective A-only revocation,
B continuity, SQL permission denials, natural expiry, conservative runtime
correlation, reconnection, dry-run evaluation and restricted KV authorization
all passed against live services. The mock CLI also retained its expected results.
Unit tests, race tests, ordinary/tagged vet, tagged compilation, shell syntax,
YAML parsing, local documentation file links and whitespace checks passed.
Final health checks found all three project services healthy, no remaining
observation-owned containers or networks, and zero `tetra` CLI readers. Services
and the reusable local worker image were intentionally left available.

## Milestone 6A live identity validation

Run against the disposable Docker Desktop lab with cgroup v2 and Tetragon
v1.7.0. The Compose configuration enables `--enable-process-ns`.

```bash
./scripts/test-container-database.sh identity
```

Run this suite on its own so concurrent live suites do not flood the shared
observation stream. The opt-in runs the existing worker/network checks and adds namespace assurance
for two independent workers and the short-lived B helper. Each positive
assertion uses the actual structured Tetragon exec event. The full container
ID and namespace pins come from trusted Docker inspection and host procfs.
Both original PostgreSQL credentials must still work on fresh authenticated
connections inside the workers. The harness checks raw transport and sanitized
output for sensitive values, removes owned workload resources, and waits for
lease expiry before removing the temporary role setup.

Missing and malformed namespace evidence, conflicts, shared namespaces,
restart, replay, late and stale events, and coverage loss are exercised
separately with synthetic correlation tests. The live identity harness skips
in GitHub Actions. CI continues to compile integration-tag tests.

On 2026-11-19 the local Docker Desktop lab verified distinct event-time pairs
for both database workers and for B's short-lived helper. Both workers
reauthenticated afterward, and the test removed its owned resources. The
initial 30-second freshness bound withheld strong assurance for delayed helper
events. The verified run used the documented 45-second bound.

## Milestone 6B local enforcement

Run the destructive test only in the disposable loopback laboratory. Use a
private, persistent local directory to retain the operation journal.

```bash
install -d -m 700 "$HOME/.local/state/runawaybao"
./scripts/test-container-database.sh enforce "$HOME/.local/state/runawaybao/enforcement.jsonl"
```

Run it directly from an active WSL terminal. Service checks and compilation run
before the prompt. The test prints a fresh run reference and the exact
Compose project, OpenBao origin, and dedicated mount and role. Type the line
verbatim into that terminal within two minutes to authorize one run for Job B.
An expired prompt requires a fresh invocation. Bootstrap provisions a separate
restricted revoker only after confirmation. The harness independently counts
exact revocation dispatches and verifies B's original password is rejected
before expiry while A authenticates and reads on a fresh connection.
The default `./scripts/test-container-database.sh` runs the dry-run control.
GitHub Actions compiles the integration tests without executing the live
enforcement workflow.


### Live acceptance evidence

The operator ran `TestLiveControlledEnforcement` in WSL and reported PASS.
The run verified strong worker and helper attribution, B’s suspicious finding,
one acknowledged exact-lease operation, B’s failed fresh authentication before
expiry, and A’s successful fresh authentication and read. Redaction, owned
resource cleanup, and natural expiry of the remaining observation leases also
passed. Publication review confirmed the protected journal held one authorized,
dispatched and acknowledged operation with no unresolved entries.
