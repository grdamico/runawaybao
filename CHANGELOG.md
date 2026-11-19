# Changelog

This file records notable changes by release. New entries should describe
user-visible behavior and identify the scope of verification.

## [0.1.0](docs/releases/v0.1.0.md)

First laboratory preview for a disposable Docker Desktop cgroup v2 environment.

### Added

- **M1** Go job authorization and restricted OpenBao KV reads.
- **M2** Per-job dynamic PostgreSQL credentials and exact-lease revocation.
- **M3** Structured Tetragon process-event ingestion, conservative correlation,
  deduplication, and observation-gap reporting.
- **M4** Deterministic executable-path allowlists and sanitized dry-run findings.
- **M5** Restricted, monitored PostgreSQL workers with isolated networks, bounded
  attached-stdin credential delivery, and owned-resource cleanup.
- **M6A** Strong workload identity using event-time PID and cgroup namespaces,
  pinned namespace objects, and trusted Docker execution-generation checks.
- **M6B** Default-disabled, operator-confirmed exact-lease enforcement with a
  protected dispatch journal and independent PostgreSQL authentication checks.

### Verified

- The local A/B enforcement run correlated B’s helper, recorded a suspicious
  finding, and received OpenBao acknowledgement for one synchronous request
  targeting B’s original lease. A fresh PostgreSQL connection rejected B before
  natural expiry. A authenticated and completed the fixed read.
- Separate dry-run validation preserved both credentials. The enforcement run
  also verified redaction, owned-resource cleanup, and natural expiry of the
  remaining observation leases.

See the [security model](docs/security-model.md) for trust assumptions and
operational limits.
