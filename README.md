# 🐉 RunawayBao

**Jobs run. Secrets shouldn't.**

RunawayBao is a hands-on security engineering project exploring how to execute containerized jobs while controlling access to secrets and monitoring runtime behavior.

It brings together **Docker**, **OpenBao**, and **Tetragon** to explore a central question:

> How can we run a job without automatically trusting everything it does?

## 🎯 The idea

Automated jobs often need credentials to access APIs, databases, or other services. But granting a job access to a secret doesn't mean its behavior should be trusted indefinitely.

A vulnerable script, unexpected command, or compromised workload can turn legitimate access into a security risk.

RunawayBao explores how to address this problem by combining:

- **Secret management** — control how workloads access sensitive values.
- **Runtime visibility** — observe process execution and selected system behaviors.
- **Policy enforcement** — identify unexpected behavior and define appropriate responses.
- **Auditability** — record job outcomes and explain security decisions.

The long-term goal is to build a small, understandable prototype that connects these capabilities into a coherent execution workflow.

## 🧩 Technologies

| Technology | Role |
|---|---|
| [Docker](https://docs.docker.com/) | Runs the lab components and containerized workloads. |
| [OpenBao](https://openbao.org/) | Manages secrets and provides an API for controlled access. |
| [Tetragon](https://tetragon.io/) | Observes runtime activity using eBPF-based security instrumentation. |
| [Go](https://go.dev/) | Planned language for the job runner and security logic. |

## 🏗️ Current architecture

```text
                  RunawayBao
                      |
             +--------+--------+
             |                 |
        OpenBao API         Tetragon
             |                 |
       Secret management   Process events
             |                 |
             +--------+--------+
                      |
             Future integration
                      |
          Job policy and audit logic
```

This is the intended direction, not a claim that all components are already integrated.

## 🚧 Project status

RunawayBao is an early-stage experiment. The first milestone is to establish a reproducible local environment and validate the individual components before connecting them.

### Implemented

- Docker Compose configuration.
- OpenBao running in development mode.
- Tetragon running in the local lab.
- Shell scripts to start, inspect, and stop the environment.
- A process-observation test using an Alpine container.

### Planned

- A small Go-based job runner.
- Mock jobs and deterministic policy tests.
- Controlled access to a demo secret through OpenBao.
- Correlation of Tetragon events with individual jobs.
- An audit trail for job execution and security decisions.
- Carefully scoped responses to policy violations.

## 🚀 Getting started

### Prerequisites

- Docker Engine or Docker Desktop with Docker Compose.
- `curl`.
- Bash.
- A Linux environment with the kernel capabilities required by Tetragon.

The current setup was developed in Ubuntu on WSL2 with Docker Desktop. Tetragon requires privileged runtime access and compatible kernel features, so other environments may need adjustments.

### Start the lab

Clone the repository:

```bash
git clone https://github.com/grdamico/runawaybao.git
cd runawaybao
```

Start the services:

```bash
./scripts/start.sh
```

Check their status:

```bash
./scripts/status.sh
```

OpenBao's local API is exposed at:

```text
http://127.0.0.1:8200
```

### Observe process execution

In one terminal, start the event listener:

```bash
./scripts/test-process.sh
```

In a second terminal, launch a test workload:

```bash
docker run --rm alpine:3.22 sh -c 'sleep 10'
```

The listener should display process events associated with the test. Press `Ctrl+C` to stop listening.

### Stop the lab

```bash
./scripts/stop.sh
```

## 🔐 Security notes

The current environment uses OpenBao development mode and a demo token. It is intended for local experimentation only.

- Never store real credentials in the repository.
- Keep `.env` out of version control.
- Do not expose the development API or token to untrusted networks.
- Do not use development mode for production secrets.
- Tetragon's privileged access should be treated as security-sensitive.
- Runtime observation alone does not enforce a policy or prove that a process is malicious.

Production-grade authentication, workload isolation, policy enforcement, and response mechanisms are outside the current implementation.

## 🧪 Engineering approach

The project will evolve incrementally:

1. Establish a reproducible environment.
2. Build a mock job runner and test its behavior.
3. Introduce secret access through OpenBao.
4. Integrate runtime observations from Tetragon.
5. Connect job identity, policy decisions, and audit records.
6. Explore safe responses to unexpected behavior.

Mocks will let us test the security logic deterministically before introducing the complexity of real runtime integrations.

## 🐉 Project principle

**A job being allowed to start doesn't mean everything it does should be allowed.**

RunawayBao is an exploration of that boundary: controlled secret access, observable execution, and explicit security decisions.

---

*An independent learning and engineering project. Not a production security product.*