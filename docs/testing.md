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
IDs and raw event payloads. The complete two-phase orchestration currently lives
in the integration harness, not this CLI.

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
test selection executes no test bodies, and all five live opt-ins are explicitly
set to `0`. No repository secrets, `.env`, Docker services, privileged workloads,
or eBPF setup are used by this workflow.

The workflow needs its first hosted run after push to establish GitHub runner
verification. Local execution of the same Go commands validates the code paths,
not GitHub's action orchestration. Live demonstrations stay opt-in outside CI;
see the [presentation and recording guide](demo.md).
