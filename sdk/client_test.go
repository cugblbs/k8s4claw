package sdk

import (
	"context"
	"testing"
	"time"

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
