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
| PostgreSQL administrator | Container initialization |
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

Observation containers run as UID/GID 65534 with no network, no capabilities,
no host binds or host namespaces, a read-only root filesystem, and
`no-new-privileges`. Limits are 64 MiB memory, 0.25 CPU, 32 PIDs, and a 16 MiB
`noexec,nosuid` temporary filesystem. The Alpine version tag resolves to an
immutable local image ID before creation. Workloads receive no Docker socket,
OpenBao tokens, or database passwords.

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
cannot be reliably erased from memory.

## Reliability and limitations

- **Volatile state:** job IDs, credentials, leases, and bindings are tracked in
  memory. There is no durable audit store, restart recovery, or reconciliation.
  Runner/manager history grows for the instance lifetime.
- **Lease lifecycle:** no automatic renewal or application-level issuance retry. A lost issuance
  response may leave an unknown lease until expiry. After OpenBao dev-mode state
  loss, acknowledging an absent lease cannot prove PostgreSQL cleanup.
- **Authentication versus sessions:** revocation and password expiry are tested
  against fresh authentication. Existing PostgreSQL sessions are not necessarily
  terminated; this client closes its own connection after each operation.
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
  is needed before using these results as an enforcement foundation.
- **Lineage and clocks:** immediate parent metadata was incomplete in this
  environment. Lifecycle checks use `process.start_time`, not event envelope time
  or WSL receipt time. Startup events before binding activation stay uncorrelated.
- **Stream lifecycle:** a remote 180-second reader timeout bounds orphan lifetime
  after connectivity failures and introduces periodic reconnect gaps in longer
  sessions. Context cancellation reaps the local reader.
- **Execution scope:** database work stays in the controller. Containers perform
  a fixed observation command; there is no arbitrary workload API, production
  scheduling, persistence, retries, or authenticated submission endpoint.
- **Response scope:** no general threat detection or automatic event-driven revocation.
  Exact-path policy evaluation is available only as an explicit dry-run option. Explicit revocation and ordinary failure cleanup exist
  independently of the observer.
- **Local development only:** OpenBao dev mode, placeholder bootstrap credentials,
  privileged monitoring, and plaintext local database connections are unsuitable
  for production. The PostgreSQL client accepts loopback IP addresses only.

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
