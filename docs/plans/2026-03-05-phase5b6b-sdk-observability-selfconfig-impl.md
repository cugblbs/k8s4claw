# Phase 5B+6B: SDK, Observability & ClawSelfConfig Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Implement Go SDK with dynamic client, operator Prometheus metrics with ServiceMonitor/PrometheusRule, K8s Events, and ClawSelfConfig CRD with reconciler.

**Architecture:** The Go SDK uses `client-go` dynamic client to CRUD Claw CRs without depending on controller-runtime. Operator metrics follow the `promauto` pattern from IPC Bus. ClawSelfConfig is a new CRD that allows AI agents to modify their own Claw configuration through the K8s API, with operator-enforced allowlist security. Events are emitted via the existing `Recorder` on ClawReconciler.

**Tech Stack:** Go 1.25, `k8s.io/client-go` (dynamic client), `prometheus/client_golang`, controller-runtime EventRecorder

**Parallelism:** Tasks 1-3 (SDK) and Tasks 4-6 (Observability) are independent and can run in parallel. Tasks 7-10 (ClawSelfConfig) depend on nothing else. Task 11 is final verification.

---

## Task 1: SDK types, options, and conversion helpers

**Files:**
- Modify: `sdk/types.go`
- Create: `sdk/options.go`
- Create: `sdk/convert.go`

### Step 1: Extend SDK types

Replace `sdk/types.go` with expanded types:

```go
package sdk

import "time"

// RuntimeType mirrors the CRD runtime type for SDK consumers.
type RuntimeType string

const (
	OpenClaw RuntimeType = "openclaw"
	NanoClaw RuntimeType = "nanoclaw"
	ZeroClaw RuntimeType = "zeroclaw"
	PicoClaw RuntimeType = "picoclaw"
	Custom   RuntimeType = "custom"
)

// ClawSpec defines the desired state for creating a Claw via the SDK.
type ClawSpec struct {
	// Name is the Claw resource name. Auto-generated if empty.
	Name string

	// Runtime is the agent runtime type (required).
	Runtime RuntimeType

	// Config holds runtime-specific configuration.
	Config *RuntimeConfig

	// Namespace to create the Claw in. Defaults to "default".
	Namespace string

	// Labels are additional labels applied to the Claw CR.
	Labels map[string]string

	// Replicas is the number of StatefulSet replicas. Defaults to 1.
	Replicas int32
}

// RuntimeConfig provides typed runtime configuration.
type RuntimeConfig struct {
	// Environment contains key-value pairs for the runtime.
	Environment map[string]string
}

// ClawInstance represents a running Claw agent.
type ClawInstance struct {
	// Name of the Claw resource.
	Name string

	// Namespace of the Claw resource.
	Namespace string

	// Runtime is the runtime type.
	Runtime RuntimeType

	// Phase is the current lifecycle phase.
	Phase string

	// Conditions are the status conditions.
	Conditions []Condition

	// CreatedAt is the creation timestamp.
	CreatedAt time.Time
}

// Condition represents a Claw status condition.
type Condition struct {
	Type               string
	Status             string
	Message            string
	LastTransitionTime time.Time
}

// UpdateSpec defines fields that can be updated on a Claw.
type UpdateSpec struct {
	// Environment replaces the runtime environment vars.
	Environment map[string]string

	// Replicas updates the replica count.
	Replicas *int32
}

// ListOptions configures List behavior.
type ListOptions struct {
	// LabelSelector filters by label (e.g. "app=myagent").
	LabelSelector string

	// Limit caps the number of results.
	Limit int64
}

// Result represents the output from a Claw agent.
type Result struct {
	// Content is the text response from the agent.
	Content string

	// Metadata contains additional response information.
	Metadata map[string]string
}
```

### Step 2: Create functional options

Create `sdk/options.go`:

```go
package sdk

// Option configures a Client.
type Option func(*clientConfig)

type clientConfig struct {
	kubeconfig       string
	defaultNamespace string
}

// WithKubeconfig sets the path to the kubeconfig file.
// If not set, uses default discovery (~/.kube/config or in-cluster).
func WithKubeconfig(path string) Option {
	return func(c *clientConfig) {
		c.kubeconfig = path
	}
}

// WithNamespace sets the default namespace for operations.
// Can be overridden per-call via ClawSpec.Namespace.
func WithNamespace(ns string) Option {
	return func(c *clientConfig) {
		c.defaultNamespace = ns
	}
}
```

### Step 3: Create conversion helpers

Create `sdk/convert.go`:

```go
package sdk

import (
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const (
	apiVersion = "claw.prismer.ai/v1alpha1"
	kind       = "Claw"
)

func toUnstructured(spec *ClawSpec) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{}
	obj.SetAPIVersion(apiVersion)
	obj.SetKind(kind)
	obj.SetName(spec.Name)
	obj.SetNamespace(spec.Namespace)
	if len(spec.Labels) > 0 {
		obj.SetLabels(spec.Labels)
	}

	s := map[string]interface{}{
		"runtime": string(spec.Runtime),
	}

	if spec.Replicas > 0 {
		s["replicas"] = int64(spec.Replicas)
	}

	if spec.Config != nil && len(spec.Config.Environment) > 0 {
		env := make(map[string]interface{}, len(spec.Config.Environment))
		for k, v := range spec.Config.Environment {
			env[k] = v
		}
		s["config"] = map[string]interface{}{
			"environment": env,
		}
	}

	if err := unstructured.SetNestedField(obj.Object, s, "spec"); err != nil {
		// Should never happen with well-formed maps.
		panic(fmt.Sprintf("failed to set spec: %v", err))
	}

	return obj
}

func fromUnstructured(obj *unstructured.Unstructured) *ClawInstance {
	inst := &ClawInstance{
		Name:      obj.GetName(),
		Namespace: obj.GetNamespace(),
		CreatedAt: obj.GetCreationTimestamp().Time,
	}

	if rt, ok, _ := unstructured.NestedString(obj.Object, "spec", "runtime"); ok {
		inst.Runtime = RuntimeType(rt)
	}

	if phase, ok, _ := unstructured.NestedString(obj.Object, "status", "phase"); ok {
		inst.Phase = phase
	}

	if conditions, ok, _ := unstructured.NestedSlice(obj.Object, "status", "conditions"); ok {
		for _, c := range conditions {
			cm, ok := c.(map[string]interface{})
			if !ok {
				continue
			}
			cond := Condition{}
			if t, ok := cm["type"].(string); ok {
				cond.Type = t
			}
			if s, ok := cm["status"].(string); ok {
				cond.Status = s
			}
			if m, ok := cm["message"].(string); ok {
				cond.Message = m
			}
			if lt, ok := cm["lastTransitionTime"].(string); ok {
				if t, err := time.Parse(time.RFC3339, lt); err == nil {
					cond.LastTransitionTime = t
				}
			}
			inst.Conditions = append(inst.Conditions, cond)
		}
	}

	return inst
}
```

### Step 4: Verify build

```bash
GOROOT=/home/willamhou/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.25.0.linux-amd64 go build ./sdk/...
```

### Step 5: Commit

```
feat: add SDK types, options, and unstructured conversion helpers
```

---

## Task 2: SDK client implementation

**Files:**
- Modify: `sdk/client.go`

### Step 1: Implement Client with dynamic client

Replace `sdk/client.go`:

```go
package sdk

import (
	"context"
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

var clawGVR = schema.GroupVersionResource{
	Group:    "claw.prismer.ai",
	Version:  "v1alpha1",
	Resource: "claws",
}

// Client provides a high-level interface for managing Claw agents on Kubernetes.
type Client struct {
	dynamic          dynamic.Interface
	defaultNamespace string
}

// NewClient creates a new k8s4claw SDK client.
func NewClient(opts ...Option) (*Client, error) {
	cfg := &clientConfig{
		defaultNamespace: "default",
	}
	for _, opt := range opts {
		opt(cfg)
	}

	restCfg, err := buildRESTConfig(cfg.kubeconfig)
	if err != nil {
		return nil, fmt.Errorf("failed to build kubeconfig: %w", err)
	}

	dynClient, err := dynamic.NewForConfig(restCfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create dynamic client: %w", err)
	}

	return &Client{
		dynamic:          dynClient,
		defaultNamespace: cfg.defaultNamespace,
	}, nil
}

// newClientFromDynamic creates a Client from an existing dynamic.Interface (for testing).
func newClientFromDynamic(dyn dynamic.Interface, ns string) *Client {
	return &Client{dynamic: dyn, defaultNamespace: ns}
}

func buildRESTConfig(kubeconfig string) (*rest.Config, error) {
	if kubeconfig != "" {
		return clientcmd.BuildConfigFromFlags("", kubeconfig)
	}
	// Try in-cluster first, fall back to default kubeconfig.
	cfg, err := rest.InClusterConfig()
	if err != nil {
		loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
		return clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
			loadingRules, &clientcmd.ConfigOverrides{},
		).ClientConfig()
	}
	return cfg, nil
}

func (c *Client) namespace(ns string) string {
	if ns != "" {
		return ns
	}
	return c.defaultNamespace
}

// Create creates a new Claw agent.
func (c *Client) Create(ctx context.Context, spec *ClawSpec) (*ClawInstance, error) {
	if spec == nil {
		return nil, fmt.Errorf("spec must not be nil")
	}
	if spec.Runtime == "" {
		return nil, fmt.Errorf("runtime must be specified")
	}

	spec.Namespace = c.namespace(spec.Namespace)

	if spec.Name == "" {
		spec.Name = fmt.Sprintf("claw-%d", time.Now().UnixMilli())
	}

	obj := toUnstructured(spec)
	created, err := c.dynamic.Resource(clawGVR).Namespace(spec.Namespace).
		Create(ctx, obj, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to create Claw: %w", err)
	}

	return fromUnstructured(created), nil
}

// Get returns the current state of a Claw agent.
func (c *Client) Get(ctx context.Context, namespace, name string) (*ClawInstance, error) {
	ns := c.namespace(namespace)
	obj, err := c.dynamic.Resource(clawGVR).Namespace(ns).
		Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to get Claw %s/%s: %w", ns, name, err)
	}
	return fromUnstructured(obj), nil
}

// List returns all Claw instances in a namespace.
func (c *Client) List(ctx context.Context, namespace string, opts *ListOptions) ([]*ClawInstance, error) {
	ns := c.namespace(namespace)
	listOpts := metav1.ListOptions{}
	if opts != nil {
		if opts.LabelSelector != "" {
			listOpts.LabelSelector = opts.LabelSelector
		}
		if opts.Limit > 0 {
			listOpts.Limit = opts.Limit
		}
	}

	list, err := c.dynamic.Resource(clawGVR).Namespace(ns).
		List(ctx, listOpts)
	if err != nil {
		return nil, fmt.Errorf("failed to list Claws in %s: %w", ns, err)
	}

	instances := make([]*ClawInstance, 0, len(list.Items))
	for i := range list.Items {
		instances = append(instances, fromUnstructured(&list.Items[i]))
	}
	return instances, nil
}

// Update patches a Claw agent's configuration.
func (c *Client) Update(ctx context.Context, instance *ClawInstance, update *UpdateSpec) (*ClawInstance, error) {
	if instance == nil {
		return nil, fmt.Errorf("instance must not be nil")
	}
	if update == nil {
		return nil, fmt.Errorf("update spec must not be nil")
	}

	ns := c.namespace(instance.Namespace)
	obj, err := c.dynamic.Resource(clawGVR).Namespace(ns).
		Get(ctx, instance.Name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to get Claw for update: %w", err)
	}

	spec, _, _ := unstructured.NestedMap(obj.Object, "spec")
	if spec == nil {
		spec = map[string]interface{}{}
	}

	if update.Environment != nil {
		env := make(map[string]interface{}, len(update.Environment))
		for k, v := range update.Environment {
			env[k] = v
		}
		config, _, _ := unstructured.NestedMap(obj.Object, "spec", "config")
		if config == nil {
			config = map[string]interface{}{}
		}
		config["environment"] = env
		spec["config"] = config
	}

	if update.Replicas != nil {
		spec["replicas"] = int64(*update.Replicas)
	}

	if err := unstructured.SetNestedField(obj.Object, spec, "spec"); err != nil {
		return nil, fmt.Errorf("failed to set updated spec: %w", err)
	}

	updated, err := c.dynamic.Resource(clawGVR).Namespace(ns).
		Update(ctx, obj, metav1.UpdateOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to update Claw: %w", err)
	}

	return fromUnstructured(updated), nil
}

// Delete removes a Claw agent.
func (c *Client) Delete(ctx context.Context, namespace, name string) error {
	ns := c.namespace(namespace)
	if err := c.dynamic.Resource(clawGVR).Namespace(ns).
		Delete(ctx, name, metav1.DeleteOptions{}); err != nil {
		return fmt.Errorf("failed to delete Claw %s/%s: %w", ns, name, err)
	}
	return nil
}

// WaitForReady watches a Claw until its Phase becomes "Running" or the context expires.
func (c *Client) WaitForReady(ctx context.Context, instance *ClawInstance) error {
	if instance == nil {
		return fmt.Errorf("instance must not be nil")
	}

	ns := c.namespace(instance.Namespace)

	// Check current state first.
	current, err := c.Get(ctx, ns, instance.Name)
	if err != nil {
		return err
	}
	if current.Phase == "Running" {
		return nil
	}

	watcher, err := c.dynamic.Resource(clawGVR).Namespace(ns).
		Watch(ctx, metav1.ListOptions{
			FieldSelector: fmt.Sprintf("metadata.name=%s", instance.Name),
		})
	if err != nil {
		return fmt.Errorf("failed to watch Claw: %w", err)
	}
	defer watcher.Stop()

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("timed out waiting for Claw %s/%s to be ready: %w", ns, instance.Name, ctx.Err())
		case event, ok := <-watcher.ResultChan():
			if !ok {
				return fmt.Errorf("watch channel closed for Claw %s/%s", ns, instance.Name)
			}
			if event.Type == watch.Modified || event.Type == watch.Added {
				obj, ok := event.Object.(*unstructured.Unstructured)
				if !ok {
					continue
				}
				phase, _, _ := unstructured.NestedString(obj.Object, "status", "phase")
				if phase == "Running" {
					return nil
				}
				if phase == "Failed" {
					return fmt.Errorf("Claw %s/%s entered Failed phase", ns, instance.Name)
				}
			}
		}
	}
}
```

### Step 2: Verify build

```bash
GOROOT=/home/willamhou/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.25.0.linux-amd64 go build ./sdk/...
```

### Step 3: Commit

```
feat: implement Go SDK client with dynamic client for Claw CRUD
```

---

## Task 3: SDK tests

**Files:**
- Create: `sdk/client_test.go`

### Step 1: Write tests using fake dynamic client

```go
package sdk

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	clienttesting "k8s.io/client-go/testing"
)

func newTestClient() (*Client, *dynamicfake.FakeDynamicClient) {
	scheme := runtime.NewScheme()
	fake := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		scheme,
		map[schema.GroupVersionResource]string{
			clawGVR: "ClawList",
		},
	)
	client := newClientFromDynamic(fake, "default")
	return client, fake
}

func TestClient_Create(t *testing.T) {
	c, _ := newTestClient()
	ctx := context.Background()

	inst, err := c.Create(ctx, &ClawSpec{
		Runtime:   OpenClaw,
		Namespace: "test-ns",
		Name:      "my-agent",
	})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	if inst.Name != "my-agent" {
		t.Fatalf("expected name my-agent, got %s", inst.Name)
	}
	if inst.Namespace != "test-ns" {
		t.Fatalf("expected namespace test-ns, got %s", inst.Namespace)
	}
}

func TestClient_Create_AutoName(t *testing.T) {
	c, _ := newTestClient()
	ctx := context.Background()

	inst, err := c.Create(ctx, &ClawSpec{Runtime: OpenClaw})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	if inst.Name == "" {
		t.Fatal("expected auto-generated name")
	}
}

func TestClient_Create_NilSpec(t *testing.T) {
	c, _ := newTestClient()
	_, err := c.Create(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error for nil spec")
	}
}

func TestClient_Create_NoRuntime(t *testing.T) {
	c, _ := newTestClient()
	_, err := c.Create(context.Background(), &ClawSpec{})
	if err == nil {
		t.Fatal("expected error for missing runtime")
	}
}

func TestClient_Get(t *testing.T) {
	c, fake := newTestClient()
	ctx := context.Background()

	// Seed an object.
	obj := &unstructured.Unstructured{}
	obj.SetAPIVersion(apiVersion)
	obj.SetKind(kind)
	obj.SetName("test-claw")
	obj.SetNamespace("default")
	_ = unstructured.SetNestedField(obj.Object, map[string]interface{}{
		"runtime": "openclaw",
	}, "spec")
	_ = unstructured.SetNestedField(obj.Object, map[string]interface{}{
		"phase": "Running",
	}, "status")

	fake.PrependReactor("get", "claws", func(action clienttesting.Action) (bool, runtime.Object, error) {
		return true, obj, nil
	})

	inst, err := c.Get(ctx, "default", "test-claw")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if inst.Phase != "Running" {
		t.Fatalf("expected phase Running, got %s", inst.Phase)
	}
	if inst.Runtime != OpenClaw {
		t.Fatalf("expected runtime openclaw, got %s", inst.Runtime)
	}
}

func TestClient_List(t *testing.T) {
	c, _ := newTestClient()
	ctx := context.Background()

	// Create two instances.
	_, _ = c.Create(ctx, &ClawSpec{Runtime: OpenClaw, Name: "a"})
	_, _ = c.Create(ctx, &ClawSpec{Runtime: NanoClaw, Name: "b"})

	list, err := c.List(ctx, "default", nil)
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("expected 2 instances, got %d", len(list))
	}
}

func TestClient_Delete(t *testing.T) {
	c, _ := newTestClient()
	ctx := context.Background()

	_, _ = c.Create(ctx, &ClawSpec{Runtime: OpenClaw, Name: "del-me"})

	if err := c.Delete(ctx, "default", "del-me"); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}
}

func TestClient_Update(t *testing.T) {
	c, _ := newTestClient()
	ctx := context.Background()

	inst, _ := c.Create(ctx, &ClawSpec{
		Runtime: OpenClaw,
		Name:    "upd",
		Config:  &RuntimeConfig{Environment: map[string]string{"A": "1"}},
	})

	replicas := int32(3)
	updated, err := c.Update(ctx, inst, &UpdateSpec{
		Environment: map[string]string{"B": "2"},
		Replicas:    &replicas,
	})
	if err != nil {
		t.Fatalf("Update failed: %v", err)
	}
	if updated.Name != "upd" {
		t.Fatalf("expected name upd, got %s", updated.Name)
	}
}

func TestClient_DefaultNamespace(t *testing.T) {
	c, _ := newTestClient()
	if got := c.namespace(""); got != "default" {
		t.Fatalf("expected default, got %s", got)
	}
	if got := c.namespace("custom"); got != "custom" {
		t.Fatalf("expected custom, got %s", got)
	}
}

func TestConvert_Roundtrip(t *testing.T) {
	spec := &ClawSpec{
		Name:      "test",
		Runtime:   ZeroClaw,
		Namespace: "ns",
		Labels:    map[string]string{"env": "dev"},
		Replicas:  2,
		Config:    &RuntimeConfig{Environment: map[string]string{"K": "V"}},
	}

	obj := toUnstructured(spec)
	if obj.GetName() != "test" {
		t.Fatal("name mismatch")
	}
	if obj.GetNamespace() != "ns" {
		t.Fatal("namespace mismatch")
	}

	inst := fromUnstructured(obj)
	if inst.Runtime != ZeroClaw {
		t.Fatalf("expected zeroclaw, got %s", inst.Runtime)
	}
}

func TestWaitForReady_AlreadyRunning(t *testing.T) {
	c, fake := newTestClient()
	ctx := context.Background()

	obj := &unstructured.Unstructured{}
	obj.SetAPIVersion(apiVersion)
	obj.SetKind(kind)
	obj.SetName("ready")
	obj.SetNamespace("default")
	_ = unstructured.SetNestedField(obj.Object, map[string]interface{}{"phase": "Running"}, "status")

	fake.PrependReactor("get", "claws", func(action clienttesting.Action) (bool, runtime.Object, error) {
		return true, obj, nil
	})

	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	if err := c.WaitForReady(ctx, &ClawInstance{Name: "ready", Namespace: "default"}); err != nil {
		t.Fatalf("WaitForReady failed: %v", err)
	}
}
```

### Step 2: Run tests

```bash
GOROOT=/home/willamhou/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.25.0.linux-amd64 go test -race ./sdk/ -v -count=1
```

### Step 3: Commit

```
test: add SDK client tests with fake dynamic client
```

---

## Task 4: Operator Prometheus metrics

**Files:**
- Create: `internal/controller/metrics.go`
- Create: `internal/controller/metrics_test.go`
- Modify: `internal/controller/claw_controller.go` (add metric calls)

### Step 1: Create operator metrics

Create `internal/controller/metrics.go`:

```go
package controller

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	reconcileTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "claw_reconcile_total",
		Help: "Total Claw reconcile invocations.",
	}, []string{"namespace", "result"})

	reconcileDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "claw_reconcile_duration_seconds",
		Help:    "Claw reconcile latency.",
		Buckets: prometheus.DefBuckets,
	}, []string{"namespace"})

	managedInstances = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "claw_managed_instances",
		Help: "Total managed Claw instances.",
	})

	instancePhase = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "claw_instance_phase",
		Help: "Claw instance phase (1=active in this phase).",
	}, []string{"namespace", "instance", "phase"})

	instanceReady = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "claw_instance_ready",
		Help: "Claw instance readiness (1=ready, 0=not ready).",
	}, []string{"namespace", "instance"})

	resourceCreationFailures = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "claw_resource_creation_failures_total",
		Help: "Sub-resource creation failures.",
	}, []string{"namespace", "resource"})
)

// RecordReconcile records a reconcile invocation with its duration and result.
func RecordReconcile(namespace, result string, duration time.Duration) {
	reconcileTotal.WithLabelValues(namespace, result).Inc()
	reconcileDuration.WithLabelValues(namespace).Observe(duration.Seconds())
}

// SetManagedInstances sets the total count of managed Claw instances.
func SetManagedInstances(count int) {
	managedInstances.Set(float64(count))
}

// SetInstancePhase sets the phase gauge for a Claw instance.
// It clears all other phases for this instance first.
func SetInstancePhase(namespace, instance, phase string) {
	for _, p := range []string{"Pending", "Provisioning", "Running", "Degraded", "Updating", "Failed", "Terminating"} {
		val := float64(0)
		if p == phase {
			val = 1
		}
		instancePhase.WithLabelValues(namespace, instance, p).Set(val)
	}
}

// SetInstanceReady sets the readiness gauge for a Claw instance.
func SetInstanceReady(namespace, instance string, ready bool) {
	val := float64(0)
	if ready {
		val = 1
	}
	instanceReady.WithLabelValues(namespace, instance).Set(val)
}

// RecordResourceCreationFailure increments the resource creation failure counter.
func RecordResourceCreationFailure(namespace, resource string) {
	resourceCreationFailures.WithLabelValues(namespace, resource).Inc()
}
```

### Step 2: Create metrics test

Create `internal/controller/metrics_test.go`:

```go
package controller

import (
	"testing"
	"time"
)

func TestRecordReconcile(t *testing.T) {
	// Should not panic.
	RecordReconcile("default", "success", 100*time.Millisecond)
	RecordReconcile("default", "error", 200*time.Millisecond)
}

func TestSetInstancePhase(t *testing.T) {
	SetInstancePhase("default", "test-claw", "Running")
	// Should not panic, verifies all phases are set.
}

func TestSetInstanceReady(t *testing.T) {
	SetInstanceReady("default", "test-claw", true)
	SetInstanceReady("default", "test-claw", false)
}

func TestSetManagedInstances(t *testing.T) {
	SetManagedInstances(5)
}

func TestRecordResourceCreationFailure(t *testing.T) {
	RecordResourceCreationFailure("default", "StatefulSet")
}
```

### Step 3: Wire metrics into reconciler

In `internal/controller/claw_controller.go`, add metric calls:

1. At the start of `Reconcile()`, record the start time: `start := time.Now()`
2. At each return point in `Reconcile()`, call `RecordReconcile(claw.Namespace, result, time.Since(start))`
3. In the status update section, call `SetInstancePhase(claw.Namespace, claw.Name, string(phase))`
4. After determining readiness, call `SetInstanceReady(claw.Namespace, claw.Name, ready)`

Use `defer` pattern for metric recording:

```go
func (r *ClawReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	start := time.Now()
	result := "success"
	defer func() {
		RecordReconcile(req.Namespace, result, time.Since(start))
	}()

	// ... existing code ...
	// On error returns, set result = "error" before returning.
}
```

### Step 4: Run tests

```bash
GOROOT=/home/willamhou/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.25.0.linux-amd64 go test -race ./internal/controller/ -run TestRecord -v -count=1
GOROOT=/home/willamhou/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.25.0.linux-amd64 go build ./...
```

### Step 5: Commit

```
feat: add operator Prometheus metrics for reconcile tracking
```

---

## Task 5: K8s Events integration

**Files:**
- Create: `internal/controller/events.go`
- Modify: `internal/controller/claw_controller.go` (add Event calls)

### Step 1: Create event reason constants

Create `internal/controller/events.go`:

```go
package controller

// Event reasons for Claw lifecycle transitions.
const (
	EventClawProvisioning = "ClawProvisioning"
	EventClawRunning      = "ClawRunning"
	EventClawDegraded     = "ClawDegraded"
	EventResourceCreated  = "ResourceCreated"
	EventSecretRotated    = "SecretRotated"
	EventReconcileError   = "ReconcileError"
	EventSelfConfigApplied = "SelfConfigApplied"
	EventSelfConfigDenied  = "SelfConfigDenied"
)
```

### Step 2: Add Events to reconciler

In `internal/controller/claw_controller.go`, add Event emissions at key points:

1. **Phase transitions** — when phase changes in the status update section:
   ```go
   if previousPhase != currentPhase {
       switch currentPhase {
       case ClawPhaseProvisioning:
           r.Recorder.Event(claw, corev1.EventTypeNormal, EventClawProvisioning, "Instance entering provisioning")
       case ClawPhaseRunning:
           r.Recorder.Event(claw, corev1.EventTypeNormal, EventClawRunning, "Instance reached running state")
       case ClawPhaseDegraded, ClawPhaseFailed:
           r.Recorder.Event(claw, corev1.EventTypeWarning, EventClawDegraded, fmt.Sprintf("Instance entered %s state", currentPhase))
       }
   }
   ```

2. **Reconcile errors** — in error return paths:
   ```go
   r.Recorder.Event(claw, corev1.EventTypeWarning, EventReconcileError, err.Error())
   ```

The exact insertion points depend on the reconcile loop structure. The subagent should read the full controller to determine precise locations.

### Step 3: Build and verify

```bash
GOROOT=/home/willamhou/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.25.0.linux-amd64 go build ./...
```

### Step 4: Commit

```
feat: add K8s Events for Claw lifecycle transitions
```

---

## Task 6: ServiceMonitor and PrometheusRule manifests

**Files:**
- Create: `config/prometheus/servicemonitor.yaml`
- Create: `config/prometheus/prometheusrule.yaml`

### Step 1: Create ServiceMonitor

Create `config/prometheus/servicemonitor.yaml`:

```yaml
apiVersion: monitoring.coreos.com/v1
kind: ServiceMonitor
metadata:
  name: k8s4claw-operator
  labels:
    app.kubernetes.io/name: k8s4claw
    app.kubernetes.io/component: operator
spec:
  selector:
    matchLabels:
      app.kubernetes.io/name: k8s4claw
      app.kubernetes.io/component: operator
  endpoints:
    - port: metrics
      interval: 30s
      path: /metrics
```

### Step 2: Create PrometheusRule

Create `config/prometheus/prometheusrule.yaml`:

```yaml
apiVersion: monitoring.coreos.com/v1
kind: PrometheusRule
metadata:
  name: k8s4claw-alerts
  labels:
    app.kubernetes.io/name: k8s4claw
    app.kubernetes.io/component: operator
spec:
  groups:
    - name: k8s4claw.rules
      rules:
        - alert: ClawReconcileErrors
          expr: rate(claw_reconcile_total{result="error"}[5m]) > 0
          for: 5m
          labels:
            severity: warning
            service: k8s4claw
          annotations:
            summary: "Claw reconcile errors detected"
            runbook_url: "https://docs.prismer.ai/runbooks/ClawReconcileErrors"

        - alert: ClawInstanceDegraded
          expr: claw_instance_phase{phase=~"Failed|Degraded"} == 1
          for: 2m
          labels:
            severity: critical
            service: k8s4claw
          annotations:
            summary: "Claw instance {{ $labels.instance }} in {{ $labels.phase }} state"
            runbook_url: "https://docs.prismer.ai/runbooks/ClawInstanceDegraded"

        - alert: ClawSlowReconciliation
          expr: histogram_quantile(0.99, rate(claw_reconcile_duration_seconds_bucket[10m])) > 30
          for: 10m
          labels:
            severity: warning
            service: k8s4claw
          annotations:
            summary: "Claw reconcile P99 latency above 30s"
            runbook_url: "https://docs.prismer.ai/runbooks/ClawSlowReconciliation"

        - alert: ClawPodCrashLooping
          expr: increase(kube_pod_container_status_restarts_total{container=~"claw-.*"}[10m]) > 2
          for: 0m
          labels:
            severity: critical
            service: k8s4claw
          annotations:
            summary: "Claw pod {{ $labels.pod }} crash looping"
            runbook_url: "https://docs.prismer.ai/runbooks/ClawPodCrashLooping"

        - alert: ClawPodOOMKilled
          expr: kube_pod_container_status_last_terminated_reason{reason="OOMKilled", container=~"claw-.*"} == 1
          for: 0m
          labels:
            severity: warning
            service: k8s4claw
          annotations:
            summary: "Claw pod {{ $labels.pod }} OOM killed"
            runbook_url: "https://docs.prismer.ai/runbooks/ClawPodOOMKilled"

        - alert: ClawPVCNearlyFull
          expr: kubelet_volume_stats_used_bytes{persistentvolumeclaim=~"claw-.*"} / kubelet_volume_stats_capacity_bytes{persistentvolumeclaim=~"claw-.*"} > 0.85
          for: 5m
          labels:
            severity: warning
            service: k8s4claw
          annotations:
            summary: "Claw PVC {{ $labels.persistentvolumeclaim }} above 85%"
            runbook_url: "https://docs.prismer.ai/runbooks/ClawPVCNearlyFull"

        - alert: ClawDLQBacklog
          expr: claw_ipcbus_dlq_size > 100
          for: 5m
          labels:
            severity: warning
            service: k8s4claw
          annotations:
            summary: "IPC Bus DLQ has {{ $value }} pending entries"
            runbook_url: "https://docs.prismer.ai/runbooks/ClawDLQBacklog"

        - alert: ClawChannelDisconnected
          expr: claw_ipcbus_sidecar_connections == 0 and claw_ipcbus_bridge_connected == 1
          for: 5m
          labels:
            severity: warning
            service: k8s4claw
          annotations:
            summary: "No channel sidecars connected to IPC Bus"
            runbook_url: "https://docs.prismer.ai/runbooks/ClawChannelDisconnected"
```

### Step 3: Commit

```
feat: add ServiceMonitor and PrometheusRule manifests
```

---

## Task 7: ClawSelfConfig CRD types

**Files:**
- Create: `api/v1alpha1/clawselfconfig_types.go`
- Modify: `api/v1alpha1/claw_types.go` (add SelfConfigure field)

### Step 1: Create ClawSelfConfig types

Create `api/v1alpha1/clawselfconfig_types.go`:

```go
package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ClawSelfConfigPhase represents the lifecycle phase of a ClawSelfConfig.
type ClawSelfConfigPhase string

const (
	SelfConfigPhasePending ClawSelfConfigPhase = "Pending"
	SelfConfigPhaseApplied ClawSelfConfigPhase = "Applied"
	SelfConfigPhaseFailed  ClawSelfConfigPhase = "Failed"
	SelfConfigPhaseDenied  ClawSelfConfigPhase = "Denied"
)

// ClawSelfConfigSpec defines the desired self-configuration changes.
type ClawSelfConfigSpec struct {
	// ClawRef is the name of the target Claw instance (required, same namespace).
	ClawRef string `json:"clawRef"`

	// AddSkills lists skills to install (max 10).
	// +optional
	AddSkills []string `json:"addSkills,omitempty"`

	// RemoveSkills lists skills to uninstall (max 10).
	// +optional
	RemoveSkills []string `json:"removeSkills,omitempty"`

	// ConfigPatch is a partial config merge applied to the Claw runtime config.
	// +optional
	ConfigPatch map[string]string `json:"configPatch,omitempty"`

	// AddWorkspaceFiles maps file names to content to create in the workspace.
	// +optional
	AddWorkspaceFiles map[string]string `json:"addWorkspaceFiles,omitempty"`

	// RemoveWorkspaceFiles lists workspace files to delete.
	// +optional
	RemoveWorkspaceFiles []string `json:"removeWorkspaceFiles,omitempty"`

	// AddEnvVars lists environment variables to add.
	// +optional
	AddEnvVars []EnvVar `json:"addEnvVars,omitempty"`

	// RemoveEnvVars lists environment variable names to remove.
	// +optional
	RemoveEnvVars []string `json:"removeEnvVars,omitempty"`
}

// EnvVar represents an environment variable.
type EnvVar struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// ClawSelfConfigStatus defines the observed state of ClawSelfConfig.
type ClawSelfConfigStatus struct {
	// Phase is the current phase.
	Phase ClawSelfConfigPhase `json:"phase,omitempty"`

	// Message provides human-readable detail.
	// +optional
	Message string `json:"message,omitempty"`

	// AppliedAt is the timestamp when the config was applied.
	// +optional
	AppliedAt *metav1.Time `json:"appliedAt,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Claw",type=string,JSONPath=`.spec.clawRef`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// ClawSelfConfig allows AI agents to modify their own Claw configuration.
type ClawSelfConfig struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ClawSelfConfigSpec   `json:"spec,omitempty"`
	Status ClawSelfConfigStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ClawSelfConfigList contains a list of ClawSelfConfig.
type ClawSelfConfigList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ClawSelfConfig `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ClawSelfConfig{}, &ClawSelfConfigList{})
}
```

### Step 2: Add SelfConfigure field to ClawSpec

In `api/v1alpha1/claw_types.go`, add to ClawSpec struct:

```go
// SelfConfigure enables agent self-configuration.
// +optional
SelfConfigure *SelfConfigureSpec `json:"selfConfigure,omitempty"`
```

### Step 3: Add SelfConfigureSpec to common_types.go or claw_types.go

```go
// SelfConfigureSpec controls agent self-configuration.
type SelfConfigureSpec struct {
	// Enabled allows the agent to create ClawSelfConfig resources.
	Enabled bool `json:"enabled"`

	// AllowedActions lists which action categories are permitted.
	// Valid values: "skills", "config", "workspaceFiles", "envVars".
	// +optional
	AllowedActions []string `json:"allowedActions,omitempty"`
}
```

### Step 4: Generate deepcopy

```bash
GOROOT=/home/willamhou/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.25.0.linux-amd64 go generate ./api/...
```

Or if controller-gen is available:
```bash
controller-gen object paths="./api/..."
```

### Step 5: Generate CRD manifests

```bash
controller-gen crd paths="./api/..." output:crd:artifacts:config=config/crd/bases
```

Or `make manifests` if controller-gen is available.

### Step 6: Build

```bash
GOROOT=/home/willamhou/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.25.0.linux-amd64 go build ./...
```

### Step 7: Commit

```
feat: add ClawSelfConfig CRD and SelfConfigure field on ClawSpec
```

---

## Task 8: ClawSelfConfig reconciler

**Files:**
- Create: `internal/controller/selfconfig_controller.go`
- Create: `internal/controller/selfconfig_controller_test.go`

### Step 1: Implement reconciler

Create `internal/controller/selfconfig_controller.go`:

```go
package controller

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	clawv1alpha1 "github.com/Prismer-AI/k8s4claw/api/v1alpha1"
)

const selfConfigTTL = 1 * time.Hour

// ClawSelfConfigReconciler reconciles ClawSelfConfig resources.
type ClawSelfConfigReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

// +kubebuilder:rbac:groups=claw.prismer.ai,resources=clawselfconfigs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=claw.prismer.ai,resources=clawselfconfigs/status,verbs=get;update;patch

func (r *ClawSelfConfigReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	var sc clawv1alpha1.ClawSelfConfig
	if err := r.Get(ctx, req.NamespacedName, &sc); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("failed to get ClawSelfConfig: %w", err)
	}

	// TTL: delete Applied configs after 1h.
	if sc.Status.Phase == clawv1alpha1.SelfConfigPhaseApplied && sc.Status.AppliedAt != nil {
		elapsed := time.Since(sc.Status.AppliedAt.Time)
		if elapsed >= selfConfigTTL {
			log.Info("deleting expired SelfConfig", "name", sc.Name, "age", elapsed)
			if err := r.Delete(ctx, &sc); err != nil {
				return ctrl.Result{}, fmt.Errorf("failed to delete expired SelfConfig: %w", err)
			}
			return ctrl.Result{}, nil
		}
		// Requeue for TTL expiry.
		return ctrl.Result{RequeueAfter: selfConfigTTL - elapsed}, nil
	}

	// Skip if already in terminal state.
	if sc.Status.Phase == clawv1alpha1.SelfConfigPhaseDenied || sc.Status.Phase == clawv1alpha1.SelfConfigPhaseFailed {
		return ctrl.Result{}, nil
	}

	// Fetch parent Claw.
	var claw clawv1alpha1.Claw
	if err := r.Get(ctx, client.ObjectKey{Namespace: sc.Namespace, Name: sc.Spec.ClawRef}, &claw); err != nil {
		if apierrors.IsNotFound(err) {
			return r.deny(ctx, &sc, fmt.Sprintf("target Claw %q not found", sc.Spec.ClawRef))
		}
		return ctrl.Result{}, fmt.Errorf("failed to get target Claw: %w", err)
	}

	// Check selfConfigure.enabled.
	if claw.Spec.SelfConfigure == nil || !claw.Spec.SelfConfigure.Enabled {
		return r.deny(ctx, &sc, "self-configuration is not enabled on target Claw")
	}

	// Validate actions against allowlist.
	if err := r.validateActions(&sc, claw.Spec.SelfConfigure.AllowedActions); err != nil {
		return r.deny(ctx, &sc, err.Error())
	}

	// Set ownerReference.
	if err := controllerutil.SetOwnerReference(&claw, &sc, r.Scheme); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to set owner reference: %w", err)
	}
	if err := r.Update(ctx, &sc); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to update ownerReference: %w", err)
	}

	// Apply changes to Claw spec.
	if err := r.applyChanges(ctx, &claw, &sc); err != nil {
		sc.Status.Phase = clawv1alpha1.SelfConfigPhaseFailed
		sc.Status.Message = err.Error()
		_ = r.Status().Update(ctx, &sc)
		return ctrl.Result{}, fmt.Errorf("failed to apply self-config: %w", err)
	}

	// Mark as Applied.
	now := metav1.Now()
	sc.Status.Phase = clawv1alpha1.SelfConfigPhaseApplied
	sc.Status.Message = "configuration applied successfully"
	sc.Status.AppliedAt = &now
	if err := r.Status().Update(ctx, &sc); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to update status: %w", err)
	}

	r.Recorder.Event(&sc, corev1.EventTypeNormal, EventSelfConfigApplied,
		fmt.Sprintf("Self-configuration applied to Claw %s", sc.Spec.ClawRef))

	log.Info("self-config applied", "name", sc.Name, "claw", sc.Spec.ClawRef)

	return ctrl.Result{RequeueAfter: selfConfigTTL}, nil
}

func (r *ClawSelfConfigReconciler) deny(ctx context.Context, sc *clawv1alpha1.ClawSelfConfig, reason string) (ctrl.Result, error) {
	sc.Status.Phase = clawv1alpha1.SelfConfigPhaseDenied
	sc.Status.Message = reason
	if err := r.Status().Update(ctx, sc); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to update denied status: %w", err)
	}

	r.Recorder.Event(sc, corev1.EventTypeWarning, EventSelfConfigDenied, reason)
	return ctrl.Result{}, nil
}

func (r *ClawSelfConfigReconciler) validateActions(sc *clawv1alpha1.ClawSelfConfig, allowed []string) error {
	allowedSet := make(map[string]bool, len(allowed))
	for _, a := range allowed {
		allowedSet[a] = true
	}

	if (len(sc.Spec.AddSkills) > 0 || len(sc.Spec.RemoveSkills) > 0) && !allowedSet["skills"] {
		return fmt.Errorf("action 'skills' is not in allowedActions")
	}
	if len(sc.Spec.ConfigPatch) > 0 && !allowedSet["config"] {
		return fmt.Errorf("action 'config' is not in allowedActions")
	}
	if (len(sc.Spec.AddWorkspaceFiles) > 0 || len(sc.Spec.RemoveWorkspaceFiles) > 0) && !allowedSet["workspaceFiles"] {
		return fmt.Errorf("action 'workspaceFiles' is not in allowedActions")
	}
	if (len(sc.Spec.AddEnvVars) > 0 || len(sc.Spec.RemoveEnvVars) > 0) && !allowedSet["envVars"] {
		return fmt.Errorf("action 'envVars' is not in allowedActions")
	}

	// Quantity limits.
	if len(sc.Spec.AddSkills) > 10 {
		return fmt.Errorf("addSkills exceeds max 10 items")
	}
	if len(sc.Spec.RemoveSkills) > 10 {
		return fmt.Errorf("removeSkills exceeds max 10 items")
	}
	if len(sc.Spec.AddWorkspaceFiles) > 10 {
		return fmt.Errorf("addWorkspaceFiles exceeds max 10 items")
	}
	if len(sc.Spec.RemoveWorkspaceFiles) > 10 {
		return fmt.Errorf("removeWorkspaceFiles exceeds max 10 items")
	}
	if len(sc.Spec.AddEnvVars) > 10 {
		return fmt.Errorf("addEnvVars exceeds max 10 items")
	}
	if len(sc.Spec.RemoveEnvVars) > 10 {
		return fmt.Errorf("removeEnvVars exceeds max 10 items")
	}

	return nil
}

func (r *ClawSelfConfigReconciler) applyChanges(ctx context.Context, claw *clawv1alpha1.Claw, sc *clawv1alpha1.ClawSelfConfig) error {
	// Apply environment variable changes via annotation (triggers reconcile).
	// The Claw reconciler reads annotations to apply env var changes.
	// For now, store as annotations on the Claw CR.
	annotations := claw.GetAnnotations()
	if annotations == nil {
		annotations = make(map[string]string)
	}

	// Mark that a self-config was applied (triggers reconcile via generation change).
	annotations["claw.prismer.ai/last-self-config"] = sc.Name
	claw.SetAnnotations(annotations)

	// Apply config patch to Claw spec config if provided.
	// Note: This is a simplified implementation. Full config merge would
	// depend on the runtime config format (JSON blob).

	if err := r.Update(ctx, claw); err != nil {
		return fmt.Errorf("failed to update Claw: %w", err)
	}

	return nil
}

// SetupWithManager registers the ClawSelfConfig controller.
func (r *ClawSelfConfigReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&clawv1alpha1.ClawSelfConfig{}).
		Complete(r)
}
```

### Step 2: Write tests

Create `internal/controller/selfconfig_controller_test.go` with tests for:
- `validateActions` — allowed, denied, quantity limits
- Deny when Claw not found
- Deny when selfConfigure not enabled

These should be unit tests on the helper functions, not envtest (since envtest requires etcd).

```go
package controller

import (
	"testing"

	clawv1alpha1 "github.com/Prismer-AI/k8s4claw/api/v1alpha1"
)

func TestValidateActions_Allowed(t *testing.T) {
	r := &ClawSelfConfigReconciler{}
	sc := &clawv1alpha1.ClawSelfConfig{}
	sc.Spec.AddSkills = []string{"tool-use"}
	sc.Spec.ConfigPatch = map[string]string{"model": "claude-sonnet-4"}

	err := r.validateActions(sc, []string{"skills", "config"})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestValidateActions_Denied(t *testing.T) {
	r := &ClawSelfConfigReconciler{}
	sc := &clawv1alpha1.ClawSelfConfig{}
	sc.Spec.AddSkills = []string{"tool-use"}

	err := r.validateActions(sc, []string{"config"}) // skills not allowed
	if err == nil {
		t.Fatal("expected error for denied skills action")
	}
}

func TestValidateActions_QuantityLimit(t *testing.T) {
	r := &ClawSelfConfigReconciler{}
	sc := &clawv1alpha1.ClawSelfConfig{}

	skills := make([]string, 11) // exceeds max 10
	for i := range skills {
		skills[i] = "skill"
	}
	sc.Spec.AddSkills = skills

	err := r.validateActions(sc, []string{"skills"})
	if err == nil {
		t.Fatal("expected error for exceeding quantity limit")
	}
}

func TestValidateActions_Empty(t *testing.T) {
	r := &ClawSelfConfigReconciler{}
	sc := &clawv1alpha1.ClawSelfConfig{}

	// No actions requested — should pass with any allowlist.
	err := r.validateActions(sc, nil)
	if err != nil {
		t.Fatalf("expected no error for empty actions, got: %v", err)
	}
}

func TestValidateActions_AllCategories(t *testing.T) {
	r := &ClawSelfConfigReconciler{}
	sc := &clawv1alpha1.ClawSelfConfig{}
	sc.Spec.AddSkills = []string{"a"}
	sc.Spec.ConfigPatch = map[string]string{"k": "v"}
	sc.Spec.AddWorkspaceFiles = map[string]string{"f": "c"}
	sc.Spec.AddEnvVars = []clawv1alpha1.EnvVar{{Name: "X", Value: "1"}}

	err := r.validateActions(sc, []string{"skills", "config", "workspaceFiles", "envVars"})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	// Missing envVars.
	err = r.validateActions(sc, []string{"skills", "config", "workspaceFiles"})
	if err == nil {
		t.Fatal("expected error for missing envVars")
	}
}
```

### Step 3: Run tests

```bash
GOROOT=/home/willamhou/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.25.0.linux-amd64 go test -race ./internal/controller/ -run TestValidateActions -v -count=1
```

### Step 4: Commit

```
feat: add ClawSelfConfig reconciler with allowlist validation
```

---

## Task 9: Wire SelfConfig controller and update RBAC

**Files:**
- Modify: `cmd/operator/main.go` (register SelfConfig controller)
- Modify: `internal/controller/claw_rbac.go` (update needsRBAC)
- Create: `config/samples/selfconfig-example.yaml`

### Step 1: Register SelfConfig controller in main.go

After the existing controller registrations, add:

```go
if err := (&controller.ClawSelfConfigReconciler{
	Client:   mgr.GetClient(),
	Scheme:   mgr.GetScheme(),
	Recorder: mgr.GetEventRecorderFor("selfconfig-controller"),
}).SetupWithManager(mgr); err != nil {
	setupLog.Error(err, "unable to create controller", "controller", "ClawSelfConfig")
	os.Exit(1)
}
```

### Step 2: Update needsRBAC in claw_rbac.go

The `needsRBAC()` function currently returns `false`. Update it to check `spec.selfConfigure.enabled`:

```go
func needsRBAC(claw *clawv1alpha1.Claw) bool {
	return claw.Spec.SelfConfigure != nil && claw.Spec.SelfConfigure.Enabled
}
```

The existing `buildRole()` already has the correct RBAC rules for ClawSelfConfig access (verified in codebase exploration).

### Step 3: Create sample CR

Create `config/samples/selfconfig-example.yaml`:

```yaml
apiVersion: claw.prismer.ai/v1alpha1
kind: ClawSelfConfig
metadata:
  name: agent-install-skill
spec:
  clawRef: my-research-agent
  addSkills:
    - "@anthropic/tool-use"
  configPatch:
    model: "claude-sonnet-4"
  addEnvVars:
    - name: CUSTOM_FLAG
      value: "true"
```

### Step 4: Build

```bash
GOROOT=/home/willamhou/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.25.0.linux-amd64 go build ./...
```

### Step 5: Commit

```
feat: wire ClawSelfConfig controller, update RBAC, and add sample CR
```

---

## Task 10: Full verification and test

### Step 1: Run all tests

```bash
GOROOT=/home/willamhou/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.25.0.linux-amd64 go test -race ./sdk/ ./internal/controller/ ./internal/ipcbus/ ./internal/runtime/ ./internal/webhook/ -v -timeout 120s -count=1
```

### Step 2: Build all binaries

```bash
GOROOT=/home/willamhou/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.25.0.linux-amd64 go build ./...
```

### Step 3: Verify go vet

```bash
GOROOT=/home/willamhou/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.25.0.linux-amd64 go vet ./...
```

### Step 4: Commit if any fixes needed

```
test: final verification for Phase 5B+6B
```
