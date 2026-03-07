# Auto-Update Controller Design

## Goal

Add an auto-update controller that monitors OCI registries for new runtime image tags, applies updates with health verification, and rolls back on failure with circuit-breaker protection.

## Architecture

A dedicated `AutoUpdateReconciler` watches Claw resources where `spec.autoUpdate.enabled=true`. It runs on a cron schedule (default `0 3 * * *`), queries the OCI registry for new tags, filters by semver constraint, and drives a state machine: Idle → Updating → HealthCheck → (Running | Rollback).

The auto-update controller does NOT own the StatefulSet. It patches the Claw CR's `status.autoUpdate` and sets an annotation `claw.prismer.ai/target-image` to signal the desired image tag. The existing `ClawReconciler` reads this annotation to override the adapter's default image during `ensureStatefulSet()`.

## MVP Scope

| Included | Excluded (future) |
|----------|-------------------|
| OCI registry tag resolver (Docker V2 API, anonymous auth, TTL cache) | Pre-backup (VolumeSnapshot/S3) |
| Semver constraint filtering (`~1.x`, `^2.0.0`) | Archive policy integration |
| Update state machine (update → health check → rollback) | Multi-replica coordination |
| Circuit breaker (max rollbacks → circuit open) | |
| Failed version tracking | |
| Status reporting + conditions + events + metrics | |
| Digest-pinned image skip | |

## CRD Changes

### Spec

```go
type AutoUpdateSpec struct {
    Enabled           bool   `json:"enabled"`
    VersionConstraint string `json:"versionConstraint,omitempty"` // e.g. "~1.x", "^2.0.0"
    Schedule          string `json:"schedule,omitempty"`           // cron expression, default "0 3 * * *"
    HealthTimeout     string `json:"healthTimeout,omitempty"`      // duration, default "10m", range 2m-30m
    MaxRollbacks      int    `json:"maxRollbacks,omitempty"`       // circuit breaker threshold, default 3
}
```

### Status

```go
type AutoUpdateStatus struct {
    CurrentVersion   string                `json:"currentVersion,omitempty"`
    AvailableVersion string                `json:"availableVersion,omitempty"`
    LastCheck        *metav1.Time          `json:"lastCheck,omitempty"`
    LastUpdate       *metav1.Time          `json:"lastUpdate,omitempty"`
    RollbackCount    int                   `json:"rollbackCount,omitempty"`
    FailedVersions   []string              `json:"failedVersions,omitempty"`
    CircuitOpen      bool                  `json:"circuitOpen,omitempty"`
    VersionHistory   []VersionHistoryEntry `json:"versionHistory,omitempty"`
}

type VersionHistoryEntry struct {
    Version   string      `json:"version"`
    AppliedAt metav1.Time `json:"appliedAt"`
    Status    string      `json:"status"` // "Healthy" or "RolledBack"
}
```

## Coordination with ClawReconciler

The auto-update controller sets:
1. `status.autoUpdate.availableVersion` — the resolved new version
2. Annotation `claw.prismer.ai/target-image` — the full image ref with new tag

The `ClawReconciler.ensureStatefulSet()` checks for this annotation. If present, it uses the annotation value as the container image instead of the adapter's hardcoded default. This avoids two controllers competing over the StatefulSet.

When a rollback is needed, the auto-update controller removes the annotation and the ClawReconciler reverts to the adapter's default image (or the previously known-good version stored in `status.autoUpdate.currentVersion`).

## OCI Registry Resolver

New package `internal/registry/`:

- `TagLister` interface: `ListTags(ctx, image) ([]string, error)` — for testability
- `RegistryClient`: implements Docker Registry V2 tag listing
  - Anonymous token exchange (required by GHCR: `GET /token?scope=repository:...`)
  - HTTP client with timeout
- Semver filtering: parse tags as semver, filter against constraint
- TTL cache: in-memory with 15-minute default TTL, keyed by image reference

### Registry Protocol

```
1. GET https://ghcr.io/token?scope=repository:prismer-ai/k8s4claw-openclaw:pull → {"token": "..."}
2. GET https://ghcr.io/v2/prismer-ai/k8s4claw-openclaw/tags/list
   Authorization: Bearer <token>
   → {"tags": ["1.0.0", "1.1.0", "1.2.0", "latest"]}
```

## State Machine

```
Idle ──(cron fires)──> CheckVersion
                           │
                    (no new version)──> Idle (requeue at next cron)
                           │
                    (new version found)
                           │
                    (circuit open?)──yes──> Idle (emit warning event)
                           │
                           no
                           │
                      Updating ──> set target-image annotation
                           │
                    ClawReconciler updates StatefulSet
                           │
                      HealthCheck ──(Pod ready within healthTimeout?)
                           │                    │
                          yes                   no
                           │                    │
                      Running              Rollback
                      (record version       (remove annotation,
                       in history,           add to failedVersions,
                       update current)       rollbackCount++,
                                             check circuit breaker)
```

### Reconcile Loop Details

Each reconcile:
1. Fetch Claw, check `spec.autoUpdate.enabled`
2. If not enabled or digest-pinned image → skip
3. Parse cron schedule, determine if check is due (compare `status.autoUpdate.lastCheck`)
4. If check is due:
   a. Query registry for tags (via TagLister)
   b. Filter by semver constraint, exclude `failedVersions`
   c. If newer version found: set `availableVersion`, set condition `AutoUpdateAvailable=True`
   d. If circuit is open → emit warning event, requeue at next cron
   e. Set `target-image` annotation, set phase to `Updating`, record `lastUpdate`
5. If currently Updating:
   a. Check StatefulSet pod readiness
   b. Pod ready → record version in history as Healthy, clear annotation phase, set `currentVersion`
   c. Health timeout exceeded → trigger rollback
6. Requeue: at next cron tick or after short interval during health check

## Events

| Event | Type | When |
|-------|------|------|
| `AutoUpdateAvailable` | Normal | New version found |
| `AutoUpdateStarting` | Normal | Update initiated |
| `AutoUpdateComplete` | Normal | Health check passed |
| `AutoUpdateRollback` | Warning | Health check failed, reverting |
| `AutoUpdateCircuitOpen` | Warning | Max rollbacks reached |

## Metrics

| Metric | Type | Labels |
|--------|------|--------|
| `claw_autoupdate_checks_total` | Counter | namespace |
| `claw_autoupdate_updates_total` | Counter | namespace, result (success/rollback) |
| `claw_autoupdate_circuit_open` | Gauge | namespace, instance |

## Webhook Validation

Add to `ClawValidator`:
- `versionConstraint` must be valid semver constraint (if non-empty)
- `schedule` must be valid cron expression (if non-empty)
- `healthTimeout` must parse as duration in range [2m, 30m]
- `maxRollbacks` must be > 0
- Reject `autoUpdate.enabled=true` when image uses digest pinning (`@sha256:...`)

## Files

### New

| File | Purpose |
|------|---------|
| `internal/registry/resolver.go` | OCI tag listing + semver filtering + TTL cache |
| `internal/registry/resolver_test.go` | Tests with httptest mock registry |
| `internal/controller/autoupdate_controller.go` | State machine reconciler |
| `internal/controller/autoupdate_controller_test.go` | Unit tests |

### Modified

| File | Change |
|------|--------|
| `api/v1alpha1/claw_types.go` | Add `AutoUpdate *AutoUpdateSpec` to ClawSpec, `AutoUpdate *AutoUpdateStatus` to ClawStatus |
| `cmd/operator/main.go` | Wire `AutoUpdateReconciler` into manager |
| `internal/controller/claw_controller.go` | Read `target-image` annotation to override image in `ensureStatefulSet` |
| `internal/controller/events.go` | Add auto-update event constants |
| `internal/controller/metrics.go` | Add auto-update metrics |
| `internal/webhook/claw_validator.go` | Validate auto-update fields |

## Dependencies

- `github.com/Masterminds/semver/v3` — semver constraint parsing (widely used, battle-tested)
- No other new dependencies needed; HTTP client from stdlib suffices for registry API
