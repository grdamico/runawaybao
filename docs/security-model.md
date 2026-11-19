# Security model and limitations

[Back to the project overview](../README.md)

## Security model

### Privilege separation

Bootstrap and normal job access use different credentials. The test harness
provisions narrowly scoped tokens without the default policy; application clients
cannot infer an opaque token's privileges, so correct configuration remains a
trusted setup responsibility.

| Credential / role | Authority |
|---|---|
| OpenBao development root token | Test setup, inspection, teardown, and optional manual dummy-secret scripts |
| PostgreSQL administrator | Container initialization and private test-only connection-origin inspection |
| PostgreSQL management account | OpenBao plugin access; non-superuser with `CREATEROLE` and authority to grant the reader group |
| KV reader token | Read only `secret/data/demo` |
| Database issuer token | Read only the configured `<mount>/creds/reader` endpoint |
| Database revoker token | Update only exact lease paths beneath the dedicated role; only `sync=true` is permitted in the request body |
| Generated database login | Connect, use the demo schema, and select the fixture table |
| Runtime observer | Read-only event and identity metadata; no OpenBao credentials or revocation interface |

The revoker policy excludes body lease-ID overrides, prefix/force revocation,
and token-revocation endpoints. The runtime correlation harness creates no
revoker policy or token. Its credentials expire naturally before bootstrap teardown.
See the [OpenBao database API](https://openbao.org/docs/api/secret/databases/)
and [lease API](https://openbao.org/docs/api/system/leases/).

### Database and workload boundaries

[PostgreSQL initialization](../postgres/init.sh) removes public connection, temporary
object, and schema privileges in the demo database. Dynamic logins inherit the
reader group, own no objects, and cannot write, create schema objects, create
roles, or read the restricted fixture table.

Legacy observation containers run as UID/GID 65534 with no network, no capabilities,
no host binds or host namespaces, a read-only root filesystem, and
`no-new-privileges`. Limits are 64 MiB memory, 0.25 CPU, 32 PIDs, and a 16 MiB
`noexec,nosuid` temporary filesystem. The Alpine version tag resolves to an
immutable local image ID before creation. Legacy workloads receive no Docker socket, OpenBao tokens, or database passwords.
Milestone 5 workers receive only their own database login through attached stdin;
they receive no Docker socket or OpenBao token.

The controller and Docker daemon are trusted. Docker access grants extensive
host authority; the observer's read-only behavior is a code boundary, not an
OS-enforced restriction on Docker privileges. Tetragon separately runs privileged
with host PID/cgroup namespaces and kernel/BPF/runtime mounts. Compose project
isolation does not isolate its host-wide instrumentation.

### Attribution and sensitive data

Fail-closed correlation means **refusing uncertain attribution**, not blocking
workload execution or revoking access. Prefix resolution depends on trusted
Docker inventory and retained history; it is not full-identity proof.

Application errors and job results omit credentials, connection strings, and raw
sensitive diagnostics. Observer output also omits lease IDs and raw event payloads.
Use non-sensitive job IDs: they are echoed in results. Keep registry metadata,
configuration structs, and returned credentials out of ordinary logs. Go strings
cannot be reliably erased from memory. The database job manager retains expired
credential values until its in-memory instance is discarded; TTL alone neither
confirms cleanup nor clears credentials.

## Reliability and limitations

- **Volatile state:** job IDs, credentials, leases, and bindings are tracked in
  memory. There is no durable audit store, restart recovery, or reconciliation.
  Runner/manager history grows for the instance lifetime.
- **Lease lifecycle:** no automatic renewal or application-level issuance retry. A lost issuance
  response may leave an unknown lease until expiry. After OpenBao dev-mode state
  loss, acknowledging an absent lease cannot prove PostgreSQL cleanup.
- **Authentication versus sessions:** revocation and password expiry are tested
  against fresh authentication. Existing PostgreSQL sessions are not necessarily
  terminated; the legacy client closes each connection. The new worker holds its
  own session until the next read, finish, cancellation/EOF, or lifetime deadline.
- **Observation gaps:** reconnects do not replay historical Tetragon events. Queue
  overflow, malformed frames, reported source loss, and interruptions record gaps;
  silent upstream loss may still occur. No event is not proof of no activity.
- **Bounded correlation:** limits include 1 MiB frames, 256 queued notices, 64 recent
  gap records, 64 bindings, 4096 deduplication entries, and 256 containers per
  inventory. Deduplication/retired history lasts two minutes. Capacity exhaustion
  refuses admission/attribution; eviction limits historical coverage.
- **Prefix identity:** uniqueness depends on current Docker inventory and retained
  bindings, not a complete history of unrelated removed containers. An adversarial
  Docker administrator is outside the trust model. Stronger identity assurance
  is required for the opt-in enforcement path. See the namespace identity
  assumptions below.
- **Lineage and clocks:** immediate parent metadata was incomplete in this
  environment. Lifecycle checks use `process.start_time`, not event envelope time
  or WSL receipt time. Startup events before binding activation stay uncorrelated.
- **Stream lifecycle:** a remote 180-second reader timeout bounds orphan lifetime
  after connectivity failures and introduces periodic reconnect gaps in longer
  sessions. Context cancellation reaps the local reader.
- **Execution scope:** Milestone 5 runs fixed database reads inside monitored
  containers; legacy demonstrations retain controller reads. There is no arbitrary
  workload API, production
  scheduling, persistence, retries, or authenticated submission endpoint.
- **Response scope:** no general threat detection or automatic event-driven revocation.
  Exact-path policy evaluation is available only as an explicit dry-run option. Explicit revocation and ordinary failure cleanup exist
  independently of the observer.
- **Local development only:** OpenBao dev mode, placeholder bootstrap credentials,
  privileged monitoring, and plaintext local database connections are unsuitable
  for production. The legacy PostgreSQL wrapper accepts loopback IP addresses only; the worker
  has a fixed internal PostgreSQL endpoint.

For Tetragon's supported event interfaces, see its
[events documentation](https://tetragon.io/docs/concepts/events/).


## Dry-run policy boundary

The evaluator is pure and receives no I/O or enforcement interfaces. The adapter
has no OpenBao clients/tokens or Docker lifecycle object. Recommendations are
strings in sanitized records, never executable actions. Policy versions are copied
and bound immutably by trusted admission before a live workload starts.

Uncorrelated, ambiguous, historical, incomplete, or currently unobservable inputs
cannot produce targeted hypothetical revocations. Known past gaps remain visible
when event-local evaluation resumes. Descriptor references such as `/proc/self/fd/6`
are unknown; path-only policies cannot establish their underlying binary identity.
This deliberate uncertainty is not a safe verdict and can conceal behavior outside
the scope of an exact-path policy. Read [policy semantics and limitations](policy.md).

## Milestone 5 credential channel and networking

Only the trusted coordinator handles issuer credentials, private database logins,
and job/lease records. Workers receive neither issuer/revoker tokens nor lease IDs.
The observer receives read-only metadata; neither it nor the pure evaluator has a
revocation or workload termination interface. Lifecycle cleanup never consumes a
policy decision. The new live harness creates no revoker policy or token.

Attached stdin avoids persistence in Docker configuration, environment, arguments,
files and images. Protocol errors are static. Full inspection and both worker log
streams are checked privately, as are raw Tetragon transport bytes, ordinary job
results and sanitized audits. This checks those actual surfaces and credential
values, not arbitrary encodings, future sensor policies or all possible leaks.
The trusted host, Docker daemon, controller memory and a privileged monitor can
observe pipe traffic or workload memory. PostgreSQL traffic is plaintext in this
local lab. There is no secure-memory-erasure guarantee.

Each job owns a separate IPv4-only internal bridge with isolated gateway mode,
containing only that worker and PostgreSQL. This requires a supporting Docker
Engine; unsupported configurations fail rather than fall back to a regular
internal bridge with host bridge reachability. PostgreSQL remains dual/multihomed
on the lab network for OpenBao management, so it is a trusted shared service,
not a network security boundary against PostgreSQL compromise.

Live probes must use positive controls: PostgreSQL is reachable, peer listeners
are locally reachable, and OpenBao/external/host targets must be reachable from
a control namespace before their denied worker probes count as confirmed.
An internal bridge is not a universal firewall. Tests cover selected IPv4 ports
and host endpoints, not every host address/service, DNS forwarding/exfiltration,
UDP or a hostile Docker administrator. See [testing](testing.md) for measured
results; unavailable controls are reported as unconfirmed.

Ownership records survive ambiguous Docker mutations and failed cleanup in memory.
A controller crash loses these journals; time-bounded workers can exit while stopped
containers, networks or PostgreSQL attachments remain. Operators must use retained
exact resource IDs/names and ownership labels for manual recovery, never broad
pruning. Lost issuance responses can still leave unknown leases until expiry.

## Milestone 6A workload identity assumptions

Strong assurance trusts the Linux kernel, Docker daemon, Tetragon service and
controller. The lab admits only controller-owned, restricted workers with
private PID and cgroup namespaces, no added capabilities and no worker access to
the Docker API. A trusted Docker administrator or privileged host process can
alter these conditions. A namespace inode alone is insufficient. The controller
pins both kernel namespace objects through a bounded stdin-owned process in the
trusted Tetragon container and binds them to the inspected Docker generation
and the supported cgroup v2 path. The descriptors are held only for the current
controller run. Controller exit, transport loss, cleanup and the holder timeout
bound their lifetime.

An event can carry a matching Docker prefix during runtime setup while
reporting host namespaces. Such events retain their ordinary observation result
and cannot receive strong namespace assurance. A change in generation, stale or
late delivery, observation gap, or missing or conflicting evidence also rules
out strong assurance. Unknown layouts are unsupported in strong mode. This
laboratory check establishes workload identity only. It makes no assertion
about binary integrity, intent, complete observation, or production readiness.

## Controlled enforcement and recovery

The opt-in coordinator revalidates the original event under the immutable
policy and its content digest. It requires the same observing consumer, a
fresh complete Docker inventory, the pinned execution identity, and the exact
lease from the private issuance registry. Its lock order is the per-execution
lifecycle reservation, the private manager entry reservation, then the local
journal. Docker and OpenBao calls run outside global mutexes. A worker can exit
and a lease can expire after inspection. The dispatch target stays bound to
the original immutable lease in either race.

The journal contains the exact lease and opaque execution and operation
references. Store it on a local filesystem supporting file and directory
synchronization and advisory locks. The parent directory must be owner-only
and the journal must be a regular owner-only file. Keep the same journal
during recovery. A torn or unreadable journal blocks startup. An intent
without an acknowledgement, or a recorded uncertain outcome, requires manual
comparison with trusted OpenBao and database evidence. Retain the original
record and document reconciliation separately. Recovery never resends the
recorded request. A local journal cannot guarantee exactly-once remote
execution.

An OpenBao acknowledgement and a fresh PostgreSQL authentication check are
separate audit stages. Existing database sessions can outlive revocation.


Milestone 6B was verified in one explicitly authorized Docker Desktop cgroup v2
lab scenario. Trust includes the kernel, Docker daemon, privileged Tetragon
agent, controller, and local OpenBao and PostgreSQL services. Coverage checks
can withhold dispatch after detected gaps. Silent upstream loss can still
limit what the controller sees. Journal recovery is manual and retaining the
journal is part of the laboratory procedure.
