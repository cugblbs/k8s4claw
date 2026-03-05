# Phase 5B + 6B: SDK, Observability & ClawSelfConfig Design

## Scope

**Phase 5 (core subset):** Go SDK (dynamic client), Operator Prometheus metrics + ServiceMonitor + PrometheusRule, K8s Events.

**Phase 6 (ClawSelfConfig only):** CRD, reconciler with allowlist validation, TTL cleanup, RBAC updates. Auto-Update is deferred.

These two phases are independent and can be implemented in parallel.

---

## 1. Go SDK (`sdk/`)

### 1.1 Design

The SDK provides a lightweight Go client for managing Claw instances programmatically. It uses `client-go` dynamic client with `unstructured.Unstructured` — consumers do NOT need controller-runtime.

```go
client, _ := sdk.NewClient()                          // kubeconfig or in-cluster
instance, _ := client.Create(ctx, &sdk.ClawSpec{...})  // creates Claw CR
_ = client.WaitForReady(ctx, instance)                 // watches until Phase=Running
instances, _ := client.List(ctx, "default")            // lists all Claws
_ = client.Update(ctx, instance, &sdk.UpdateSpec{...}) // patches Claw CR
_ = client.Delete(ctx, "default", "my-agent")          // deletes Claw CR
```

### 1.2 Types

Existing `sdk/types.go` types are kept and extended:

- `ClawSpec` — add `Name` (optional, auto-generated if empty), `Labels`, `Replicas`
- `ClawInstance` — add `Runtime`, `Conditions []Condition`, `CreatedAt time.Time`
- `Condition` — `Type`, `Status`, `Message`, `LastTransitionTime`
- `UpdateSpec` — partial update: `Environment`, `Replicas`
- `ListOptions` — `LabelSelector`, `Limit`

### 1.3 Internal conversion

```
sdk.ClawSpec → unstructured.Unstructured (GVR: claw.prismer.ai/v1alpha1/claws)
unstructured.Unstructured → sdk.ClawInstance
```

Helper functions `toUnstructured(spec)` and `fromUnstructured(obj)` handle the mapping.

### 1.4 Client construction

```go
func NewClient(opts ...Option) (*Client, error)

func WithKubeconfig(path string) Option
func WithNamespace(ns string) Option
```

- Default: uses `~/.kube/config` or in-cluster config
- `WithNamespace` sets a default namespace (overridable per call)

### 1.5 WaitForReady

Uses K8s Watch API (not polling) on the Claw CR. Returns when `status.phase == "Running"` or context expires.

---

## 2. Operator Prometheus Metrics

### 2.1 Metrics

Defined in `internal/controller/metrics.go`:

| Metric | Type | Labels | Description |
| --- | --- | --- | --- |
| `claw_reconcile_total` | Counter | `namespace`, `result` | Reconcile invocations (success/error) |
| `claw_reconcile_duration_seconds` | Histogram | `namespace` | Reconcile latency |
| `claw_managed_instances` | Gauge | — | Total managed Claw instances |
| `claw_instance_phase` | Gauge | `namespace`, `instance`, `phase` | Per-instance phase (1=active) |
| `claw_instance_ready` | Gauge | `namespace`, `instance` | Pod readiness (1/0) |
| `claw_resource_creation_failures_total` | Counter | `namespace`, `resource` | Sub-resource creation failures |

Note: `claw_instance_info`, `claw_autoupdate_*` metrics are deferred (Auto-Update not in scope).

### 2.2 Integration

Metrics are registered via `promauto` (same pattern as IPC Bus metrics). The reconciler calls helper functions at key points:

- `RecordReconcile(namespace, result, duration)` — at end of Reconcile
- `SetInstancePhase(namespace, instance, phase)` — on phase transition
- `SetInstanceReady(namespace, instance, ready)` — on readiness change
- `SetManagedInstances(count)` — after list

### 2.3 ServiceMonitor

`config/prometheus/servicemonitor.yaml` — targets the operator's `/metrics` endpoint. Created as a static manifest (not operator-managed), applied via `make deploy`.

### 2.4 PrometheusRule

`config/prometheus/prometheusrule.yaml` — 8 production alerts (excluding Auto-Update alerts):

| Alert | Condition | Severity | For |
| --- | --- | --- | --- |
| `ClawReconcileErrors` | Reconcile error rate > 0 in 5m | Warning | 5m |
| `ClawInstanceDegraded` | Phase = Failed or Degraded | Critical | 2m |
| `ClawSlowReconciliation` | P99 reconcile > 30s | Warning | 10m |
| `ClawPodCrashLooping` | Container restarts > 2 in 10m | Critical | 0m |
| `ClawPodOOMKilled` | OOM kill detected | Warning | 0m |
| `ClawPVCNearlyFull` | PVC usage > 85% | Warning | 5m |
| `ClawDLQBacklog` | Dead letter count > 100 | Warning | 5m |
| `ClawChannelDisconnected` | Channel disconnected > 5m | Warning | 5m |

---

## 3. K8s Events

### 3.1 Event List

Emitted via controller-runtime `record.EventRecorder`:

| Event Type | Reason | When |
| --- | --- | --- |
| Normal | `ClawProvisioning` | Instance entering provisioning |
| Normal | `ClawRunning` | Instance reached Running phase |
| Normal | `ResourceCreated` | Sub-resource created/updated |
| Normal | `SecretRotated` | Secret hash changed, rolling update |
| Normal | `SelfConfigApplied` | Agent self-configuration applied |
| Warning | `ClawDegraded` | Instance entered degraded state |
| Warning | `ReconcileError` | Reconcile returned error |
| Warning | `SelfConfigDenied` | Unauthorized self-configuration |
| Warning | `DLQExhausted` | Message exhausted retries |

### 3.2 Integration

Add `EventRecorder` to the reconciler struct. Call `r.recorder.Eventf(claw, corev1.EventTypeNormal, "ClawRunning", "...")` at relevant points in the reconcile loop.

---

## 4. ClawSelfConfig

### 4.1 CRD

```yaml
apiVersion: claw.prismer.ai/v1alpha1
kind: ClawSelfConfig
metadata:
  name: my-agent-install-skill
spec:
  clawRef: my-research-agent
  addSkills:
    - "@anthropic/tool-use"
  removeSkills: []
  configPatch:
    model: "claude-sonnet-4"
  addWorkspaceFiles:
    my-prompt.md: "You are a research assistant..."
  removeWorkspaceFiles: []
  addEnvVars:
    - name: CUSTOM_FLAG
      value: "true"
  removeEnvVars: []
status:
  phase: Pending  # Pending | Applied | Failed | Denied
  message: ""
  appliedAt: null
```

### 4.2 Reconciler

`internal/controller/selfconfig_controller.go`:

1. **Validate** `clawRef` points to existing Claw in same namespace
2. **Check** `spec.selfConfigure.enabled == true` on parent Claw
3. **Allowlist** — each action category (`skills`, `config`, `workspaceFiles`, `envVars`) must be in `spec.selfConfigure.allowedActions`
4. **Deny** unauthorized actions → set `Phase=Denied` + emit `SelfConfigDenied` Warning Event
5. **Apply** — optimistic update of Claw CR spec (with conflict retry)
6. **Set** `Phase=Applied`, `appliedAt=now()`, ownerReference to parent Claw
7. **TTL** — requeue after 1h, delete Applied SelfConfig resources

### 4.3 CRD Fields on Claw

The `Claw` CRD needs these fields (under `spec`):

```go
SelfConfigure *SelfConfigureSpec `json:"selfConfigure,omitempty"`

type SelfConfigureSpec struct {
    Enabled        bool     `json:"enabled"`
    AllowedActions []string `json:"allowedActions,omitempty"` // "skills", "config", "workspaceFiles", "envVars"
}
```

### 4.4 RBAC

When `selfConfigure.enabled == true`, the per-instance Role (already created in Phase 1) gets additional rules:

```yaml
- apiGroups: ["claw.prismer.ai"]
  resources: [claws]
  verbs: [get]
  resourceNames: ["<instance-name>"]
- apiGroups: ["claw.prismer.ai"]
  resources: [clawselfconfigs]
  verbs: [create, get, list]
```

---

## 5. File Layout

```
sdk/
  client.go          # Client with CRUD + WaitForReady
  types.go           # ClawSpec, ClawInstance, UpdateSpec, etc.
  options.go         # Functional options
  convert.go         # toUnstructured / fromUnstructured
  client_test.go     # Unit tests with fake dynamic client

api/v1alpha1/
  clawselfconfig_types.go   # ClawSelfConfig CRD types
  zz_generated.deepcopy.go  # Generated

internal/controller/
  metrics.go                    # Operator Prometheus metrics
  events.go                     # Event reason constants
  selfconfig_controller.go      # ClawSelfConfig reconciler
  selfconfig_controller_test.go # Tests

config/prometheus/
  servicemonitor.yaml    # ServiceMonitor for operator
  prometheusrule.yaml    # PrometheusRule with 8 alerts

config/samples/
  selfconfig-example.yaml  # Example ClawSelfConfig
```

## 6. Dependencies

- `k8s.io/client-go` (already in go.mod, used by SDK)
- No new external dependencies required
