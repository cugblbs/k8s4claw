# Changelog

All notable changes to k8s4claw will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.1.0] - 2026-03-29

Initial release of k8s4claw — Kubernetes operator for managing heterogeneous AI agent runtimes.

### Operator Core

- **Claw CRD** — declarative resource for AI agent instances with runtime type, config, credentials, channels, persistence, observability, security, ingress, and availability
- **ClawChannel CRD** — declarative resource for external communication channels with deletion protection and reference counting
- **ClawSelfConfig CRD** — agent self-configuration with allowlist validation, TTL cleanup, and owner-reference lifecycle
- **Runtime adapters** — extensible `RuntimeAdapter` interface with 5 built-in runtimes:
  - **OpenClaw** — full-featured AI assistant platform (TypeScript/Node.js)
  - **NanoClaw** — lightweight secure personal assistant (TypeScript/Node.js)
  - **ZeroClaw** — high-performance agent runtime (Rust)
  - **PicoClaw** — ultra-minimal serverless agent runtime
  - **IronClaw** — security/privacy-focused AI assistant (Rust, WASM sandbox)
- **Admission webhooks** — validating (credential requirements, runtime immutability, PVC size/storage class constraints, auto-update field validation) and defaulting (reclaim policy, observability defaults)
- **PodBuilder** — generates PodTemplateSpec with init container, runtime container, probes, security context, volumes, and credential injection
- **StatefulSet management** — reconciles StatefulSet with PVC templates, rolling updates on config/credential changes
- **Service** — headless Service per Claw instance
- **ConfigMap** — runtime config with deep-merge, overwrite, and passthrough modes
- **ServiceAccount** — per-instance restricted SA with optional user-managed override
- **Credentials** — Secret-based injection with hash annotation for rolling updates; supports secretRef, externalSecret, and per-key mappings

### Persistence & Storage

- **PVC lifecycle** — session, output, and workspace volumes via StatefulSet volumeClaimTemplates
- **Reclaim policies** — Retain (orphan PVCs), Delete (cascade via ownerReference), Archive (placeholder with status condition)
- **CSI VolumeSnapshot** — cron-scheduled snapshots with configurable retention and automatic pruning
- **S3 archival sidecar** — output archival to S3-compatible storage (S3, MinIO, GCS, R2) with inotify trigger, gzip compression, and local retention

### IPC Bus

- **Native sidecar** — deployed as init container with `restartPolicy: Always`
- **Message router** — routes JSON messages between channel sidecars and runtime via UDS (`/var/run/claw/bus.sock`)
- **Bridge protocols** — WebSocket (OpenClaw), TCP with length-prefix framing (PicoClaw), UDS with length-prefix framing (NanoClaw), SSE with HTTP POST (ZeroClaw)
- **WAL** — append-only write-ahead log on emptyDir for at-least-once delivery with recovery and compaction
- **DLQ** — BoltDB dead letter queue for messages exceeding retry limits
- **Backpressure** — ring buffer with configurable high/low watermark flow control
- **Prometheus metrics** — message counters, latency histograms, WAL/DLQ gauges, backpressure state

### Channel Sidecars

- **Slack** — Socket Mode + Web API bridging with bidirectional message flow
- **Webhook** — HTTP inbound/outbound bridging with signature verification

### Auto-Update Controller

- **OCI registry polling** — cron-scheduled tag listing with semver constraint filtering
- **Health-verified rollouts** — configurable timeout with StatefulSet readiness checks
- **Automatic rollback** — reverts to previous version on health check failure
- **Circuit breaker** — opens after N consecutive rollbacks, blocks further updates
- **Version history** — tracks successful and rolled-back versions with capped history

### Networking & Security

- **NetworkPolicy** — per-instance default-deny with configurable egress CIDRs and ingress namespaces
- **Ingress** — optional HTTP access with TLS termination, Basic Auth, and custom annotations
- **PodDisruptionBudget** — configurable minAvailable with default-on behavior
- **Security context** — non-root (UID 1000), read-only root filesystem, all capabilities dropped, seccomp runtime default

### Observability

- **Prometheus metrics** — reconcile duration, error counts, auto-update checks/results, circuit breaker state
- **Kubernetes Events** — lifecycle transitions (Created, Running, Degraded, Failed), auto-update events, self-config applied/denied
- **Status conditions** — Ready, AutoUpdateAvailable, AutoUpdateInProgress with human-readable messages
- **ServiceMonitor + PrometheusRule** — optional manifests for Prometheus Operator integration

### Go SDK

- **CRUD client** — Create, Get, Update, Delete Claw resources via dynamic client
- **Channel SDK** — length-prefix framed UDS client with bus-down buffering, backpressure handling, and functional options

### Helm Chart

- **Full operator deployment** — Deployment, ServiceAccount, ClusterRole, ClusterRoleBinding, CRDs
- **Webhook configuration** — cert-manager integration with self-signed fallback
- **Observability** — optional ServiceMonitor and PrometheusRule
- **Helm test** — webhook service DNS resolution check

### Infrastructure

- **CI** — GitHub Actions with lint, vet, test (envtest), and CodeQL
- **Test coverage** — 80%+ across all packages
- **Issue templates** — bug report, feature request, good first issue
- **Contributing guide** — step-by-step runtime addition guide

[0.1.0]: https://github.com/Prismer-AI/k8s4claw/releases/tag/v0.1.0
