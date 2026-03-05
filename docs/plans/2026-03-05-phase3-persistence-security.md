# Phase 3: Persistence + Security Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Implement the 9 Phase 3 features that add persistence lifecycle management, network security, ingress, PDB, and skill installation safety to the Claw operator.

**Architecture:** Each feature follows the existing `ensure*` pattern in `claw_controller.go`. New CRD fields are added to `api/v1alpha1/` types, new `ensure*` methods are wired into the reconcile loop, and operator RBAC manifests are updated. Features that depend on optional CRDs (VolumeSnapshot) use conditional registration. The output archiver sidecar is injected similarly to channel sidecars.

**Tech Stack:** Go 1.25, controller-runtime v0.23.1, envtest, K8s API v0.35.2, CSI VolumeSnapshot v1

---

## Task 1: Add CRD fields for Security, Ingress, and PDB

**Files:**
- Modify: `api/v1alpha1/common_types.go` (add new types)
- Modify: `api/v1alpha1/claw_types.go` (add fields to ClawSpec)
- Modify: `api/v1alpha1/zz_generated.deepcopy.go` (regenerated)

### Step 1: Add types to common_types.go

Append these types after `ObservabilitySpec`:

```go
// SecuritySpec configures network security policies.
type SecuritySpec struct {
	// NetworkPolicy controls per-instance NetworkPolicy creation.
	// +optional
	NetworkPolicy *NetworkPolicySpec `json:"networkPolicy,omitempty"`
}

// NetworkPolicySpec configures the auto-generated NetworkPolicy.
type NetworkPolicySpec struct {
	// Enabled controls whether a NetworkPolicy is created. Default: true.
	// +kubebuilder:default=true
	Enabled bool `json:"enabled"`

	// AllowedEgressCIDRs are additional CIDR blocks allowed for egress.
	// +optional
	AllowedEgressCIDRs []string `json:"allowedEgressCIDRs,omitempty"`

	// AllowedIngressNamespaces are namespaces allowed to access this Claw.
	// +optional
	AllowedIngressNamespaces []string `json:"allowedIngressNamespaces,omitempty"`
}

// IngressSpec configures external HTTP access.
type IngressSpec struct {
	// Enabled controls whether an Ingress is created.
	Enabled bool `json:"enabled"`

	// Host is the FQDN for the Ingress rule.
	Host string `json:"host"`

	// ClassName is the IngressClass name (e.g., "nginx").
	// +optional
	ClassName string `json:"className,omitempty"`

	// TLS configures TLS termination.
	// +optional
	TLS *IngressTLS `json:"tls,omitempty"`

	// BasicAuth configures optional HTTP Basic Authentication.
	// +optional
	BasicAuth *BasicAuthSpec `json:"basicAuth,omitempty"`

	// Annotations are additional annotations to add to the Ingress.
	// +optional
	Annotations map[string]string `json:"annotations,omitempty"`
}

// IngressTLS configures TLS for an Ingress.
type IngressTLS struct {
	// SecretName is the TLS Secret name.
	SecretName string `json:"secretName"`
}

// BasicAuthSpec configures HTTP Basic Auth for Ingress.
type BasicAuthSpec struct {
	// Enabled controls whether basic auth is active.
	Enabled bool `json:"enabled"`

	// SecretName references a Secret containing htpasswd data.
	SecretName string `json:"secretName"`
}

// AvailabilitySpec configures availability settings.
type AvailabilitySpec struct {
	// PDB configures PodDisruptionBudget.
	// +optional
	PDB *PDBSpec `json:"pdb,omitempty"`
}

// PDBSpec configures a PodDisruptionBudget.
type PDBSpec struct {
	// Enabled controls whether a PDB is created. Default: true.
	// +kubebuilder:default=true
	Enabled bool `json:"enabled"`

	// MinAvailable is the minimum number of available pods. Default: 1.
	// +kubebuilder:default=1
	MinAvailable int `json:"minAvailable,omitempty"`
}
```

### Step 2: Add fields to ClawSpec in claw_types.go

Add these fields to `ClawSpec`:

```go
	// Security configures network policies.
	// +optional
	Security *SecuritySpec `json:"security,omitempty"`

	// Ingress configures external HTTP access.
	// +optional
	Ingress *IngressSpec `json:"ingress,omitempty"`

	// Availability configures PDB and other availability settings.
	// +optional
	Availability *AvailabilitySpec `json:"availability,omitempty"`
```

### Step 3: Regenerate deepcopy

Run: `go generate ./api/...` (or `controller-gen object paths=./api/...`)

### Step 4: Regenerate CRD manifests

Run: `make manifests` or `controller-gen crd paths=./api/... output:crd:dir=config/crd/bases`

### Step 5: Verify build

Run: `go build ./...`

### Step 6: Commit

```
feat: add CRD fields for Security, Ingress, and Availability
```

---

## Task 2: NetworkPolicy per Claw instance

**Files:**
- Create: `internal/controller/claw_networkpolicy.go`
- Modify: `internal/controller/claw_controller.go` (add `ensureNetworkPolicy` to reconcile loop + `Owns`)
- Modify: `config/rbac/role.yaml` (add `networking.k8s.io` permissions)
- Test: `internal/controller/claw_controller_coverage_test.go` (unit tests for buildNetworkPolicy)
- Test: `internal/controller/claw_controller_test.go` (integration test)

### Step 1: Write unit tests for `buildNetworkPolicy`

```go
func TestBuildNetworkPolicy(t *testing.T) {
	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{Name: "my-agent", Namespace: "prod"},
		Spec: clawv1alpha1.ClawSpec{
			Runtime: clawv1alpha1.RuntimeOpenClaw,
		},
	}
	np := buildNetworkPolicy(claw, 18900)

	// Verify name: <claw-name>-netpol
	if np.Name != "my-agent-netpol" { t.Errorf(...) }

	// Verify podSelector uses claw.prismer.ai/instance label
	// Verify policyTypes = [Ingress, Egress]
	// Verify DNS egress (port 53 UDP+TCP)
	// Verify HTTPS egress (port 443 TCP)
	// Verify same-namespace ingress (empty podSelector)
}

func TestBuildNetworkPolicy_WithCustomEgress(t *testing.T) {
	// Test with spec.security.networkPolicy.allowedEgressCIDRs
}

func TestBuildNetworkPolicy_Disabled(t *testing.T) {
	// Test ensureNetworkPolicy is a no-op when enabled=false
}
```

### Step 2: Implement `claw_networkpolicy.go`

Follow the `ensureService` / `ensureRole` pattern:
- `ensureNetworkPolicy(ctx, claw, adapter)` — create/update/delete based on `spec.security.networkPolicy.enabled`
- `buildNetworkPolicy(claw, gatewayPort)` — construct the desired NetworkPolicy matching design doc Section 9.2
- `deleteOwnedNetworkPolicy(ctx, claw, name)` — cleanup when disabled

Key details from design doc:
- Name: `<claw-name>-netpol`
- Default-deny + selective allow
- Always allow: DNS (53 UDP/TCP), HTTPS (443 TCP), same-namespace ingress
- Conditionally allow: user-defined CIDR egress, cross-namespace ingress, ingress-controller ingress
- When `spec.security.networkPolicy.enabled: false` → skip creation entirely

### Step 3: Wire into controller

Add `ensureNetworkPolicy` call after `ensureRoleBinding` in reconcile loop.
Add `Owns(&networkingv1.NetworkPolicy{})` to `SetupWithManager`.
Add RBAC marker: `// +kubebuilder:rbac:groups=networking.k8s.io,resources=networkpolicies,verbs=get;list;watch;create;update;patch;delete`

### Step 4: Update `config/rbac/role.yaml`

Add:
```yaml
  # Networking (NetworkPolicy + Ingress)
  - apiGroups: ["networking.k8s.io"]
    resources: [networkpolicies, ingresses]
    verbs: [get, list, watch, create, update, patch, delete]
```

### Step 5: Run tests

Run: `KUBEBUILDER_ASSETS=$(setup-envtest use --print path) go test -race ./internal/controller/ -v -run TestBuildNetworkPolicy -timeout 120s`

### Step 6: Commit

```
feat: add per-instance NetworkPolicy with default-deny
```

---

## Task 3: Ingress management

**Files:**
- Create: `internal/controller/claw_ingress.go`
- Modify: `internal/controller/claw_controller.go` (add `ensureIngress` + `Owns`)
- Test: `internal/controller/claw_controller_coverage_test.go` (unit tests)

### Step 1: Write unit tests for `buildIngress`

Test cases:
- Basic ingress with host + gateway port
- Ingress with TLS
- Ingress with BasicAuth annotations
- Ingress with custom annotations merged
- `ensureIngress` is no-op when `spec.ingress` is nil or `enabled: false`

### Step 2: Implement `claw_ingress.go`

- `ensureIngress(ctx, claw, adapter)` — create/update/delete
- `buildIngress(claw, gatewayPort)` — construct Ingress from design doc Section 9.5:
  - Name: `<claw-name>`
  - `ingressClassName` from `spec.ingress.className`
  - TLS from `spec.ingress.tls.secretName`
  - Basic auth: adds `nginx.ingress.kubernetes.io/auth-type: basic` and `nginx.ingress.kubernetes.io/auth-secret` annotations
  - User annotations merged on top
  - Single rule: host → path `/` Prefix → service `<claw-name>` port `<gateway-port>`

### Step 3: Wire into controller

Add after `ensureNetworkPolicy` in reconcile loop. `Owns(&networkingv1.Ingress{})` already covered by RBAC from Task 2.

### Step 4: Run tests, commit

```
feat: add Ingress management with optional TLS and Basic Auth
```

---

## Task 4: PodDisruptionBudget per instance

**Files:**
- Create: `internal/controller/claw_pdb.go`
- Modify: `internal/controller/claw_controller.go` (add `ensurePDB` + `Owns`)
- Modify: `config/rbac/role.yaml` (add `policy` apiGroup)
- Test: `internal/controller/claw_controller_coverage_test.go` (unit tests)

### Step 1: Write unit tests for `buildPDB`

Test cases:
- Default PDB with minAvailable=1
- Custom minAvailable
- PDB disabled (cleanup path)

### Step 2: Implement `claw_pdb.go`

- `ensurePDB(ctx, claw)` — create/update/delete
- `buildPDB(claw)` — from design doc Section 9.6:
  - Name: `<claw-name>`
  - `minAvailable` from `spec.availability.pdb.minAvailable` (default 1)
  - Selector: `claw.prismer.ai/instance: <claw-name>`
- Default behavior: create PDB unless `spec.availability.pdb.enabled: false`

### Step 3: Wire into controller, update RBAC

Add `Owns(&policyv1.PodDisruptionBudget{})`.
RBAC marker: `// +kubebuilder:rbac:groups=policy,resources=poddisruptionbudgets,verbs=get;list;watch;create;update;patch;delete`

Add to `config/rbac/role.yaml`:
```yaml
  - apiGroups: ["policy"]
    resources: [poddisruptionbudgets]
    verbs: [get, list, watch, create, update, patch, delete]
```

### Step 4: Run tests, commit

```
feat: add PodDisruptionBudget per Claw instance
```

---

## Task 5: PVC lifecycle — ownerReferences on StatefulSet VolumeClaimTemplates

**Files:**
- Modify: `internal/runtime/pod_builder.go` (add labels to VolumeClaimTemplates)
- Modify: `internal/controller/claw_controller.go` (ensure PVC labels after StatefulSet creation)
- Test: existing tests cover VolumeClaimTemplates; add label verification

### Step 1: Verify current VolumeClaimTemplate labels

Check `BuildVolumeClaimTemplates` in `pod_builder.go` — ensure PVCs get `claw.prismer.ai/instance: <name>` label (already used by `deleteClawPVCs`).

### Step 2: Add ownerReference label if missing

The StatefulSet controller creates PVCs, but ownerReferences are NOT set by StatefulSet. The design doc says:
- PVCs have `ownerReferences` pointing to the Claw CR + labels
- For `Retain` policy: finalizer removes ownerReference (PVCs become orphaned)

Add a reconcile step after StatefulSet creation that lists PVCs by label and ensures ownerReference is set to the Claw CR. This ensures GC works for `Delete` policy.

### Step 3: Update `handleDeletion` for `Retain` policy

For `Retain`: remove ownerReference from PVCs (so they survive Claw deletion) instead of doing nothing.

### Step 4: Run tests, commit

```
feat: add ownerReferences to PVCs for lifecycle management
```

---

## Task 6: Archive reclaim policy implementation

**Files:**
- Modify: `internal/controller/claw_controller.go` (implement Archive path in `handleDeletion`)

### Step 1: Implement Archive reclaim in `handleDeletion`

Currently the Archive case just logs. Implement:
- For now, treat Archive as "log warning + retain" until the archiver sidecar (Task 8) is fully implemented
- Set a status condition `ArchivePending=True` on the PVCs
- Emit a K8s Event `ArchiveRequested`

This is a placeholder that will be completed when the archiver sidecar exists.

### Step 2: Run tests, commit

```
feat: add Archive reclaim policy placeholder with status condition
```

---

## Task 7: CSI VolumeSnapshot management

**Files:**
- Create: `internal/controller/claw_snapshot.go`
- Modify: `internal/controller/claw_controller.go` (add snapshot reconciliation)
- Modify: `go.mod` (add `github.com/kubernetes-csi/external-snapshotter` dependency)

### Step 1: Add VolumeSnapshot dependency

Run: `go get github.com/kubernetes-csi/external-snapshotter/client/v8@latest`

### Step 2: Implement `claw_snapshot.go`

- `reconcileSnapshots(ctx, claw)` — called from reconcile loop
- For each volume with `snapshot.enabled: true`:
  - Parse cron schedule, check if snapshot is due
  - Create `VolumeSnapshot` CR with `persistentVolumeClaimName` source
  - List existing snapshots by label, prune beyond `retain` count
- Use annotation `claw.prismer.ai/last-snapshot-time` to track schedule without a timer

Key: This uses `RequeueAfter` to schedule next snapshot check based on cron.

### Step 3: Add optional CRD registration

In `SetupWithManager`, conditionally add `Owns(&snapshotv1.VolumeSnapshot{})` only if the VolumeSnapshot CRD is installed (check via discovery API at startup).

### Step 4: Write unit tests

Test `buildVolumeSnapshot`, snapshot pruning logic, cron schedule parsing.

### Step 5: Run tests, commit

```
feat: add CSI VolumeSnapshot management with cron scheduling
```

---

## Task 8: Output archiver sidecar injection

**Files:**
- Create: `internal/controller/claw_archiver.go`
- Modify: `internal/controller/claw_controller.go` (inject archiver in `buildStatefulSet`)

### Step 1: Implement archiver sidecar builder

When `spec.persistence.output.archive.enabled: true`:
- Inject a native sidecar container `archive-sidecar` into the pod template
- Image: `ghcr.io/prismer-ai/claw-archiver:latest`
- Mount: output PVC at same mountPath
- Env: S3 config from `spec.persistence.output.archive.destination`
- Args: `--schedule`, `--inotify`, `--local-retention`, `--compress`
- Resources: 50m/64Mi request, 200m/256Mi limit

### Step 2: Wire into `buildStatefulSet`

After channel sidecar injection, before security context:
```go
if claw.Spec.Persistence != nil && claw.Spec.Persistence.Output != nil &&
   claw.Spec.Persistence.Output.Archive != nil && claw.Spec.Persistence.Output.Archive.Enabled {
    injectArchiverSidecar(claw, podTemplate)
}
```

### Step 3: Write unit tests, commit

```
feat: inject output archiver sidecar for S3 archival
```

---

## Task 9: Skill installation security in init container

**Files:**
- Modify: `internal/runtime/pod_builder.go` (add `NPM_CONFIG_IGNORE_SCRIPTS=true` env to init container)

### Step 1: Add env var to init container

In `buildInitContainer()`, add to the env list:
```go
{Name: "NPM_CONFIG_IGNORE_SCRIPTS", Value: "true"},
```

This prevents malicious npm postinstall scripts during skill installation (Section 9.4).

### Step 2: Write unit test verifying env var exists

### Step 3: Run tests, commit

```
feat: add NPM_CONFIG_IGNORE_SCRIPTS to init container for supply chain protection
```
