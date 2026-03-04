# P0 Operator Core Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Implement the 5 P0 features that complete the core Claw operator reconciliation loop: Headless Service, ConfigMap, credential injection, ClawChannel controller, and PVC reclaim policy.

**Architecture:** Each feature follows the existing create-or-update pattern in `claw_controller.go`. New `ensure*` methods are added to the reconcile loop in order: Service → ConfigMap → credentials (injected into pod template). The ClawChannel controller is a separate reconciler with field indexer for back-references. PVC reclaim is added to the existing `handleDeletion` method.

**Tech Stack:** Go 1.25, controller-runtime v0.23.1, envtest, K8s API v0.35.2

---

## Task 1: Headless Service per Claw

**Files:**
- Create: `internal/controller/claw_service.go`
- Modify: `internal/controller/claw_controller.go:41-81` (add `ensureService` call to Reconcile)
- Test: `internal/controller/claw_controller_test.go` (add `TestClawReconciler_ServiceCreated`)

### Step 1: Write the failing test

Add to `internal/controller/claw_controller_test.go`:

```go
func TestClawReconciler_ServiceCreated(t *testing.T) {
	ns := fmt.Sprintf("test-svc-create-%d", time.Now().UnixNano())
	createNamespace(t, ns)

	clawName := "test-claw-svc"
	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{
			Name:      clawName,
			Namespace: ns,
		},
		Spec: clawv1alpha1.ClawSpec{
			Runtime: clawv1alpha1.RuntimeOpenClaw,
		},
	}

	if err := k8sClient.Create(ctx, claw); err != nil {
		t.Fatalf("failed to create Claw: %v", err)
	}

	// Wait for the headless Service to appear.
	var svc corev1.Service
	waitForCondition(t, testTimeout, testInterval, func() (bool, error) {
		err := k8sClient.Get(ctx, types.NamespacedName{
			Name:      clawName,
			Namespace: ns,
		}, &svc)
		if err != nil {
			if client.IgnoreNotFound(err) == nil {
				return false, nil
			}
			return false, err
		}
		return true, nil
	})

	// Verify headless (ClusterIP = None).
	if svc.Spec.ClusterIP != "None" {
		t.Errorf("expected ClusterIP=None, got %q", svc.Spec.ClusterIP)
	}

	// Verify selector labels.
	expectedSelector := map[string]string{
		"app.kubernetes.io/name":     "claw",
		"app.kubernetes.io/instance": clawName,
	}
	for k, v := range expectedSelector {
		if got := svc.Spec.Selector[k]; got != v {
			t.Errorf("Service selector %q: expected %q, got %q", k, v, got)
		}
	}

	// Verify port 18900 (OpenClaw gateway port).
	if len(svc.Spec.Ports) == 0 {
		t.Fatal("expected at least one port on the Service")
	}
	foundGateway := false
	for _, p := range svc.Spec.Ports {
		if p.Name == "gateway" {
			foundGateway = true
			if p.Port != 18900 {
				t.Errorf("expected gateway port=18900, got %d", p.Port)
			}
		}
	}
	if !foundGateway {
		t.Error("expected port named 'gateway' on Service")
	}

	// Verify ownerReferences.
	if len(svc.OwnerReferences) != 1 {
		t.Fatalf("expected 1 ownerReference, got %d", len(svc.OwnerReferences))
	}
	if svc.OwnerReferences[0].Kind != "Claw" {
		t.Errorf("expected ownerReference kind=Claw, got %q", svc.OwnerReferences[0].Kind)
	}

	// Verify labels on the Service.
	expectedLabels := map[string]string{
		"app.kubernetes.io/name":     "claw",
		"app.kubernetes.io/instance": clawName,
		"claw.prismer.ai/runtime":    "openclaw",
		"claw.prismer.ai/instance":   clawName,
	}
	for k, v := range expectedLabels {
		if got := svc.Labels[k]; got != v {
			t.Errorf("Service label %q: expected %q, got %q", k, v, got)
		}
	}
}
```

### Step 2: Run test to verify it fails

Run: `KUBEBUILDER_ASSETS=$PWD/bin/k8s/k8s/1.35.0-linux-amd64 go test ./internal/controller/ -run TestClawReconciler_ServiceCreated -v -count=1`
Expected: FAIL — test times out waiting for Service (no `ensureService` method exists yet).

### Step 3: Create `claw_service.go` with `ensureService`

Create `internal/controller/claw_service.go`:

```go
package controller

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	clawv1alpha1 "github.com/Prismer-AI/k8s4claw/api/v1alpha1"
	clawruntime "github.com/Prismer-AI/k8s4claw/internal/runtime"
)

// ensureService creates or updates the headless Service for the given Claw.
func (r *ClawReconciler) ensureService(ctx context.Context, claw *clawv1alpha1.Claw, adapter clawruntime.RuntimeAdapter) error {
	logger := log.FromContext(ctx)

	desired := r.buildService(claw, adapter)
	if err := controllerutil.SetControllerReference(claw, desired, r.Scheme); err != nil {
		return fmt.Errorf("failed to set controller reference on Service: %w", err)
	}

	var existing corev1.Service
	err := r.Get(ctx, types.NamespacedName{Name: desired.Name, Namespace: desired.Namespace}, &existing)
	if apierrors.IsNotFound(err) {
		logger.Info("creating headless Service", "name", desired.Name, "namespace", desired.Namespace)
		if err := r.Create(ctx, desired); err != nil {
			return fmt.Errorf("failed to create Service: %w", err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("failed to get Service: %w", err)
	}

	// Update mutable fields.
	existing.Spec.Ports = desired.Spec.Ports
	existing.Labels = desired.Labels
	if err := r.Update(ctx, &existing); err != nil {
		return fmt.Errorf("failed to update Service: %w", err)
	}

	return nil
}

// buildService constructs the headless Service for a Claw.
func (r *ClawReconciler) buildService(claw *clawv1alpha1.Claw, adapter clawruntime.RuntimeAdapter) *corev1.Service {
	labels := clawLabels(claw)

	// Extract ports from the adapter's pod template.
	podTemplate := adapter.PodTemplate(claw)
	var svcPorts []corev1.ServicePort
	for _, c := range podTemplate.Spec.Containers {
		for _, p := range c.Ports {
			svcPorts = append(svcPorts, corev1.ServicePort{
				Name:       p.Name,
				Port:       p.ContainerPort,
				TargetPort: intstr.FromInt32(p.ContainerPort),
				Protocol:   p.Protocol,
			})
		}
	}

	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      claw.Name,
			Namespace: claw.Namespace,
			Labels:    labels,
		},
		Spec: corev1.ServiceSpec{
			ClusterIP: "None",
			Selector: map[string]string{
				"app.kubernetes.io/name":     "claw",
				"app.kubernetes.io/instance": claw.Name,
			},
			Ports: svcPorts,
		},
	}
}
```

### Step 4: Extract `clawLabels` helper

Add to `claw_controller.go` or create a small helper (avoid duplication with `buildStatefulSet`):

```go
// clawLabels returns the standard labels for a Claw resource.
func clawLabels(claw *clawv1alpha1.Claw) map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":     "claw",
		"app.kubernetes.io/instance": claw.Name,
		"claw.prismer.ai/runtime":    string(claw.Spec.Runtime),
		"claw.prismer.ai/instance":   claw.Name,
	}
}
```

Then update `buildStatefulSet` to use `clawLabels(claw)` instead of the inline map.

### Step 5: Wire `ensureService` into Reconcile

In `claw_controller.go`, add after `ensureFinalizer` and before `ensureStatefulSet`:

```go
	// Ensure headless Service exists.
	if err := r.ensureService(ctx, &claw, adapter); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to ensure Service: %w", err)
	}
```

### Step 6: Run tests to verify it passes

Run: `KUBEBUILDER_ASSETS=$PWD/bin/k8s/k8s/1.35.0-linux-amd64 go test ./internal/controller/ -v -count=1`
Expected: ALL PASS (including new `TestClawReconciler_ServiceCreated`).

### Step 7: Commit

```bash
git add internal/controller/claw_service.go internal/controller/claw_controller.go internal/controller/claw_controller_test.go
git commit -m "feat: add headless Service creation per Claw"
```

---

## Task 2: ConfigMap per Claw

**Files:**
- Create: `internal/controller/claw_configmap.go`
- Modify: `internal/controller/claw_controller.go` (add `ensureConfigMap` call to Reconcile)
- Test: `internal/controller/claw_controller_test.go` (add `TestClawReconciler_ConfigMapCreated`)

### Step 1: Write the failing test

```go
func TestClawReconciler_ConfigMapCreated(t *testing.T) {
	ns := fmt.Sprintf("test-cm-create-%d", time.Now().UnixNano())
	createNamespace(t, ns)

	clawName := "test-claw-cm"
	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{
			Name:      clawName,
			Namespace: ns,
		},
		Spec: clawv1alpha1.ClawSpec{
			Runtime: clawv1alpha1.RuntimeNanoClaw,
		},
	}

	if err := k8sClient.Create(ctx, claw); err != nil {
		t.Fatalf("failed to create Claw: %v", err)
	}

	// Wait for the ConfigMap to appear.
	cmName := clawName + "-config"
	var cm corev1.ConfigMap
	waitForCondition(t, testTimeout, testInterval, func() (bool, error) {
		err := k8sClient.Get(ctx, types.NamespacedName{
			Name:      cmName,
			Namespace: ns,
		}, &cm)
		if err != nil {
			if client.IgnoreNotFound(err) == nil {
				return false, nil
			}
			return false, err
		}
		return true, nil
	})

	// Verify ownerReferences.
	if len(cm.OwnerReferences) != 1 {
		t.Fatalf("expected 1 ownerReference, got %d", len(cm.OwnerReferences))
	}
	if cm.OwnerReferences[0].Kind != "Claw" {
		t.Errorf("expected ownerReference kind=Claw, got %q", cm.OwnerReferences[0].Kind)
	}

	// Verify labels.
	expectedLabels := map[string]string{
		"app.kubernetes.io/name":     "claw",
		"app.kubernetes.io/instance": clawName,
	}
	for k, v := range expectedLabels {
		if got := cm.Labels[k]; got != v {
			t.Errorf("ConfigMap label %q: expected %q, got %q", k, v, got)
		}
	}

	// Verify the ConfigMap has a "config.json" key with runtime defaults.
	if _, ok := cm.Data["config.json"]; !ok {
		t.Error("expected config.json key in ConfigMap data")
	}
}
```

### Step 2: Run test to verify it fails

Run: `KUBEBUILDER_ASSETS=$PWD/bin/k8s/k8s/1.35.0-linux-amd64 go test ./internal/controller/ -run TestClawReconciler_ConfigMapCreated -v -count=1`
Expected: FAIL (no `ensureConfigMap` method).

### Step 3: Add `RuntimeConfig` serialization to adapters

The `RuntimeConfig` struct already exists in `internal/runtime/adapter.go`. Add a `DefaultConfigJSON` helper:

In `internal/runtime/adapter.go` (or a new `config.go`):

```go
// DefaultConfigJSON returns the default config as a JSON string.
func DefaultConfigJSON(adapter RuntimeAdapter) (string, error) {
	cfg := adapter.DefaultConfig()
	if cfg == nil {
		return "{}", nil
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		return "", fmt.Errorf("failed to marshal default config: %w", err)
	}
	return string(data), nil
}
```

### Step 4: Create `claw_configmap.go`

```go
package controller

import (
	"context"
	"encoding/json"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	clawv1alpha1 "github.com/Prismer-AI/k8s4claw/api/v1alpha1"
	clawruntime "github.com/Prismer-AI/k8s4claw/internal/runtime"
)

// ensureConfigMap creates or updates the config ConfigMap for the given Claw.
func (r *ClawReconciler) ensureConfigMap(ctx context.Context, claw *clawv1alpha1.Claw, adapter clawruntime.RuntimeAdapter) error {
	logger := log.FromContext(ctx)

	desired, err := r.buildConfigMap(claw, adapter)
	if err != nil {
		return fmt.Errorf("failed to build ConfigMap: %w", err)
	}
	if err := controllerutil.SetControllerReference(claw, desired, r.Scheme); err != nil {
		return fmt.Errorf("failed to set controller reference on ConfigMap: %w", err)
	}

	var existing corev1.ConfigMap
	err = r.Get(ctx, types.NamespacedName{Name: desired.Name, Namespace: desired.Namespace}, &existing)
	if apierrors.IsNotFound(err) {
		logger.Info("creating ConfigMap", "name", desired.Name, "namespace", desired.Namespace)
		if err := r.Create(ctx, desired); err != nil {
			return fmt.Errorf("failed to create ConfigMap: %w", err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("failed to get ConfigMap: %w", err)
	}

	// Update data.
	existing.Data = desired.Data
	existing.Labels = desired.Labels
	if err := r.Update(ctx, &existing); err != nil {
		return fmt.Errorf("failed to update ConfigMap: %w", err)
	}

	return nil
}

// buildConfigMap constructs the ConfigMap for a Claw. It merges the runtime
// default config with any user-specified config from claw.Spec.Config.
func (r *ClawReconciler) buildConfigMap(claw *clawv1alpha1.Claw, adapter clawruntime.RuntimeAdapter) (*corev1.ConfigMap, error) {
	configJSON, err := clawruntime.DefaultConfigJSON(adapter)
	if err != nil {
		return nil, fmt.Errorf("failed to get default config JSON: %w", err)
	}

	// If user provided config, merge or override based on adapter's ConfigMode.
	if claw.Spec.Config != nil {
		merged, mergeErr := mergeConfig(configJSON, claw.Spec.Config.Raw)
		if mergeErr != nil {
			return nil, fmt.Errorf("failed to merge user config: %w", mergeErr)
		}
		configJSON = merged
	}

	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-config", claw.Name),
			Namespace: claw.Namespace,
			Labels:    clawLabels(claw),
		},
		Data: map[string]string{
			"config.json": configJSON,
		},
	}, nil
}

// mergeConfig deep-merges user config over defaults. Both inputs are JSON strings.
func mergeConfig(defaultJSON string, userJSON []byte) (string, error) {
	var base map[string]interface{}
	if err := json.Unmarshal([]byte(defaultJSON), &base); err != nil {
		return "", fmt.Errorf("failed to unmarshal default config: %w", err)
	}

	var user map[string]interface{}
	if err := json.Unmarshal(userJSON, &user); err != nil {
		return "", fmt.Errorf("failed to unmarshal user config: %w", err)
	}

	merged := deepMerge(base, user)
	result, err := json.Marshal(merged)
	if err != nil {
		return "", fmt.Errorf("failed to marshal merged config: %w", err)
	}
	return string(result), nil
}

// deepMerge recursively merges src into dst. src values override dst values.
func deepMerge(dst, src map[string]interface{}) map[string]interface{} {
	for k, v := range src {
		if srcMap, ok := v.(map[string]interface{}); ok {
			if dstMap, ok := dst[k].(map[string]interface{}); ok {
				dst[k] = deepMerge(dstMap, srcMap)
				continue
			}
		}
		dst[k] = v
	}
	return dst
}
```

### Step 5: Wire `ensureConfigMap` into Reconcile

In `claw_controller.go`, add after `ensureService` and before `ensureStatefulSet`:

```go
	// Ensure ConfigMap exists.
	if err := r.ensureConfigMap(ctx, &claw, adapter); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to ensure ConfigMap: %w", err)
	}
```

### Step 6: Run tests to verify it passes

Run: `KUBEBUILDER_ASSETS=$PWD/bin/k8s/k8s/1.35.0-linux-amd64 go test ./internal/controller/ -v -count=1`
Expected: ALL PASS.

### Step 7: Commit

```bash
git add internal/controller/claw_configmap.go internal/controller/claw_controller.go internal/controller/claw_controller_test.go internal/runtime/
git commit -m "feat: add ConfigMap creation with runtime default config merge"
```

---

## Task 3: Credential Injection (Secret hash + envFrom)

**Files:**
- Create: `internal/controller/claw_credentials.go`
- Modify: `internal/controller/claw_controller.go` (inject credentials into pod template)
- Test: `internal/controller/claw_controller_test.go` (add credential tests)

### Step 1: Write failing tests

```go
func TestClawReconciler_CredentialInjection(t *testing.T) {
	ns := fmt.Sprintf("test-cred-inject-%d", time.Now().UnixNano())
	createNamespace(t, ns)

	// Pre-create a Secret for the Claw to reference.
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-claw-creds",
			Namespace: ns,
		},
		StringData: map[string]string{
			"OPENAI_API_KEY": "sk-test-key-123",
		},
	}
	if err := k8sClient.Create(ctx, secret); err != nil {
		t.Fatalf("failed to create Secret: %v", err)
	}

	clawName := "test-claw-with-creds"
	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{
			Name:      clawName,
			Namespace: ns,
		},
		Spec: clawv1alpha1.ClawSpec{
			Runtime: clawv1alpha1.RuntimePicoClaw,
			Credentials: &clawv1alpha1.CredentialSpec{
				SecretRef: &corev1.LocalObjectReference{
					Name: "test-claw-creds",
				},
			},
		},
	}

	if err := k8sClient.Create(ctx, claw); err != nil {
		t.Fatalf("failed to create Claw: %v", err)
	}

	// Wait for the StatefulSet to appear.
	var sts appsv1.StatefulSet
	waitForCondition(t, testTimeout, testInterval, func() (bool, error) {
		err := k8sClient.Get(ctx, types.NamespacedName{
			Name:      clawName,
			Namespace: ns,
		}, &sts)
		if err != nil {
			if client.IgnoreNotFound(err) == nil {
				return false, nil
			}
			return false, err
		}
		return true, nil
	})

	// Find the runtime container.
	var runtimeContainer *corev1.Container
	for i := range sts.Spec.Template.Spec.Containers {
		if sts.Spec.Template.Spec.Containers[i].Name == "runtime" {
			runtimeContainer = &sts.Spec.Template.Spec.Containers[i]
			break
		}
	}
	if runtimeContainer == nil {
		t.Fatal("expected runtime container, not found")
	}

	// Verify envFrom references the secret.
	if len(runtimeContainer.EnvFrom) == 0 {
		t.Fatal("expected envFrom on runtime container, got none")
	}
	foundSecretRef := false
	for _, ef := range runtimeContainer.EnvFrom {
		if ef.SecretRef != nil && ef.SecretRef.Name == "test-claw-creds" {
			foundSecretRef = true
			break
		}
	}
	if !foundSecretRef {
		t.Error("expected envFrom with secretRef 'test-claw-creds'")
	}

	// Verify secret hash annotation on pod template.
	ann := sts.Spec.Template.Annotations
	if ann == nil {
		t.Fatal("expected pod template annotations, got nil")
	}
	if _, ok := ann["claw.prismer.ai/secret-hash"]; !ok {
		t.Error("expected claw.prismer.ai/secret-hash annotation on pod template")
	}
}

func TestClawReconciler_CredentialKeyMapping(t *testing.T) {
	ns := fmt.Sprintf("test-cred-keys-%d", time.Now().UnixNano())
	createNamespace(t, ns)

	// Pre-create a Secret.
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "multi-key-secret",
			Namespace: ns,
		},
		StringData: map[string]string{
			"api-key":    "sk-key-1",
			"api-secret": "sk-secret-1",
		},
	}
	if err := k8sClient.Create(ctx, secret); err != nil {
		t.Fatalf("failed to create Secret: %v", err)
	}

	clawName := "test-claw-keymapping"
	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{
			Name:      clawName,
			Namespace: ns,
		},
		Spec: clawv1alpha1.ClawSpec{
			Runtime: clawv1alpha1.RuntimePicoClaw,
			Credentials: &clawv1alpha1.CredentialSpec{
				Keys: []clawv1alpha1.KeyMapping{
					{
						Name: "OPENAI_KEY",
						SecretKeyRef: corev1.SecretKeySelector{
							LocalObjectReference: corev1.LocalObjectReference{Name: "multi-key-secret"},
							Key:                  "api-key",
						},
					},
				},
			},
		},
	}

	if err := k8sClient.Create(ctx, claw); err != nil {
		t.Fatalf("failed to create Claw: %v", err)
	}

	// Wait for the StatefulSet to appear.
	var sts appsv1.StatefulSet
	waitForCondition(t, testTimeout, testInterval, func() (bool, error) {
		err := k8sClient.Get(ctx, types.NamespacedName{
			Name:      clawName,
			Namespace: ns,
		}, &sts)
		if err != nil {
			if client.IgnoreNotFound(err) == nil {
				return false, nil
			}
			return false, err
		}
		return true, nil
	})

	// Find the runtime container.
	var runtimeContainer *corev1.Container
	for i := range sts.Spec.Template.Spec.Containers {
		if sts.Spec.Template.Spec.Containers[i].Name == "runtime" {
			runtimeContainer = &sts.Spec.Template.Spec.Containers[i]
			break
		}
	}
	if runtimeContainer == nil {
		t.Fatal("expected runtime container, not found")
	}

	// Verify env var with valueFrom secretKeyRef.
	foundKey := false
	for _, e := range runtimeContainer.Env {
		if e.Name == "OPENAI_KEY" && e.ValueFrom != nil && e.ValueFrom.SecretKeyRef != nil {
			if e.ValueFrom.SecretKeyRef.Name == "multi-key-secret" && e.ValueFrom.SecretKeyRef.Key == "api-key" {
				foundKey = true
			}
		}
	}
	if !foundKey {
		t.Error("expected env var OPENAI_KEY with secretKeyRef to multi-key-secret/api-key")
	}
}
```

### Step 2: Run test to verify it fails

Run: `KUBEBUILDER_ASSETS=$PWD/bin/k8s/k8s/1.35.0-linux-amd64 go test ./internal/controller/ -run TestClawReconciler_Credential -v -count=1`
Expected: FAIL (no credential injection logic).

### Step 3: Create `claw_credentials.go`

```go
package controller

import (
	"context"
	"crypto/sha256"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"

	clawv1alpha1 "github.com/Prismer-AI/k8s4claw/api/v1alpha1"
)

// injectCredentials modifies the pod template to include credential sources.
// For secretRef: adds envFrom to the runtime container and computes a hash
// annotation so that Secret changes trigger a pod rollout.
// For keys: adds individual env vars with valueFrom secretKeyRef.
func (r *ClawReconciler) injectCredentials(ctx context.Context, claw *clawv1alpha1.Claw, podTemplate *corev1.PodTemplateSpec) error {
	if claw.Spec.Credentials == nil {
		return nil
	}

	// Find the runtime container.
	runtimeIdx := -1
	for i, c := range podTemplate.Spec.Containers {
		if c.Name == "runtime" {
			runtimeIdx = i
			break
		}
	}
	if runtimeIdx == -1 {
		return fmt.Errorf("runtime container not found in pod template")
	}

	creds := claw.Spec.Credentials

	// Handle secretRef: inject envFrom + hash annotation.
	if creds.SecretRef != nil {
		podTemplate.Spec.Containers[runtimeIdx].EnvFrom = append(
			podTemplate.Spec.Containers[runtimeIdx].EnvFrom,
			corev1.EnvFromSource{
				SecretRef: &corev1.SecretEnvSource{
					LocalObjectReference: *creds.SecretRef,
				},
			},
		)

		hash, err := r.computeSecretHash(ctx, claw.Namespace, creds.SecretRef.Name)
		if err != nil {
			return fmt.Errorf("failed to compute secret hash: %w", err)
		}

		if podTemplate.Annotations == nil {
			podTemplate.Annotations = make(map[string]string)
		}
		podTemplate.Annotations["claw.prismer.ai/secret-hash"] = hash
	}

	// Handle per-key mappings: inject individual env vars.
	for _, km := range creds.Keys {
		podTemplate.Spec.Containers[runtimeIdx].Env = append(
			podTemplate.Spec.Containers[runtimeIdx].Env,
			corev1.EnvVar{
				Name: km.Name,
				ValueFrom: &corev1.EnvVarSource{
					SecretKeyRef: &corev1.SecretKeySelector{
						LocalObjectReference: km.SecretKeyRef.LocalObjectReference,
						Key:                  km.SecretKeyRef.Key,
					},
				},
			},
		)
	}

	return nil
}

// computeSecretHash fetches the Secret and returns a SHA-256 hash of its data.
// This is used as a pod annotation so that Secret changes trigger rolling updates.
func (r *ClawReconciler) computeSecretHash(ctx context.Context, namespace, name string) (string, error) {
	var secret corev1.Secret
	if err := r.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, &secret); err != nil {
		return "", fmt.Errorf("failed to get Secret %s/%s: %w", namespace, name, err)
	}

	h := sha256.New()
	for k, v := range secret.Data {
		h.Write([]byte(k))
		h.Write([]byte("="))
		h.Write(v)
	}
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}
```

### Step 4: Wire credential injection into `buildStatefulSet`

In `claw_controller.go`, inside `buildStatefulSet`, add after the channel sidecar injection block:

```go
	// Inject credentials into the pod template.
	if claw.Spec.Credentials != nil {
		if err := r.injectCredentials(ctx, claw, podTemplate); err != nil {
			return nil, fmt.Errorf("failed to inject credentials: %w", err)
		}
	}
```

### Step 5: Run tests to verify they pass

Run: `KUBEBUILDER_ASSETS=$PWD/bin/k8s/k8s/1.35.0-linux-amd64 go test ./internal/controller/ -v -count=1`
Expected: ALL PASS.

### Step 6: Commit

```bash
git add internal/controller/claw_credentials.go internal/controller/claw_controller.go internal/controller/claw_controller_test.go
git commit -m "feat: add credential injection with Secret hash for rolling updates"
```

---

## Task 4: ClawChannel Controller

**Files:**
- Modify: `internal/controller/channel_controller.go` (implement reconcile loop)
- Modify: `api/v1alpha1/channel_types.go` (add ReferenceCount + ReferencingClaws to status)
- Modify: `cmd/operator/main.go` (register ClawChannelReconciler)
- Modify: `internal/controller/suite_test.go` (register ClawChannelReconciler in test manager)
- Create: `internal/controller/channel_controller_test.go`

### Step 1: Add status fields to ClawChannelStatus

In `api/v1alpha1/channel_types.go`, add to `ClawChannelStatus`:

```go
type ClawChannelStatus struct {
	// Conditions represent the latest available observations.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// ObservedGeneration is the most recent generation observed by the controller.
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// ReferenceCount is the number of Claw resources referencing this channel.
	ReferenceCount int `json:"referenceCount,omitempty"`

	// ReferencingClaws lists the names of Claw resources referencing this channel.
	// +optional
	ReferencingClaws []string `json:"referencingClaws,omitempty"`
}
```

### Step 2: Regenerate CRD manifests

Run: `make generate && make manifests`

### Step 3: Write failing tests

Create `internal/controller/channel_controller_test.go`:

```go
package controller

import (
	"fmt"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	clawv1alpha1 "github.com/Prismer-AI/k8s4claw/api/v1alpha1"
)

const channelFinalizer = "claw.prismer.ai/channel-cleanup"

func TestClawChannelReconciler_FinalizerAdded(t *testing.T) {
	ns := fmt.Sprintf("test-ch-finalizer-%d", time.Now().UnixNano())
	createNamespace(t, ns)

	channel := &clawv1alpha1.ClawChannel{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-channel",
			Namespace: ns,
		},
		Spec: clawv1alpha1.ClawChannelSpec{
			Type: clawv1alpha1.ChannelTypeSlack,
		},
	}

	if err := k8sClient.Create(ctx, channel); err != nil {
		t.Fatalf("failed to create ClawChannel: %v", err)
	}

	waitForCondition(t, testTimeout, testInterval, func() (bool, error) {
		var fetched clawv1alpha1.ClawChannel
		if err := k8sClient.Get(ctx, types.NamespacedName{
			Name:      channel.Name,
			Namespace: channel.Namespace,
		}, &fetched); err != nil {
			if client.IgnoreNotFound(err) == nil {
				return false, nil
			}
			return false, err
		}
		return controllerutil.ContainsFinalizer(&fetched, channelFinalizer), nil
	})
}

func TestClawChannelReconciler_ReferenceCount(t *testing.T) {
	ns := fmt.Sprintf("test-ch-refcount-%d", time.Now().UnixNano())
	createNamespace(t, ns)

	// Create a channel.
	channel := &clawv1alpha1.ClawChannel{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "ref-channel",
			Namespace: ns,
		},
		Spec: clawv1alpha1.ClawChannelSpec{
			Type: clawv1alpha1.ChannelTypeWebhook,
			Mode: clawv1alpha1.ChannelModeBidirectional,
		},
	}

	if err := k8sClient.Create(ctx, channel); err != nil {
		t.Fatalf("failed to create ClawChannel: %v", err)
	}

	// Create a Claw that references the channel.
	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "claw-with-channel",
			Namespace: ns,
		},
		Spec: clawv1alpha1.ClawSpec{
			Runtime: clawv1alpha1.RuntimePicoClaw,
			Channels: []clawv1alpha1.ChannelRef{
				{Name: "ref-channel", Mode: clawv1alpha1.ChannelModeBidirectional},
			},
		},
	}

	if err := k8sClient.Create(ctx, claw); err != nil {
		t.Fatalf("failed to create Claw: %v", err)
	}

	// Wait for the channel status to show referenceCount >= 1.
	waitForCondition(t, testTimeout, testInterval, func() (bool, error) {
		var fetched clawv1alpha1.ClawChannel
		if err := k8sClient.Get(ctx, types.NamespacedName{
			Name:      channel.Name,
			Namespace: channel.Namespace,
		}, &fetched); err != nil {
			return false, err
		}
		return fetched.Status.ReferenceCount >= 1, nil
	})
}

func TestClawChannelReconciler_DeletionProtection(t *testing.T) {
	ns := fmt.Sprintf("test-ch-delprot-%d", time.Now().UnixNano())
	createNamespace(t, ns)

	// Create a channel.
	channel := &clawv1alpha1.ClawChannel{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "protected-channel",
			Namespace: ns,
		},
		Spec: clawv1alpha1.ClawChannelSpec{
			Type: clawv1alpha1.ChannelTypeSlack,
			Mode: clawv1alpha1.ChannelModeBidirectional,
		},
	}

	if err := k8sClient.Create(ctx, channel); err != nil {
		t.Fatalf("failed to create ClawChannel: %v", err)
	}

	// Wait for finalizer.
	waitForCondition(t, testTimeout, testInterval, func() (bool, error) {
		var fetched clawv1alpha1.ClawChannel
		if err := k8sClient.Get(ctx, types.NamespacedName{
			Name:      channel.Name,
			Namespace: channel.Namespace,
		}, &fetched); err != nil {
			return false, err
		}
		return controllerutil.ContainsFinalizer(&fetched, channelFinalizer), nil
	})

	// Create a Claw referencing this channel.
	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "claw-blocking-delete",
			Namespace: ns,
		},
		Spec: clawv1alpha1.ClawSpec{
			Runtime: clawv1alpha1.RuntimePicoClaw,
			Channels: []clawv1alpha1.ChannelRef{
				{Name: "protected-channel", Mode: clawv1alpha1.ChannelModeBidirectional},
			},
		},
	}

	if err := k8sClient.Create(ctx, claw); err != nil {
		t.Fatalf("failed to create Claw: %v", err)
	}

	// Wait for referenceCount to update.
	waitForCondition(t, testTimeout, testInterval, func() (bool, error) {
		var fetched clawv1alpha1.ClawChannel
		if err := k8sClient.Get(ctx, types.NamespacedName{
			Name:      channel.Name,
			Namespace: channel.Namespace,
		}, &fetched); err != nil {
			return false, err
		}
		return fetched.Status.ReferenceCount >= 1, nil
	})

	// Try to delete the channel — it should remain because of the finalizer.
	var latest clawv1alpha1.ClawChannel
	if err := k8sClient.Get(ctx, types.NamespacedName{
		Name:      channel.Name,
		Namespace: channel.Namespace,
	}, &latest); err != nil {
		t.Fatalf("failed to get channel: %v", err)
	}

	if err := k8sClient.Delete(ctx, &latest); err != nil {
		t.Fatalf("failed to delete ClawChannel: %v", err)
	}

	// Wait briefly, then verify the channel still exists (finalizer blocks deletion).
	time.Sleep(2 * time.Second)

	var stillExists clawv1alpha1.ClawChannel
	if err := k8sClient.Get(ctx, types.NamespacedName{
		Name:      channel.Name,
		Namespace: channel.Namespace,
	}, &stillExists); err != nil {
		t.Fatalf("channel should still exist (InUse protection), but got error: %v", err)
	}

	// Verify InUse condition is set.
	var inUseCond *metav1.Condition
	for i := range stillExists.Status.Conditions {
		if stillExists.Status.Conditions[i].Type == "InUse" {
			inUseCond = &stillExists.Status.Conditions[i]
			break
		}
	}
	if inUseCond == nil {
		t.Fatal("expected InUse condition, not found")
	}
	if inUseCond.Status != metav1.ConditionTrue {
		t.Errorf("expected InUse status=True, got %s", inUseCond.Status)
	}
}
```

### Step 4: Run tests to verify they fail

Run: `KUBEBUILDER_ASSETS=$PWD/bin/k8s/k8s/1.35.0-linux-amd64 go test ./internal/controller/ -run TestClawChannelReconciler -v -count=1`
Expected: FAIL.

### Step 5: Implement the ClawChannel controller

Replace `internal/controller/channel_controller.go`:

```go
package controller

import (
	"context"
	"fmt"

	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	clawv1alpha1 "github.com/Prismer-AI/k8s4claw/api/v1alpha1"
)

const clawChannelFinalizer = "claw.prismer.ai/channel-cleanup"

// ClawChannelReconciler reconciles a ClawChannel object.
type ClawChannelReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=claw.prismer.ai,resources=clawchannels,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=claw.prismer.ai,resources=clawchannels/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=claw.prismer.ai,resources=clawchannels/finalizers,verbs=update
// +kubebuilder:rbac:groups=claw.prismer.ai,resources=claws,verbs=get;list;watch

func (r *ClawChannelReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var channel clawv1alpha1.ClawChannel
	if err := r.Get(ctx, req.NamespacedName, &channel); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Find all Claws that reference this channel.
	referencingClaws, err := r.findReferencingClaws(ctx, &channel)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to find referencing Claws: %w", err)
	}

	// Handle deletion.
	if !channel.DeletionTimestamp.IsZero() {
		return r.handleChannelDeletion(ctx, &channel, referencingClaws)
	}

	// Ensure finalizer.
	if !controllerutil.ContainsFinalizer(&channel, clawChannelFinalizer) {
		logger.Info("adding finalizer to ClawChannel", "name", channel.Name)
		patch := client.MergeFrom(channel.DeepCopy())
		controllerutil.AddFinalizer(&channel, clawChannelFinalizer)
		if err := r.Patch(ctx, &channel, patch); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to add finalizer: %w", err)
		}
	}

	// Update status with reference count.
	if err := r.updateChannelStatus(ctx, &channel, referencingClaws); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to update channel status: %w", err)
	}

	return ctrl.Result{}, nil
}

// findReferencingClaws lists all Claws in the same namespace that reference this channel.
func (r *ClawChannelReconciler) findReferencingClaws(ctx context.Context, channel *clawv1alpha1.ClawChannel) ([]string, error) {
	var clawList clawv1alpha1.ClawList
	if err := r.List(ctx, &clawList, client.InNamespace(channel.Namespace)); err != nil {
		return nil, fmt.Errorf("failed to list Claws: %w", err)
	}

	var refs []string
	for _, claw := range clawList.Items {
		for _, ch := range claw.Spec.Channels {
			if ch.Name == channel.Name {
				refs = append(refs, claw.Name)
				break
			}
		}
	}
	return refs, nil
}

// handleChannelDeletion blocks deletion if the channel is still referenced by any Claw.
func (r *ClawChannelReconciler) handleChannelDeletion(ctx context.Context, channel *clawv1alpha1.ClawChannel, referencingClaws []string) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(channel, clawChannelFinalizer) {
		return ctrl.Result{}, nil
	}

	logger := log.FromContext(ctx)

	if len(referencingClaws) > 0 {
		logger.Info("ClawChannel still in use, blocking deletion",
			"name", channel.Name,
			"referencingClaws", referencingClaws,
		)

		// Set InUse condition and update status.
		if err := r.updateChannelStatus(ctx, channel, referencingClaws); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to update status during deletion: %w", err)
		}

		// Requeue to check again later.
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}

	// No references — safe to remove finalizer.
	logger.Info("no references remaining, removing finalizer", "name", channel.Name)
	patch := client.MergeFrom(channel.DeepCopy())
	controllerutil.RemoveFinalizer(channel, clawChannelFinalizer)
	if err := r.Patch(ctx, channel, patch); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to remove finalizer: %w", err)
	}

	return ctrl.Result{}, nil
}

// updateChannelStatus updates the ClawChannel status with reference count and conditions.
func (r *ClawChannelReconciler) updateChannelStatus(ctx context.Context, channel *clawv1alpha1.ClawChannel, referencingClaws []string) error {
	channel.Status.ReferenceCount = len(referencingClaws)
	channel.Status.ReferencingClaws = referencingClaws
	channel.Status.ObservedGeneration = channel.Generation

	// Set InUse condition.
	inUse := metav1.Condition{
		Type:               "InUse",
		ObservedGeneration: channel.Generation,
		LastTransitionTime: metav1.Now(),
	}
	if len(referencingClaws) > 0 {
		inUse.Status = metav1.ConditionTrue
		inUse.Reason = "ClawsReferencing"
		inUse.Message = fmt.Sprintf("Referenced by %d Claw(s): %v", len(referencingClaws), referencingClaws)
	} else {
		inUse.Status = metav1.ConditionFalse
		inUse.Reason = "NoReferences"
		inUse.Message = "No Claw resources reference this channel"
	}
	apimeta.SetStatusCondition(&channel.Status.Conditions, inUse)

	return r.Status().Update(ctx, channel)
}

func (r *ClawChannelReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&clawv1alpha1.ClawChannel{}).
		Complete(r)
}
```

**Note:** Add `"time"` to the imports.

### Step 6: Register in `cmd/operator/main.go`

Replace the TODO comment with:

```go
	if err := (&controller.ClawChannelReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "ClawChannel")
		os.Exit(1)
	}
```

### Step 7: Register in `internal/controller/suite_test.go`

After the ClawReconciler setup, add:

```go
	if err := (&ClawChannelReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		panic("failed to set up ClawChannelReconciler: " + err.Error())
	}
```

### Step 8: Run tests to verify they pass

Run: `KUBEBUILDER_ASSETS=$PWD/bin/k8s/k8s/1.35.0-linux-amd64 go test ./internal/controller/ -v -count=1`
Expected: ALL PASS.

### Step 9: Commit

```bash
git add api/v1alpha1/channel_types.go internal/controller/channel_controller.go internal/controller/channel_controller_test.go internal/controller/suite_test.go cmd/operator/main.go
git commit -m "feat: implement ClawChannel controller with finalizer and deletion protection"
```

---

## Task 5: PVC Reclaim Policy (Retain/Delete)

**Files:**
- Modify: `internal/controller/claw_controller.go` (implement reclaim logic in `handleDeletion`)
- Test: `internal/controller/claw_controller_test.go` (add reclaim tests)

### Step 1: Write failing tests

```go
func TestClawReconciler_PVCReclaimDelete(t *testing.T) {
	ns := fmt.Sprintf("test-reclaim-del-%d", time.Now().UnixNano())
	createNamespace(t, ns)

	clawName := "test-claw-reclaim-del"
	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{
			Name:      clawName,
			Namespace: ns,
		},
		Spec: clawv1alpha1.ClawSpec{
			Runtime: clawv1alpha1.RuntimePicoClaw,
			Persistence: &clawv1alpha1.PersistenceSpec{
				ReclaimPolicy: clawv1alpha1.ReclaimDelete,
			},
		},
	}

	if err := k8sClient.Create(ctx, claw); err != nil {
		t.Fatalf("failed to create Claw: %v", err)
	}

	// Wait for finalizer.
	nn := types.NamespacedName{Name: clawName, Namespace: ns}
	var latest clawv1alpha1.Claw
	waitForCondition(t, testTimeout, testInterval, func() (bool, error) {
		if err := k8sClient.Get(ctx, nn, &latest); err != nil {
			if client.IgnoreNotFound(err) == nil {
				return false, nil
			}
			return false, err
		}
		return controllerutil.ContainsFinalizer(&latest, clawFinalizer), nil
	})

	// Pre-create a PVC that matches the StatefulSet naming pattern.
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("session-%s-0", clawName),
			Namespace: ns,
			Labels: map[string]string{
				"claw.prismer.ai/instance": clawName,
			},
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: resource.MustParse("1Gi"),
				},
			},
		},
	}
	if err := k8sClient.Create(ctx, pvc); err != nil {
		t.Fatalf("failed to create PVC: %v", err)
	}

	// Delete the Claw.
	if err := k8sClient.Delete(ctx, &latest); err != nil {
		t.Fatalf("failed to delete Claw: %v", err)
	}

	// Wait for Claw to be fully deleted.
	waitForCondition(t, testTimeout, testInterval, func() (bool, error) {
		var fetched clawv1alpha1.Claw
		err := k8sClient.Get(ctx, nn, &fetched)
		if err != nil {
			if client.IgnoreNotFound(err) == nil {
				return true, nil
			}
			return false, err
		}
		return false, nil
	})

	// Verify the PVC was deleted (ReclaimPolicy=Delete).
	var fetchedPVC corev1.PersistentVolumeClaim
	err := k8sClient.Get(ctx, types.NamespacedName{
		Name:      fmt.Sprintf("session-%s-0", clawName),
		Namespace: ns,
	}, &fetchedPVC)
	if err == nil {
		t.Fatal("expected PVC to be deleted with ReclaimPolicy=Delete, but it still exists")
	}
	if client.IgnoreNotFound(err) != nil {
		t.Fatalf("unexpected error checking PVC: %v", err)
	}
}

func TestClawReconciler_PVCReclaimRetain(t *testing.T) {
	ns := fmt.Sprintf("test-reclaim-ret-%d", time.Now().UnixNano())
	createNamespace(t, ns)

	clawName := "test-claw-reclaim-ret"
	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{
			Name:      clawName,
			Namespace: ns,
		},
		Spec: clawv1alpha1.ClawSpec{
			Runtime: clawv1alpha1.RuntimePicoClaw,
			Persistence: &clawv1alpha1.PersistenceSpec{
				ReclaimPolicy: clawv1alpha1.ReclaimRetain,
			},
		},
	}

	if err := k8sClient.Create(ctx, claw); err != nil {
		t.Fatalf("failed to create Claw: %v", err)
	}

	// Wait for finalizer.
	nn := types.NamespacedName{Name: clawName, Namespace: ns}
	var latest clawv1alpha1.Claw
	waitForCondition(t, testTimeout, testInterval, func() (bool, error) {
		if err := k8sClient.Get(ctx, nn, &latest); err != nil {
			if client.IgnoreNotFound(err) == nil {
				return false, nil
			}
			return false, err
		}
		return controllerutil.ContainsFinalizer(&latest, clawFinalizer), nil
	})

	// Pre-create a PVC.
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("workspace-%s-0", clawName),
			Namespace: ns,
			Labels: map[string]string{
				"claw.prismer.ai/instance": clawName,
			},
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: resource.MustParse("1Gi"),
				},
			},
		},
	}
	if err := k8sClient.Create(ctx, pvc); err != nil {
		t.Fatalf("failed to create PVC: %v", err)
	}

	// Delete the Claw.
	if err := k8sClient.Delete(ctx, &latest); err != nil {
		t.Fatalf("failed to delete Claw: %v", err)
	}

	// Wait for Claw to be fully deleted.
	waitForCondition(t, testTimeout, testInterval, func() (bool, error) {
		var fetched clawv1alpha1.Claw
		err := k8sClient.Get(ctx, nn, &fetched)
		if err != nil {
			if client.IgnoreNotFound(err) == nil {
				return true, nil
			}
			return false, err
		}
		return false, nil
	})

	// Verify PVC still exists (Retain policy).
	var fetchedPVC corev1.PersistentVolumeClaim
	if err := k8sClient.Get(ctx, types.NamespacedName{
		Name:      fmt.Sprintf("workspace-%s-0", clawName),
		Namespace: ns,
	}, &fetchedPVC); err != nil {
		t.Fatalf("expected PVC to be retained with ReclaimPolicy=Retain, but got error: %v", err)
	}
}
```

**Note:** Add `"k8s.io/apimachinery/pkg/api/resource"` to the test file imports.

### Step 2: Run tests to verify they fail

Run: `KUBEBUILDER_ASSETS=$PWD/bin/k8s/k8s/1.35.0-linux-amd64 go test ./internal/controller/ -run TestClawReconciler_PVCReclaim -v -count=1`
Expected: FAIL (no PVC deletion logic in `handleDeletion`).

### Step 3: Implement reclaim logic in `handleDeletion`

Replace the TODO block in `handleDeletion` in `claw_controller.go`:

```go
	// Execute reclaim policy.
	switch reclaimPolicy {
	case "Delete":
		if err := r.deleteClawPVCs(ctx, claw); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to delete PVCs: %w", err)
		}
	case "Retain":
		logger.Info("retaining PVCs per reclaim policy", "name", claw.Name)
	case "Archive":
		// TODO: implement Archive (snapshot PVCs, then delete)
		logger.Info("Archive reclaim policy not yet implemented, retaining PVCs", "name", claw.Name)
	}
```

Add the `deleteClawPVCs` method:

```go
// deleteClawPVCs deletes all PVCs labeled with the Claw instance name.
func (r *ClawReconciler) deleteClawPVCs(ctx context.Context, claw *clawv1alpha1.Claw) error {
	logger := log.FromContext(ctx)

	var pvcList corev1.PersistentVolumeClaimList
	if err := r.List(ctx, &pvcList,
		client.InNamespace(claw.Namespace),
		client.MatchingLabels{"claw.prismer.ai/instance": claw.Name},
	); err != nil {
		return fmt.Errorf("failed to list PVCs: %w", err)
	}

	for i := range pvcList.Items {
		logger.Info("deleting PVC", "name", pvcList.Items[i].Name, "namespace", pvcList.Items[i].Namespace)
		if err := r.Delete(ctx, &pvcList.Items[i]); err != nil {
			if client.IgnoreNotFound(err) != nil {
				return fmt.Errorf("failed to delete PVC %s: %w", pvcList.Items[i].Name, err)
			}
		}
	}

	return nil
}
```

### Step 4: Run tests to verify they pass

Run: `KUBEBUILDER_ASSETS=$PWD/bin/k8s/k8s/1.35.0-linux-amd64 go test ./internal/controller/ -v -count=1`
Expected: ALL PASS.

### Step 5: Commit

```bash
git add internal/controller/claw_controller.go internal/controller/claw_controller_test.go
git commit -m "feat: implement PVC reclaim policy (Retain/Delete) on Claw deletion"
```

---

## Verification

After all 5 tasks are complete, run the full verification:

```bash
# Full test suite
KUBEBUILDER_ASSETS=$PWD/bin/k8s/k8s/1.35.0-linux-amd64 go test -race -cover ./...

# Static analysis
go vet ./...

# Build check
go build ./...
```

Expected:
- All tests PASS
- Controller coverage > 70%
- Runtime coverage > 95%
- go vet clean
- go build clean
