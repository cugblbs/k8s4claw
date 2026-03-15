package sdk

import (
	"context"
	"fmt"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
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

func TestWaitForReady_NilInstance(t *testing.T) {
	c, _ := newTestClient()
	err := c.WaitForReady(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error for nil instance")
	}
}

func TestWaitForReady_GetFailure(t *testing.T) {
	c, fake := newTestClient()
	fake.PrependReactor("get", "claws", func(action clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, fmt.Errorf("api unavailable")
	})

	err := c.WaitForReady(context.Background(), &ClawInstance{Name: "x", Namespace: "default"})
	if err == nil {
		t.Fatal("expected error when Get fails")
	}
}

func TestWaitForReady_FailedPhase(t *testing.T) {
	c, fake := newTestClient()

	pendingObj := &unstructured.Unstructured{}
	pendingObj.SetAPIVersion(apiVersion)
	pendingObj.SetKind(kind)
	pendingObj.SetName("fail-agent")
	pendingObj.SetNamespace("default")
	_ = unstructured.SetNestedField(pendingObj.Object, map[string]interface{}{"phase": "Pending"}, "status")

	fake.PrependReactor("get", "claws", func(action clienttesting.Action) (bool, runtime.Object, error) {
		return true, pendingObj, nil
	})

	watcher := watch.NewFake()
	fake.PrependWatchReactor("claws", clienttesting.DefaultWatchReactor(watcher, nil))

	go func() {
		failedObj := pendingObj.DeepCopy()
		_ = unstructured.SetNestedField(failedObj.Object, map[string]interface{}{"phase": "Failed"}, "status")
		watcher.Modify(failedObj)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := c.WaitForReady(ctx, &ClawInstance{Name: "fail-agent", Namespace: "default"})
	if err == nil {
		t.Fatal("expected error for Failed phase")
	}
}

func TestWaitForReady_WatchTransitionToRunning(t *testing.T) {
	c, fake := newTestClient()

	pendingObj := &unstructured.Unstructured{}
	pendingObj.SetAPIVersion(apiVersion)
	pendingObj.SetKind(kind)
	pendingObj.SetName("watch-agent")
	pendingObj.SetNamespace("default")
	_ = unstructured.SetNestedField(pendingObj.Object, map[string]interface{}{"phase": "Pending"}, "status")

	fake.PrependReactor("get", "claws", func(action clienttesting.Action) (bool, runtime.Object, error) {
		return true, pendingObj, nil
	})

	watcher := watch.NewFake()
	fake.PrependWatchReactor("claws", clienttesting.DefaultWatchReactor(watcher, nil))

	go func() {
		runningObj := pendingObj.DeepCopy()
		_ = unstructured.SetNestedField(runningObj.Object, map[string]interface{}{"phase": "Running"}, "status")
		watcher.Modify(runningObj)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := c.WaitForReady(ctx, &ClawInstance{Name: "watch-agent", Namespace: "default"}); err != nil {
		t.Fatalf("WaitForReady failed: %v", err)
	}
}

func TestWaitForReady_ContextTimeout(t *testing.T) {
	c, fake := newTestClient()

	pendingObj := &unstructured.Unstructured{}
	pendingObj.SetAPIVersion(apiVersion)
	pendingObj.SetKind(kind)
	pendingObj.SetName("timeout-agent")
	pendingObj.SetNamespace("default")
	_ = unstructured.SetNestedField(pendingObj.Object, map[string]interface{}{"phase": "Pending"}, "status")

	fake.PrependReactor("get", "claws", func(action clienttesting.Action) (bool, runtime.Object, error) {
		return true, pendingObj, nil
	})

	watcher := watch.NewFake()
	fake.PrependWatchReactor("claws", clienttesting.DefaultWatchReactor(watcher, nil))

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	err := c.WaitForReady(ctx, &ClawInstance{Name: "timeout-agent", Namespace: "default"})
	if err == nil {
		t.Fatal("expected timeout error")
	}
}

func TestWaitForReady_WatchChannelClosed(t *testing.T) {
	c, fake := newTestClient()

	pendingObj := &unstructured.Unstructured{}
	pendingObj.SetAPIVersion(apiVersion)
	pendingObj.SetKind(kind)
	pendingObj.SetName("close-agent")
	pendingObj.SetNamespace("default")
	_ = unstructured.SetNestedField(pendingObj.Object, map[string]interface{}{"phase": "Pending"}, "status")

	fake.PrependReactor("get", "claws", func(action clienttesting.Action) (bool, runtime.Object, error) {
		return true, pendingObj, nil
	})

	watcher := watch.NewFake()
	fake.PrependWatchReactor("claws", clienttesting.DefaultWatchReactor(watcher, nil))

	go func() {
		watcher.Stop()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := c.WaitForReady(ctx, &ClawInstance{Name: "close-agent", Namespace: "default"})
	if err == nil {
		t.Fatal("expected error when watch channel is closed")
	}
}

func TestClient_Update_NilInstance(t *testing.T) {
	c, _ := newTestClient()
	_, err := c.Update(context.Background(), nil, &UpdateSpec{})
	if err == nil {
		t.Fatal("expected error for nil instance")
	}
}

func TestClient_Update_NilSpec(t *testing.T) {
	c, _ := newTestClient()
	_, err := c.Update(context.Background(), &ClawInstance{Name: "x"}, nil)
	if err == nil {
		t.Fatal("expected error for nil update spec")
	}
}

func TestClient_Update_EnvironmentOnly(t *testing.T) {
	c, _ := newTestClient()
	ctx := context.Background()

	inst, _ := c.Create(ctx, &ClawSpec{
		Runtime: OpenClaw,
		Name:    "env-only",
	})

	updated, err := c.Update(ctx, inst, &UpdateSpec{
		Environment: map[string]string{"KEY": "VAL"},
	})
	if err != nil {
		t.Fatalf("Update failed: %v", err)
	}
	if updated.Name != "env-only" {
		t.Fatalf("expected name env-only, got %s", updated.Name)
	}
}

func TestClient_Update_ReplicasOnly(t *testing.T) {
	c, _ := newTestClient()
	ctx := context.Background()

	inst, _ := c.Create(ctx, &ClawSpec{
		Runtime: OpenClaw,
		Name:    "rep-only",
	})

	replicas := int32(5)
	updated, err := c.Update(ctx, inst, &UpdateSpec{
		Replicas: &replicas,
	})
	if err != nil {
		t.Fatalf("Update failed: %v", err)
	}
	if updated.Name != "rep-only" {
		t.Fatalf("expected name rep-only, got %s", updated.Name)
	}
}

func TestClient_Create_WithLabels(t *testing.T) {
	c, _ := newTestClient()
	ctx := context.Background()

	inst, err := c.Create(ctx, &ClawSpec{
		Runtime: NanoClaw,
		Name:    "labeled",
		Labels:  map[string]string{"app": "test", "tier": "backend"},
	})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	if inst.Name != "labeled" {
		t.Fatalf("expected name labeled, got %s", inst.Name)
	}
}

func TestClient_Create_WithReplicas(t *testing.T) {
	c, _ := newTestClient()
	ctx := context.Background()

	inst, err := c.Create(ctx, &ClawSpec{
		Runtime:  ZeroClaw,
		Name:     "multi-rep",
		Replicas: 3,
	})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	if inst.Name != "multi-rep" {
		t.Fatalf("expected name multi-rep, got %s", inst.Name)
	}
}

func TestClient_Create_WithEnvironment(t *testing.T) {
	c, _ := newTestClient()
	ctx := context.Background()

	inst, err := c.Create(ctx, &ClawSpec{
		Runtime: PicoClaw,
		Name:    "env-agent",
		Config:  &RuntimeConfig{Environment: map[string]string{"DB": "postgres"}},
	})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	if inst.Runtime != PicoClaw {
		t.Fatalf("expected runtime picoclaw, got %s", inst.Runtime)
	}
}

func TestClient_Get_WithConditions(t *testing.T) {
	c, fake := newTestClient()

	obj := &unstructured.Unstructured{}
	obj.SetAPIVersion(apiVersion)
	obj.SetKind(kind)
	obj.SetName("cond-agent")
	obj.SetNamespace("default")
	_ = unstructured.SetNestedField(obj.Object, map[string]interface{}{
		"runtime": "openclaw",
	}, "spec")
	_ = unstructured.SetNestedField(obj.Object, map[string]interface{}{
		"phase": "Running",
		"conditions": []interface{}{
			map[string]interface{}{
				"type":               "Ready",
				"status":             "True",
				"message":            "agent is ready",
				"lastTransitionTime": "2026-01-15T10:00:00Z",
			},
			map[string]interface{}{
				"type":   "Scheduled",
				"status": "True",
			},
		},
	}, "status")

	fake.PrependReactor("get", "claws", func(action clienttesting.Action) (bool, runtime.Object, error) {
		return true, obj, nil
	})

	inst, err := c.Get(context.Background(), "default", "cond-agent")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if len(inst.Conditions) != 2 {
		t.Fatalf("expected 2 conditions, got %d", len(inst.Conditions))
	}
	if inst.Conditions[0].Type != "Ready" {
		t.Fatalf("expected condition type Ready, got %s", inst.Conditions[0].Type)
	}
	if inst.Conditions[0].Message != "agent is ready" {
		t.Fatalf("expected message 'agent is ready', got %s", inst.Conditions[0].Message)
	}
	if inst.Conditions[0].LastTransitionTime.IsZero() {
		t.Fatal("expected non-zero lastTransitionTime")
	}
	if inst.Conditions[1].Message != "" {
		t.Fatalf("expected empty message for second condition, got %s", inst.Conditions[1].Message)
	}
}

func TestClient_Get_MissingStatus(t *testing.T) {
	c, fake := newTestClient()

	obj := &unstructured.Unstructured{}
	obj.SetAPIVersion(apiVersion)
	obj.SetKind(kind)
	obj.SetName("no-status")
	obj.SetNamespace("default")
	_ = unstructured.SetNestedField(obj.Object, map[string]interface{}{
		"runtime": "openclaw",
	}, "spec")

	fake.PrependReactor("get", "claws", func(action clienttesting.Action) (bool, runtime.Object, error) {
		return true, obj, nil
	})

	inst, err := c.Get(context.Background(), "default", "no-status")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if inst.Phase != "" {
		t.Fatalf("expected empty phase, got %s", inst.Phase)
	}
	if len(inst.Conditions) != 0 {
		t.Fatalf("expected 0 conditions, got %d", len(inst.Conditions))
	}
}

func TestClient_List_WithLabelSelector(t *testing.T) {
	c, _ := newTestClient()
	ctx := context.Background()

	_, _ = c.Create(ctx, &ClawSpec{Runtime: OpenClaw, Name: "ls-a", Labels: map[string]string{"app": "test"}})
	_, _ = c.Create(ctx, &ClawSpec{Runtime: OpenClaw, Name: "ls-b"})

	list, err := c.List(ctx, "default", &ListOptions{LabelSelector: "app=test"})
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	// Fake client doesn't actually filter, but we exercise the code path.
	_ = list
}

func TestClient_List_Empty(t *testing.T) {
	c, _ := newTestClient()
	list, err := c.List(context.Background(), "default", nil)
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("expected 0 instances, got %d", len(list))
	}
}

func TestClient_List_WithLimit(t *testing.T) {
	c, _ := newTestClient()
	ctx := context.Background()

	_, _ = c.Create(ctx, &ClawSpec{Runtime: OpenClaw, Name: "lim-a"})
	_, _ = c.Create(ctx, &ClawSpec{Runtime: OpenClaw, Name: "lim-b"})

	list, err := c.List(ctx, "default", &ListOptions{Limit: 10})
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("expected 2 instances, got %d", len(list))
	}
}

func TestFromUnstructured_ConditionsMissingFields(t *testing.T) {
	obj := &unstructured.Unstructured{}
	obj.SetAPIVersion(apiVersion)
	obj.SetKind(kind)
	obj.SetName("edge")
	obj.SetNamespace("default")
	_ = unstructured.SetNestedField(obj.Object, map[string]interface{}{
		"runtime": "openclaw",
	}, "spec")
	_ = unstructured.SetNestedField(obj.Object, map[string]interface{}{
		"phase": "Pending",
		"conditions": []interface{}{
			map[string]interface{}{
				"type": "Ready",
				// missing status, message, lastTransitionTime
			},
			"not-a-map", // invalid entry, should be skipped
			map[string]interface{}{
				"type":               "Available",
				"lastTransitionTime": "invalid-time-format",
			},
		},
	}, "status")

	inst := fromUnstructured(obj)
	// "not-a-map" should be skipped
	if len(inst.Conditions) != 2 {
		t.Fatalf("expected 2 conditions, got %d", len(inst.Conditions))
	}
	if inst.Conditions[0].Status != "" {
		t.Fatalf("expected empty status, got %s", inst.Conditions[0].Status)
	}
	// Invalid time should leave zero time
	if !inst.Conditions[1].LastTransitionTime.IsZero() {
		t.Fatal("expected zero time for invalid time format")
	}
}

func TestFromUnstructured_EmptyConditions(t *testing.T) {
	obj := &unstructured.Unstructured{}
	obj.SetAPIVersion(apiVersion)
	obj.SetKind(kind)
	obj.SetName("empty-cond")
	obj.SetNamespace("default")
	_ = unstructured.SetNestedField(obj.Object, map[string]interface{}{
		"runtime": "openclaw",
	}, "spec")
	_ = unstructured.SetNestedField(obj.Object, map[string]interface{}{
		"phase":      "Running",
		"conditions": []interface{}{},
	}, "status")

	inst := fromUnstructured(obj)
	if len(inst.Conditions) != 0 {
		t.Fatalf("expected 0 conditions, got %d", len(inst.Conditions))
	}
}

func TestOptions(t *testing.T) {
	cfg := &clientConfig{}
	WithKubeconfig("/path/to/kubeconfig")(cfg)
	if cfg.kubeconfig != "/path/to/kubeconfig" {
		t.Fatalf("expected kubeconfig /path/to/kubeconfig, got %s", cfg.kubeconfig)
	}

	WithNamespace("custom-ns")(cfg)
	if cfg.defaultNamespace != "custom-ns" {
		t.Fatalf("expected namespace custom-ns, got %s", cfg.defaultNamespace)
	}
}

func TestClient_Delete_NotFound(t *testing.T) {
	c, _ := newTestClient()
	err := c.Delete(context.Background(), "default", "nonexistent")
	if err == nil {
		t.Fatal("expected error deleting nonexistent resource")
	}
}

func TestClient_Get_NotFound(t *testing.T) {
	c, _ := newTestClient()
	_, err := c.Get(context.Background(), "default", "nonexistent")
	if err == nil {
		t.Fatal("expected error getting nonexistent resource")
	}
}
