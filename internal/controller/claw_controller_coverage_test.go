package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	clawv1alpha1 "github.com/Prismer-AI/k8s4claw/api/v1alpha1"
	clawruntime "github.com/Prismer-AI/k8s4claw/internal/runtime"
)

// ---------------------------------------------------------------------------
// Unit tests: mergeConfig / deepMerge
// ---------------------------------------------------------------------------

func TestMergeConfig(t *testing.T) {
	tests := []struct {
		name        string
		defaultJSON string
		userJSON    []byte
		wantKeys    map[string]interface{}
		wantErr     bool
	}{
		{
			name:        "nil user JSON returns defaults",
			defaultJSON: `{"gatewayPort":18900,"workspacePath":"/workspace"}`,
			userJSON:    nil,
			wantKeys:    map[string]interface{}{"gatewayPort": float64(18900), "workspacePath": "/workspace"},
		},
		{
			name:        "empty user JSON returns defaults",
			defaultJSON: `{"gatewayPort":18900}`,
			userJSON:    []byte{},
			wantKeys:    map[string]interface{}{"gatewayPort": float64(18900)},
		},
		{
			name:        "user overrides existing key",
			defaultJSON: `{"gatewayPort":18900,"model":"default"}`,
			userJSON:    []byte(`{"model":"claude-sonnet-4"}`),
			wantKeys:    map[string]interface{}{"gatewayPort": float64(18900), "model": "claude-sonnet-4"},
		},
		{
			name:        "user adds new key",
			defaultJSON: `{"gatewayPort":18900}`,
			userJSON:    []byte(`{"customField":"hello"}`),
			wantKeys:    map[string]interface{}{"gatewayPort": float64(18900), "customField": "hello"},
		},
		{
			name:        "invalid default JSON returns error",
			defaultJSON: `{invalid`,
			userJSON:    []byte(`{"key":"val"}`),
			wantErr:     true,
		},
		{
			name:        "invalid user JSON returns error",
			defaultJSON: `{"gatewayPort":18900}`,
			userJSON:    []byte(`{invalid`),
			wantErr:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := mergeConfig(tt.defaultJSON, tt.userJSON)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if tt.wantKeys != nil {
				var parsed map[string]interface{}
				if err := json.Unmarshal([]byte(result), &parsed); err != nil {
					t.Fatalf("result is not valid JSON: %v", err)
				}
				for k, want := range tt.wantKeys {
					got, ok := parsed[k]
					if !ok {
						t.Errorf("missing key %q in merged result", k)
						continue
					}
					if got != want {
						t.Errorf("key %q: got %v, want %v", k, got, want)
					}
				}
			}
		})
	}
}

func TestDeepMerge(t *testing.T) {
	tests := []struct {
		name string
		dst  map[string]interface{}
		src  map[string]interface{}
		want map[string]interface{}
	}{
		{
			name: "simple override",
			dst:  map[string]interface{}{"a": "old"},
			src:  map[string]interface{}{"a": "new"},
			want: map[string]interface{}{"a": "new"},
		},
		{
			name: "add new key",
			dst:  map[string]interface{}{"a": "val"},
			src:  map[string]interface{}{"b": "new"},
			want: map[string]interface{}{"a": "val", "b": "new"},
		},
		{
			name: "nested map merge",
			dst: map[string]interface{}{
				"nested": map[string]interface{}{"x": float64(1), "y": float64(2)},
			},
			src: map[string]interface{}{
				"nested": map[string]interface{}{"y": float64(3), "z": float64(4)},
			},
			want: map[string]interface{}{
				"nested": map[string]interface{}{"x": float64(1), "y": float64(3), "z": float64(4)},
			},
		},
		{
			name: "src map replaces non-map dst value",
			dst:  map[string]interface{}{"key": "string-value"},
			src:  map[string]interface{}{"key": map[string]interface{}{"nested": true}},
			want: map[string]interface{}{"key": map[string]interface{}{"nested": true}},
		},
		{
			name: "src scalar replaces dst map",
			dst:  map[string]interface{}{"key": map[string]interface{}{"nested": true}},
			src:  map[string]interface{}{"key": "scalar"},
			want: map[string]interface{}{"key": "scalar"},
		},
		{
			name: "empty src returns dst",
			dst:  map[string]interface{}{"a": float64(1)},
			src:  map[string]interface{}{},
			want: map[string]interface{}{"a": float64(1)},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := deepMerge(tt.dst, tt.src)

			gotJSON, _ := json.Marshal(got)
			wantJSON, _ := json.Marshal(tt.want)
			if string(gotJSON) != string(wantJSON) {
				t.Errorf("deepMerge:\n  got  %s\n  want %s", gotJSON, wantJSON)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Integration test: ConfigMap with user config merge
// ---------------------------------------------------------------------------

func TestClawReconciler_ConfigMapWithUserConfig(t *testing.T) {
	ns := fmt.Sprintf("test-cm-merge-%d", time.Now().UnixNano())
	createNamespace(t, ns)

	clawName := "test-claw-cm-merge"
	userConfig := `{"model":"claude-sonnet-4","customSetting":true}`
	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{
			Name:      clawName,
			Namespace: ns,
		},
		Spec: clawv1alpha1.ClawSpec{
			Runtime: clawv1alpha1.RuntimeOpenClaw,
			Config:  &apiextensionsv1.JSON{Raw: []byte(userConfig)},
		},
	}

	if err := k8sClient.Create(ctx, claw); err != nil {
		t.Fatalf("failed to create Claw: %v", err)
	}

	// Wait for the ConfigMap to appear.
	cmName := fmt.Sprintf("%s-config", clawName)
	var cm corev1.ConfigMap
	waitForCondition(t, testTimeout, testInterval, func() (bool, error) {
		err := k8sClient.Get(ctx, types.NamespacedName{Name: cmName, Namespace: ns}, &cm)
		if err != nil {
			if client.IgnoreNotFound(err) == nil {
				return false, nil
			}
			return false, err
		}
		return true, nil
	})

	// Parse the merged config.json.
	configJSON, ok := cm.Data["config.json"]
	if !ok {
		t.Fatal("expected config.json key in ConfigMap data")
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(configJSON), &parsed); err != nil {
		t.Fatalf("config.json is not valid JSON: %v", err)
	}

	// Verify user config was merged (user's model should override default).
	if model, ok := parsed["model"]; !ok || model != "claude-sonnet-4" {
		t.Errorf("expected model=claude-sonnet-4, got %v", parsed["model"])
	}
	if cs, ok := parsed["customSetting"]; !ok || cs != true {
		t.Errorf("expected customSetting=true, got %v", parsed["customSetting"])
	}

	// Verify default keys are still present (gatewayPort from OpenClaw defaults).
	if gp, ok := parsed["gatewayPort"]; !ok {
		t.Error("expected gatewayPort from defaults to be present")
	} else if int(gp.(float64)) != 18900 {
		t.Errorf("expected gatewayPort=18900, got %v", gp)
	}
}

// ---------------------------------------------------------------------------
// Integration test: Channel sidecar injection into StatefulSet
// ---------------------------------------------------------------------------

func TestClawReconciler_ChannelSidecarInjected(t *testing.T) {
	ns := fmt.Sprintf("test-ch-inject-%d", time.Now().UnixNano())
	createNamespace(t, ns)

	// Create a ClawChannel.
	channel := &clawv1alpha1.ClawChannel{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-slack-ch",
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

	// Create a Claw that references the channel.
	clawName := "test-claw-ch-inject"
	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{
			Name:      clawName,
			Namespace: ns,
		},
		Spec: clawv1alpha1.ClawSpec{
			Runtime: clawv1alpha1.RuntimeZeroClaw,
			Channels: []clawv1alpha1.ChannelRef{
				{
					Name: "test-slack-ch",
					Mode: clawv1alpha1.ChannelModeInbound,
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
		err := k8sClient.Get(ctx, types.NamespacedName{Name: clawName, Namespace: ns}, &sts)
		if err != nil {
			if client.IgnoreNotFound(err) == nil {
				return false, nil
			}
			return false, err
		}
		return true, nil
	})

	// With NativeSidecarsEnabled=true, the channel sidecar should be in initContainers.
	found := false
	for _, c := range sts.Spec.Template.Spec.InitContainers {
		if c.Name == "channel-test-slack-ch" {
			found = true
			if c.Image != "ghcr.io/prismer-ai/claw-channel-slack:latest" {
				t.Errorf("expected slack sidecar image, got %q", c.Image)
			}
			// Verify ipc-socket volume mount.
			hasIPC := false
			for _, m := range c.VolumeMounts {
				if m.Name == "ipc-socket" {
					hasIPC = true
					break
				}
			}
			if !hasIPC {
				t.Error("expected ipc-socket volume mount on channel sidecar")
			}
			// Verify restartPolicy=Always (native sidecar).
			if c.RestartPolicy == nil || *c.RestartPolicy != corev1.ContainerRestartPolicyAlways {
				t.Error("expected restartPolicy=Always for native sidecar")
			}
			// Verify env vars.
			envMap := make(map[string]string)
			for _, e := range c.Env {
				envMap[e.Name] = e.Value
			}
			if envMap["CHANNEL_NAME"] != "test-slack-ch" {
				t.Errorf("expected CHANNEL_NAME=test-slack-ch, got %q", envMap["CHANNEL_NAME"])
			}
			if envMap["CHANNEL_TYPE"] != "slack" {
				t.Errorf("expected CHANNEL_TYPE=slack, got %q", envMap["CHANNEL_TYPE"])
			}
			if envMap["CHANNEL_MODE"] != "inbound" {
				t.Errorf("expected CHANNEL_MODE=inbound, got %q", envMap["CHANNEL_MODE"])
			}
			break
		}
	}
	if !found {
		t.Error("expected channel sidecar 'channel-test-slack-ch' in initContainers, not found")
	}
}

// ---------------------------------------------------------------------------
// Integration test: Channel sidecar skipped when ClawChannel not found
// ---------------------------------------------------------------------------

func TestClawReconciler_ChannelSidecarSkippedMissing(t *testing.T) {
	ns := fmt.Sprintf("test-ch-skip-%d", time.Now().UnixNano())
	createNamespace(t, ns)

	clawName := "test-claw-ch-skip"
	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{
			Name:      clawName,
			Namespace: ns,
		},
		Spec: clawv1alpha1.ClawSpec{
			Runtime: clawv1alpha1.RuntimePicoClaw,
			Channels: []clawv1alpha1.ChannelRef{
				{
					Name: "nonexistent-channel",
					Mode: clawv1alpha1.ChannelModeBidirectional,
				},
			},
		},
	}
	if err := k8sClient.Create(ctx, claw); err != nil {
		t.Fatalf("failed to create Claw: %v", err)
	}

	// Wait for the StatefulSet to appear (reconciler should still succeed).
	var sts appsv1.StatefulSet
	waitForCondition(t, testTimeout, testInterval, func() (bool, error) {
		err := k8sClient.Get(ctx, types.NamespacedName{Name: clawName, Namespace: ns}, &sts)
		if err != nil {
			if client.IgnoreNotFound(err) == nil {
				return false, nil
			}
			return false, err
		}
		return true, nil
	})

	// Verify no channel sidecar was injected.
	for _, c := range sts.Spec.Template.Spec.InitContainers {
		if c.Name == "channel-nonexistent-channel" {
			t.Error("expected missing channel to be skipped, but sidecar was found")
		}
	}
}

// ---------------------------------------------------------------------------
// Integration test: Channel sidecar skipped on mode incompatibility
// ---------------------------------------------------------------------------

func TestClawReconciler_ChannelSidecarSkippedIncompatible(t *testing.T) {
	ns := fmt.Sprintf("test-ch-incompat-%d", time.Now().UnixNano())
	createNamespace(t, ns)

	// Create an inbound-only channel.
	channel := &clawv1alpha1.ClawChannel{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "inbound-only-ch",
			Namespace: ns,
		},
		Spec: clawv1alpha1.ClawChannelSpec{
			Type: clawv1alpha1.ChannelTypeWebhook,
			Mode: clawv1alpha1.ChannelModeInbound,
		},
	}
	if err := k8sClient.Create(ctx, channel); err != nil {
		t.Fatalf("failed to create ClawChannel: %v", err)
	}

	// Create a Claw requesting bidirectional on that inbound-only channel.
	clawName := "test-claw-ch-incompat"
	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{
			Name:      clawName,
			Namespace: ns,
		},
		Spec: clawv1alpha1.ClawSpec{
			Runtime: clawv1alpha1.RuntimePicoClaw,
			Channels: []clawv1alpha1.ChannelRef{
				{
					Name: "inbound-only-ch",
					Mode: clawv1alpha1.ChannelModeBidirectional,
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
		err := k8sClient.Get(ctx, types.NamespacedName{Name: clawName, Namespace: ns}, &sts)
		if err != nil {
			if client.IgnoreNotFound(err) == nil {
				return false, nil
			}
			return false, err
		}
		return true, nil
	})

	// Verify the incompatible channel sidecar was skipped.
	for _, c := range sts.Spec.Template.Spec.InitContainers {
		if c.Name == "channel-inbound-only-ch" {
			t.Error("expected incompatible channel to be skipped, but sidecar was found")
		}
	}
}

// ---------------------------------------------------------------------------
// Integration test: StatefulSet update path (spec change triggers update)
// ---------------------------------------------------------------------------

func TestClawReconciler_StatefulSetUpdated(t *testing.T) {
	ns := fmt.Sprintf("test-sts-update-%d", time.Now().UnixNano())
	createNamespace(t, ns)

	clawName := "test-claw-sts-upd"
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

	// Wait for the StatefulSet to appear.
	nn := types.NamespacedName{Name: clawName, Namespace: ns}
	var sts appsv1.StatefulSet
	waitForCondition(t, testTimeout, testInterval, func() (bool, error) {
		err := k8sClient.Get(ctx, nn, &sts)
		if err != nil {
			if client.IgnoreNotFound(err) == nil {
				return false, nil
			}
			return false, err
		}
		return true, nil
	})

	// Record initial resourceVersion.
	initialRV := sts.ResourceVersion

	// Update the Claw spec: add credentials to trigger StatefulSet update.
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "upd-creds",
			Namespace: ns,
		},
		Data: map[string][]byte{"KEY": []byte("val")},
	}
	if err := k8sClient.Create(ctx, secret); err != nil {
		t.Fatalf("failed to create Secret: %v", err)
	}

	var latest clawv1alpha1.Claw
	if err := k8sClient.Get(ctx, nn, &latest); err != nil {
		t.Fatalf("failed to re-fetch Claw: %v", err)
	}
	latest.Spec.Credentials = &clawv1alpha1.CredentialSpec{
		SecretRef: &corev1.LocalObjectReference{Name: "upd-creds"},
	}
	if err := k8sClient.Update(ctx, &latest); err != nil {
		t.Fatalf("failed to update Claw: %v", err)
	}

	// Wait for the StatefulSet to be updated (resourceVersion changes).
	waitForCondition(t, testTimeout, testInterval, func() (bool, error) {
		if err := k8sClient.Get(ctx, nn, &sts); err != nil {
			return false, err
		}
		return sts.ResourceVersion != initialRV, nil
	})

	// Verify the runtime container now has envFrom with the secret ref.
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

	found := false
	for _, ef := range runtimeContainer.EnvFrom {
		if ef.SecretRef != nil && ef.SecretRef.Name == "upd-creds" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected envFrom with secretRef 'upd-creds' after update, not found")
	}
}

// ---------------------------------------------------------------------------
// Integration test: Different runtimes produce correct StatefulSets
// ---------------------------------------------------------------------------

func TestClawReconciler_AllRuntimes(t *testing.T) {
	runtimes := []struct {
		name        clawv1alpha1.RuntimeType
		gatewayPort int32
		image       string
		gracePeriod int64
	}{
		{clawv1alpha1.RuntimeNanoClaw, 19000, "ghcr.io/prismer-ai/k8s4claw-nanoclaw:latest", 25},
		{clawv1alpha1.RuntimeZeroClaw, 3000, "ghcr.io/prismer-ai/k8s4claw-zeroclaw:latest", 15},
		{clawv1alpha1.RuntimePicoClaw, 8080, "ghcr.io/prismer-ai/k8s4claw-picoclaw:latest", 12},
	}

	for _, rt := range runtimes {
		t.Run(string(rt.name), func(t *testing.T) {
			ns := fmt.Sprintf("test-rt-%s-%d", rt.name, time.Now().UnixNano())
			createNamespace(t, ns)

			clawName := fmt.Sprintf("test-%s", rt.name)
			claw := &clawv1alpha1.Claw{
				ObjectMeta: metav1.ObjectMeta{
					Name:      clawName,
					Namespace: ns,
				},
				Spec: clawv1alpha1.ClawSpec{
					Runtime: rt.name,
				},
			}
			if err := k8sClient.Create(ctx, claw); err != nil {
				t.Fatalf("failed to create Claw: %v", err)
			}

			// Wait for StatefulSet.
			nn := types.NamespacedName{Name: clawName, Namespace: ns}
			var sts appsv1.StatefulSet
			waitForCondition(t, testTimeout, testInterval, func() (bool, error) {
				err := k8sClient.Get(ctx, nn, &sts)
				if err != nil {
					if client.IgnoreNotFound(err) == nil {
						return false, nil
					}
					return false, err
				}
				return true, nil
			})

			// Verify runtime image.
			var runtimeContainer *corev1.Container
			for i := range sts.Spec.Template.Spec.Containers {
				if sts.Spec.Template.Spec.Containers[i].Name == "runtime" {
					runtimeContainer = &sts.Spec.Template.Spec.Containers[i]
					break
				}
			}
			if runtimeContainer == nil {
				t.Fatal("runtime container not found")
			}
			if runtimeContainer.Image != rt.image {
				t.Errorf("expected image=%q, got %q", rt.image, runtimeContainer.Image)
			}

			// Verify terminationGracePeriodSeconds.
			if sts.Spec.Template.Spec.TerminationGracePeriodSeconds == nil ||
				*sts.Spec.Template.Spec.TerminationGracePeriodSeconds != rt.gracePeriod {
				t.Errorf("expected terminationGracePeriodSeconds=%d, got %v",
					rt.gracePeriod, sts.Spec.Template.Spec.TerminationGracePeriodSeconds)
			}

			// Verify runtime label.
			if sts.Labels["claw.prismer.ai/runtime"] != string(rt.name) {
				t.Errorf("expected runtime label=%q, got %q", rt.name, sts.Labels["claw.prismer.ai/runtime"])
			}

			// Wait for ConfigMap and verify gatewayPort.
			cmName := fmt.Sprintf("%s-config", clawName)
			var cm corev1.ConfigMap
			waitForCondition(t, testTimeout, testInterval, func() (bool, error) {
				err := k8sClient.Get(ctx, types.NamespacedName{Name: cmName, Namespace: ns}, &cm)
				if err != nil {
					if client.IgnoreNotFound(err) == nil {
						return false, nil
					}
					return false, err
				}
				return true, nil
			})

			var parsed map[string]interface{}
			if err := json.Unmarshal([]byte(cm.Data["config.json"]), &parsed); err != nil {
				t.Fatalf("config.json is not valid JSON: %v", err)
			}
			if gp, ok := parsed["gatewayPort"]; !ok || int(gp.(float64)) != int(rt.gatewayPort) {
				t.Errorf("expected gatewayPort=%d, got %v", rt.gatewayPort, parsed["gatewayPort"])
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Integration test: PVC reclaim Archive (stub behavior — should retain)
// ---------------------------------------------------------------------------

func TestClawReconciler_PVCReclaimArchive(t *testing.T) {
	ns := fmt.Sprintf("test-reclaim-arc-%d", time.Now().UnixNano())
	createNamespace(t, ns)

	clawName := "test-claw-reclaim-arc"
	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{
			Name:      clawName,
			Namespace: ns,
		},
		Spec: clawv1alpha1.ClawSpec{
			Runtime: clawv1alpha1.RuntimePicoClaw,
			Persistence: &clawv1alpha1.PersistenceSpec{
				ReclaimPolicy: clawv1alpha1.ReclaimArchive,
			},
		},
	}

	if err := k8sClient.Create(ctx, claw); err != nil {
		t.Fatalf("failed to create Claw: %v", err)
	}

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

	// Pre-create a labeled PVC.
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("session-%s-0", clawName),
			Namespace: ns,
			Labels:    map[string]string{"claw.prismer.ai/instance": clawName},
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")},
			},
		},
	}
	if err := k8sClient.Create(ctx, pvc); err != nil {
		t.Fatalf("failed to create PVC: %v", err)
	}

	// Delete the Claw.
	if err := k8sClient.Get(ctx, nn, &latest); err != nil {
		t.Fatalf("failed to re-fetch Claw: %v", err)
	}
	if err := k8sClient.Delete(ctx, &latest); err != nil {
		t.Fatalf("failed to delete Claw: %v", err)
	}

	// Wait for the Claw to be fully deleted.
	waitForCondition(t, testTimeout, testInterval, func() (bool, error) {
		err := k8sClient.Get(ctx, nn, &clawv1alpha1.Claw{})
		if client.IgnoreNotFound(err) == nil {
			return true, nil
		}
		return false, err
	})

	// Verify PVC still exists (Archive stub retains PVCs).
	if err := k8sClient.Get(ctx, types.NamespacedName{
		Name:      pvc.Name,
		Namespace: ns,
	}, &corev1.PersistentVolumeClaim{}); err != nil {
		t.Fatalf("expected PVC to be retained with Archive policy stub, but got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Integration test: ConfigMap update path (spec change triggers update)
// ---------------------------------------------------------------------------

func TestClawReconciler_ConfigMapUpdated(t *testing.T) {
	ns := fmt.Sprintf("test-cm-upd-%d", time.Now().UnixNano())
	createNamespace(t, ns)

	clawName := "test-claw-cm-upd"
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

	// Wait for ConfigMap to be created.
	cmName := fmt.Sprintf("%s-config", clawName)
	nn := types.NamespacedName{Name: cmName, Namespace: ns}
	var cm corev1.ConfigMap
	waitForCondition(t, testTimeout, testInterval, func() (bool, error) {
		err := k8sClient.Get(ctx, nn, &cm)
		if err != nil {
			if client.IgnoreNotFound(err) == nil {
				return false, nil
			}
			return false, err
		}
		return true, nil
	})
	initialRV := cm.ResourceVersion

	// Update the Claw's config to trigger ConfigMap update path.
	var latest clawv1alpha1.Claw
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: clawName, Namespace: ns}, &latest); err != nil {
		t.Fatalf("failed to re-fetch Claw: %v", err)
	}
	latest.Spec.Config = &apiextensionsv1.JSON{Raw: []byte(`{"model":"claude-sonnet-4"}`)}
	if err := k8sClient.Update(ctx, &latest); err != nil {
		t.Fatalf("failed to update Claw: %v", err)
	}

	// Wait for ConfigMap to be updated (resourceVersion changes).
	waitForCondition(t, testTimeout, testInterval, func() (bool, error) {
		if err := k8sClient.Get(ctx, nn, &cm); err != nil {
			return false, err
		}
		return cm.ResourceVersion != initialRV, nil
	})

	// Verify the ConfigMap now has the user config merged in.
	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(cm.Data["config.json"]), &parsed); err != nil {
		t.Fatalf("config.json is not valid JSON: %v", err)
	}
	if parsed["model"] != "claude-sonnet-4" {
		t.Errorf("expected model=claude-sonnet-4, got %v", parsed["model"])
	}
	// Default gatewayPort should still be present.
	if gp, ok := parsed["gatewayPort"]; !ok || int(gp.(float64)) != 18900 {
		t.Errorf("expected gatewayPort=18900, got %v", parsed["gatewayPort"])
	}
}

// ---------------------------------------------------------------------------
// Integration test: Custom channel with full sidecar spec
// ---------------------------------------------------------------------------

func TestClawReconciler_CustomChannelSidecar(t *testing.T) {
	ns := fmt.Sprintf("test-ch-custom-%d", time.Now().UnixNano())
	createNamespace(t, ns)

	// Create a custom ClawChannel with sidecar spec.
	channel := &clawv1alpha1.ClawChannel{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "custom-adapter",
			Namespace: ns,
		},
		Spec: clawv1alpha1.ClawChannelSpec{
			Type: clawv1alpha1.ChannelTypeCustom,
			Mode: clawv1alpha1.ChannelModeBidirectional,
			Sidecar: &clawv1alpha1.SidecarSpec{
				Image: "my-registry/custom-adapter:v1",
				Env: []corev1.EnvVar{
					{Name: "CUSTOM_VAR", Value: "custom_value"},
				},
				Ports: []corev1.ContainerPort{
					{Name: "http", ContainerPort: 9090, Protocol: corev1.ProtocolTCP},
				},
				Resources: &corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("100m"),
						corev1.ResourceMemory: resource.MustParse("64Mi"),
					},
				},
			},
		},
	}
	if err := k8sClient.Create(ctx, channel); err != nil {
		t.Fatalf("failed to create ClawChannel: %v", err)
	}

	// Create a Claw referencing the custom channel.
	clawName := "test-claw-custom-ch"
	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{
			Name:      clawName,
			Namespace: ns,
		},
		Spec: clawv1alpha1.ClawSpec{
			Runtime: clawv1alpha1.RuntimePicoClaw,
			Channels: []clawv1alpha1.ChannelRef{
				{Name: "custom-adapter", Mode: clawv1alpha1.ChannelModeBidirectional},
			},
		},
	}
	if err := k8sClient.Create(ctx, claw); err != nil {
		t.Fatalf("failed to create Claw: %v", err)
	}

	// Wait for StatefulSet.
	var sts appsv1.StatefulSet
	waitForCondition(t, testTimeout, testInterval, func() (bool, error) {
		err := k8sClient.Get(ctx, types.NamespacedName{Name: clawName, Namespace: ns}, &sts)
		if err != nil {
			if client.IgnoreNotFound(err) == nil {
				return false, nil
			}
			return false, err
		}
		return true, nil
	})

	// Find the custom channel sidecar in initContainers.
	var sidecar *corev1.Container
	for i := range sts.Spec.Template.Spec.InitContainers {
		if sts.Spec.Template.Spec.InitContainers[i].Name == "channel-custom-adapter" {
			sidecar = &sts.Spec.Template.Spec.InitContainers[i]
			break
		}
	}
	if sidecar == nil {
		t.Fatal("expected channel sidecar 'channel-custom-adapter' in initContainers, not found")
	}

	// Verify custom image.
	if sidecar.Image != "my-registry/custom-adapter:v1" {
		t.Errorf("expected custom image, got %q", sidecar.Image)
	}

	// Verify custom env var is present.
	envMap := make(map[string]string)
	for _, e := range sidecar.Env {
		envMap[e.Name] = e.Value
	}
	if envMap["CUSTOM_VAR"] != "custom_value" {
		t.Errorf("expected CUSTOM_VAR=custom_value, got %q", envMap["CUSTOM_VAR"])
	}
	if envMap["CHANNEL_TYPE"] != "custom" {
		t.Errorf("expected CHANNEL_TYPE=custom, got %q", envMap["CHANNEL_TYPE"])
	}

	// Verify custom port.
	if len(sidecar.Ports) != 1 || sidecar.Ports[0].ContainerPort != 9090 {
		t.Errorf("expected port 9090, got %v", sidecar.Ports)
	}

	// Verify custom resources override default.
	if sidecar.Resources.Requests.Cpu().String() != "100m" {
		t.Errorf("expected CPU request 100m, got %s", sidecar.Resources.Requests.Cpu().String())
	}
}

// ---------------------------------------------------------------------------
// Integration test: Channel with credentials (envFrom in sidecar)
// ---------------------------------------------------------------------------

func TestClawReconciler_ChannelWithCredentials(t *testing.T) {
	ns := fmt.Sprintf("test-ch-cred-%d", time.Now().UnixNano())
	createNamespace(t, ns)

	// Create a secret for the channel.
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "slack-creds",
			Namespace: ns,
		},
		Data: map[string][]byte{"SLACK_TOKEN": []byte("xoxb-test")},
	}
	if err := k8sClient.Create(ctx, secret); err != nil {
		t.Fatalf("failed to create Secret: %v", err)
	}

	// Create a ClawChannel with credentials.
	channel := &clawv1alpha1.ClawChannel{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cred-slack",
			Namespace: ns,
		},
		Spec: clawv1alpha1.ClawChannelSpec{
			Type: clawv1alpha1.ChannelTypeSlack,
			Mode: clawv1alpha1.ChannelModeBidirectional,
			Credentials: &clawv1alpha1.CredentialSpec{
				SecretRef: &corev1.LocalObjectReference{Name: "slack-creds"},
			},
		},
	}
	if err := k8sClient.Create(ctx, channel); err != nil {
		t.Fatalf("failed to create ClawChannel: %v", err)
	}

	// Create a Claw referencing the channel.
	clawName := "test-claw-ch-cred"
	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{
			Name:      clawName,
			Namespace: ns,
		},
		Spec: clawv1alpha1.ClawSpec{
			Runtime: clawv1alpha1.RuntimePicoClaw,
			Channels: []clawv1alpha1.ChannelRef{
				{Name: "cred-slack", Mode: clawv1alpha1.ChannelModeBidirectional},
			},
		},
	}
	if err := k8sClient.Create(ctx, claw); err != nil {
		t.Fatalf("failed to create Claw: %v", err)
	}

	// Wait for StatefulSet.
	var sts appsv1.StatefulSet
	waitForCondition(t, testTimeout, testInterval, func() (bool, error) {
		err := k8sClient.Get(ctx, types.NamespacedName{Name: clawName, Namespace: ns}, &sts)
		if err != nil {
			if client.IgnoreNotFound(err) == nil {
				return false, nil
			}
			return false, err
		}
		return true, nil
	})

	// Find the channel sidecar.
	var sidecar *corev1.Container
	for i := range sts.Spec.Template.Spec.InitContainers {
		if sts.Spec.Template.Spec.InitContainers[i].Name == "channel-cred-slack" {
			sidecar = &sts.Spec.Template.Spec.InitContainers[i]
			break
		}
	}
	if sidecar == nil {
		t.Fatal("expected channel sidecar 'channel-cred-slack', not found")
	}

	// Verify envFrom with the secret ref.
	found := false
	for _, ef := range sidecar.EnvFrom {
		if ef.SecretRef != nil && ef.SecretRef.Name == "slack-creds" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected envFrom with secretRef 'slack-creds' on channel sidecar, not found")
	}
}

// ---------------------------------------------------------------------------
// Integration test: Channel with config (CHANNEL_CONFIG env var)
// ---------------------------------------------------------------------------

func TestClawReconciler_ChannelWithConfig(t *testing.T) {
	ns := fmt.Sprintf("test-ch-cfg-%d", time.Now().UnixNano())
	createNamespace(t, ns)

	configJSON := `{"webhook_url":"https://example.com/hook","timeout":30}`
	channel := &clawv1alpha1.ClawChannel{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cfg-webhook",
			Namespace: ns,
		},
		Spec: clawv1alpha1.ClawChannelSpec{
			Type:   clawv1alpha1.ChannelTypeWebhook,
			Mode:   clawv1alpha1.ChannelModeBidirectional,
			Config: &apiextensionsv1.JSON{Raw: []byte(configJSON)},
		},
	}
	if err := k8sClient.Create(ctx, channel); err != nil {
		t.Fatalf("failed to create ClawChannel: %v", err)
	}

	clawName := "test-claw-ch-cfg"
	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{
			Name:      clawName,
			Namespace: ns,
		},
		Spec: clawv1alpha1.ClawSpec{
			Runtime: clawv1alpha1.RuntimePicoClaw,
			Channels: []clawv1alpha1.ChannelRef{
				{Name: "cfg-webhook", Mode: clawv1alpha1.ChannelModeBidirectional},
			},
		},
	}
	if err := k8sClient.Create(ctx, claw); err != nil {
		t.Fatalf("failed to create Claw: %v", err)
	}

	var sts appsv1.StatefulSet
	waitForCondition(t, testTimeout, testInterval, func() (bool, error) {
		err := k8sClient.Get(ctx, types.NamespacedName{Name: clawName, Namespace: ns}, &sts)
		if err != nil {
			if client.IgnoreNotFound(err) == nil {
				return false, nil
			}
			return false, err
		}
		return true, nil
	})

	// Find the channel sidecar.
	var sidecar *corev1.Container
	for i := range sts.Spec.Template.Spec.InitContainers {
		if sts.Spec.Template.Spec.InitContainers[i].Name == "channel-cfg-webhook" {
			sidecar = &sts.Spec.Template.Spec.InitContainers[i]
			break
		}
	}
	if sidecar == nil {
		t.Fatal("expected channel sidecar 'channel-cfg-webhook', not found")
	}

	// Verify CHANNEL_CONFIG env var is present and contains the config.
	envMap := make(map[string]string)
	for _, e := range sidecar.Env {
		envMap[e.Name] = e.Value
	}
	channelCfg, ok := envMap["CHANNEL_CONFIG"]
	if !ok {
		t.Fatal("expected CHANNEL_CONFIG env var, not found")
	}
	if channelCfg == "" {
		t.Error("expected non-empty CHANNEL_CONFIG")
	}
}

// ---------------------------------------------------------------------------
// Integration test: Credential injection with both secretRef AND keys
// ---------------------------------------------------------------------------

func TestClawReconciler_CredentialBothSecretRefAndKeys(t *testing.T) {
	ns := fmt.Sprintf("test-cred-both-%d", time.Now().UnixNano())
	createNamespace(t, ns)

	// Create a Secret.
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "both-creds",
			Namespace: ns,
		},
		Data: map[string][]byte{
			"API_KEY":    []byte("key-123"),
			"API_SECRET": []byte("secret-456"),
		},
	}
	if err := k8sClient.Create(ctx, secret); err != nil {
		t.Fatalf("failed to create Secret: %v", err)
	}

	clawName := "test-claw-cred-both"
	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{
			Name:      clawName,
			Namespace: ns,
		},
		Spec: clawv1alpha1.ClawSpec{
			Runtime: clawv1alpha1.RuntimeOpenClaw,
			Credentials: &clawv1alpha1.CredentialSpec{
				SecretRef: &corev1.LocalObjectReference{Name: "both-creds"},
				Keys: []clawv1alpha1.KeyMapping{
					{
						Name: "MAPPED_KEY",
						SecretKeyRef: corev1.SecretKeySelector{
							LocalObjectReference: corev1.LocalObjectReference{Name: "both-creds"},
							Key:                  "API_SECRET",
						},
					},
				},
			},
		},
	}
	if err := k8sClient.Create(ctx, claw); err != nil {
		t.Fatalf("failed to create Claw: %v", err)
	}

	var sts appsv1.StatefulSet
	waitForCondition(t, testTimeout, testInterval, func() (bool, error) {
		err := k8sClient.Get(ctx, types.NamespacedName{Name: clawName, Namespace: ns}, &sts)
		if err != nil {
			if client.IgnoreNotFound(err) == nil {
				return false, nil
			}
			return false, err
		}
		return true, nil
	})

	// Find runtime container.
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

	// Verify envFrom (from secretRef).
	foundEnvFrom := false
	for _, ef := range runtimeContainer.EnvFrom {
		if ef.SecretRef != nil && ef.SecretRef.Name == "both-creds" {
			foundEnvFrom = true
			break
		}
	}
	if !foundEnvFrom {
		t.Error("expected envFrom with secretRef 'both-creds', not found")
	}

	// Verify per-key env var (from keys mapping).
	foundKeyMapping := false
	for _, env := range runtimeContainer.Env {
		if env.Name == "MAPPED_KEY" &&
			env.ValueFrom != nil &&
			env.ValueFrom.SecretKeyRef != nil &&
			env.ValueFrom.SecretKeyRef.Name == "both-creds" &&
			env.ValueFrom.SecretKeyRef.Key == "API_SECRET" {
			foundKeyMapping = true
			break
		}
	}
	if !foundKeyMapping {
		t.Error("expected env var MAPPED_KEY with secretKeyRef, not found")
	}

	// Verify secret hash annotation is present (from secretRef path).
	hash, ok := sts.Spec.Template.Annotations["claw.prismer.ai/secret-hash"]
	if !ok || hash == "" {
		t.Error("expected non-empty secret-hash annotation")
	}
}

// ---------------------------------------------------------------------------
// Integration test: findClawsForChannel maps channel changes to Claws
// ---------------------------------------------------------------------------

func TestClawReconciler_FindClawsForChannel(t *testing.T) {
	ns := fmt.Sprintf("test-find-ch-%d", time.Now().UnixNano())
	createNamespace(t, ns)

	// Create a ClawChannel.
	channel := &clawv1alpha1.ClawChannel{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "watched-channel",
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

	// Create a Claw referencing the channel.
	clawName := "test-claw-watch"
	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{
			Name:      clawName,
			Namespace: ns,
		},
		Spec: clawv1alpha1.ClawSpec{
			Runtime: clawv1alpha1.RuntimePicoClaw,
			Channels: []clawv1alpha1.ChannelRef{
				{Name: "watched-channel", Mode: clawv1alpha1.ChannelModeBidirectional},
			},
		},
	}
	if err := k8sClient.Create(ctx, claw); err != nil {
		t.Fatalf("failed to create Claw: %v", err)
	}

	// Wait for StatefulSet to confirm Claw is reconciled.
	var sts appsv1.StatefulSet
	waitForCondition(t, testTimeout, testInterval, func() (bool, error) {
		err := k8sClient.Get(ctx, types.NamespacedName{Name: clawName, Namespace: ns}, &sts)
		if err != nil {
			if client.IgnoreNotFound(err) == nil {
				return false, nil
			}
			return false, err
		}
		return true, nil
	})
	initialRV := sts.ResourceVersion

	// Update the ClawChannel spec to trigger the Watches handler.
	var latestChannel clawv1alpha1.ClawChannel
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: "watched-channel", Namespace: ns}, &latestChannel); err != nil {
		t.Fatalf("failed to get channel: %v", err)
	}
	latestChannel.Spec.Mode = clawv1alpha1.ChannelModeOutbound
	if err := k8sClient.Update(ctx, &latestChannel); err != nil {
		t.Fatalf("failed to update channel: %v", err)
	}

	// The Watches handler should map the channel change to the Claw, triggering re-reconcile.
	// Wait for the StatefulSet to be re-reconciled (resourceVersion changes).
	waitForCondition(t, testTimeout, testInterval, func() (bool, error) {
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: clawName, Namespace: ns}, &sts); err != nil {
			return false, err
		}
		return sts.ResourceVersion != initialRV, nil
	})
}

// ---------------------------------------------------------------------------
// Integration test: Channel with custom resources override
// ---------------------------------------------------------------------------

func TestClawReconciler_ChannelWithCustomResources(t *testing.T) {
	ns := fmt.Sprintf("test-ch-res-%d", time.Now().UnixNano())
	createNamespace(t, ns)

	channel := &clawv1alpha1.ClawChannel{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "res-channel",
			Namespace: ns,
		},
		Spec: clawv1alpha1.ClawChannelSpec{
			Type: clawv1alpha1.ChannelTypeWebhook,
			Mode: clawv1alpha1.ChannelModeBidirectional,
			Resources: &corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("250m"),
					corev1.ResourceMemory: resource.MustParse("256Mi"),
				},
				Limits: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("500m"),
					corev1.ResourceMemory: resource.MustParse("512Mi"),
				},
			},
		},
	}
	if err := k8sClient.Create(ctx, channel); err != nil {
		t.Fatalf("failed to create ClawChannel: %v", err)
	}

	clawName := "test-claw-ch-res"
	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{
			Name:      clawName,
			Namespace: ns,
		},
		Spec: clawv1alpha1.ClawSpec{
			Runtime: clawv1alpha1.RuntimePicoClaw,
			Channels: []clawv1alpha1.ChannelRef{
				{Name: "res-channel", Mode: clawv1alpha1.ChannelModeBidirectional},
			},
		},
	}
	if err := k8sClient.Create(ctx, claw); err != nil {
		t.Fatalf("failed to create Claw: %v", err)
	}

	var sts appsv1.StatefulSet
	waitForCondition(t, testTimeout, testInterval, func() (bool, error) {
		err := k8sClient.Get(ctx, types.NamespacedName{Name: clawName, Namespace: ns}, &sts)
		if err != nil {
			if client.IgnoreNotFound(err) == nil {
				return false, nil
			}
			return false, err
		}
		return true, nil
	})

	// Find the channel sidecar.
	var sidecar *corev1.Container
	for i := range sts.Spec.Template.Spec.InitContainers {
		if sts.Spec.Template.Spec.InitContainers[i].Name == "channel-res-channel" {
			sidecar = &sts.Spec.Template.Spec.InitContainers[i]
			break
		}
	}
	if sidecar == nil {
		t.Fatal("expected channel sidecar 'channel-res-channel', not found")
	}

	// Verify custom resources were applied.
	if sidecar.Resources.Requests.Cpu().String() != "250m" {
		t.Errorf("expected CPU request 250m, got %s", sidecar.Resources.Requests.Cpu().String())
	}
	if sidecar.Resources.Limits.Memory().String() != "512Mi" {
		t.Errorf("expected memory limit 512Mi, got %s", sidecar.Resources.Limits.Memory().String())
	}
}

// ---------------------------------------------------------------------------
// Integration test: ClawChannel deletion proceeds when no references
// ---------------------------------------------------------------------------

func TestClawChannelReconciler_DeletionNoReferences(t *testing.T) {
	ns := fmt.Sprintf("test-ch-delok-%d", time.Now().UnixNano())
	createNamespace(t, ns)

	// Create a ClawChannel with NO Claws referencing it.
	channel := &clawv1alpha1.ClawChannel{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-channel-delok",
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

	// Wait for the finalizer to be added.
	nn := types.NamespacedName{Name: channel.Name, Namespace: channel.Namespace}
	waitForCondition(t, testTimeout, testInterval, func() (bool, error) {
		var fetched clawv1alpha1.ClawChannel
		if err := k8sClient.Get(ctx, nn, &fetched); err != nil {
			if client.IgnoreNotFound(err) == nil {
				return false, nil
			}
			return false, err
		}
		return controllerutil.ContainsFinalizer(&fetched, clawChannelFinalizer), nil
	})

	// Delete the channel.
	var latest clawv1alpha1.ClawChannel
	if err := k8sClient.Get(ctx, nn, &latest); err != nil {
		t.Fatalf("failed to get channel: %v", err)
	}
	if err := k8sClient.Delete(ctx, &latest); err != nil {
		t.Fatalf("failed to delete channel: %v", err)
	}

	// Wait for the channel to be fully deleted (finalizer removed, no references).
	waitForCondition(t, testTimeout, testInterval, func() (bool, error) {
		err := k8sClient.Get(ctx, nn, &clawv1alpha1.ClawChannel{})
		if client.IgnoreNotFound(err) == nil {
			return true, nil
		}
		return false, err
	})
}

// ===========================================================================
// Unit tests: error paths using fake client + interceptor
// ===========================================================================

// newFakeReconciler builds a ClawReconciler with the given client for unit testing.
func newFakeReconciler(c client.Client) *ClawReconciler {
	registry := clawruntime.NewRegistry()
	registry.Register(clawv1alpha1.RuntimeOpenClaw, &clawruntime.OpenClawAdapter{})
	registry.Register(clawv1alpha1.RuntimeNanoClaw, &clawruntime.NanoClawAdapter{})
	registry.Register(clawv1alpha1.RuntimeZeroClaw, &clawruntime.ZeroClawAdapter{})
	registry.Register(clawv1alpha1.RuntimePicoClaw, &clawruntime.PicoClawAdapter{})
	return &ClawReconciler{
		Client:                c,
		Scheme:                scheme.Scheme,
		Registry:              registry,
		NativeSidecarsEnabled: true,
	}
}

func TestReconcile_ClawNotFound(t *testing.T) {
	fakeClient := fake.NewClientBuilder().WithScheme(scheme.Scheme).Build()
	r := newFakeReconciler(fakeClient)

	// Reconcile a non-existent Claw — should return nil (IgnoreNotFound).
	result, err := r.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "nonexistent", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("expected nil error for not-found Claw, got: %v", err)
	}
	if result.Requeue {
		t.Error("expected no requeue for not-found Claw")
	}
}

func TestEnsureConfigMap_GetError(t *testing.T) {
	errSimulated := errors.New("simulated get error")
	fakeClient := fake.NewClientBuilder().WithScheme(scheme.Scheme).Build()
	wrappedClient := interceptor.NewClient(fakeClient, interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if _, ok := obj.(*corev1.ConfigMap); ok {
				return errSimulated
			}
			return c.Get(ctx, key, obj, opts...)
		},
	})

	r := newFakeReconciler(wrappedClient)
	adapter, _ := r.Registry.Get(clawv1alpha1.RuntimeOpenClaw)

	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
		Spec:       clawv1alpha1.ClawSpec{Runtime: clawv1alpha1.RuntimeOpenClaw},
	}

	err := r.ensureConfigMap(ctx, claw, adapter)
	if err == nil {
		t.Fatal("expected error from ensureConfigMap, got nil")
	}
	if !errors.Is(err, errSimulated) {
		t.Errorf("expected simulated error, got: %v", err)
	}
}

func TestEnsureConfigMap_CreateError(t *testing.T) {
	errSimulated := errors.New("simulated create error")
	fakeClient := fake.NewClientBuilder().WithScheme(scheme.Scheme).Build()
	wrappedClient := interceptor.NewClient(fakeClient, interceptor.Funcs{
		Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			if _, ok := obj.(*corev1.ConfigMap); ok {
				return errSimulated
			}
			return c.Create(ctx, obj, opts...)
		},
	})

	r := newFakeReconciler(wrappedClient)
	adapter, _ := r.Registry.Get(clawv1alpha1.RuntimeOpenClaw)

	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default", UID: "test-uid"},
		Spec:       clawv1alpha1.ClawSpec{Runtime: clawv1alpha1.RuntimeOpenClaw},
	}

	err := r.ensureConfigMap(ctx, claw, adapter)
	if err == nil {
		t.Fatal("expected error from ensureConfigMap Create, got nil")
	}
	if !errors.Is(err, errSimulated) {
		t.Errorf("expected simulated create error, got: %v", err)
	}
}

func TestEnsureConfigMap_UpdateError(t *testing.T) {
	errSimulated := errors.New("simulated update error")

	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default", UID: "test-uid"},
		Spec:       clawv1alpha1.ClawSpec{Runtime: clawv1alpha1.RuntimeOpenClaw},
	}

	// Pre-create a ConfigMap so the update path is triggered.
	existingCM := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "test-config", Namespace: "default"},
		Data:       map[string]string{"config.json": "{}"},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme.Scheme).
		WithObjects(existingCM).
		Build()
	wrappedClient := interceptor.NewClient(fakeClient, interceptor.Funcs{
		Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
			if _, ok := obj.(*corev1.ConfigMap); ok {
				return errSimulated
			}
			return c.Update(ctx, obj, opts...)
		},
	})

	r := newFakeReconciler(wrappedClient)
	adapter, _ := r.Registry.Get(clawv1alpha1.RuntimeOpenClaw)

	err := r.ensureConfigMap(ctx, claw, adapter)
	if err == nil {
		t.Fatal("expected error from ensureConfigMap Update, got nil")
	}
	if !errors.Is(err, errSimulated) {
		t.Errorf("expected simulated update error, got: %v", err)
	}
}

func TestEnsureService_GetError(t *testing.T) {
	errSimulated := errors.New("simulated service get error")
	fakeClient := fake.NewClientBuilder().WithScheme(scheme.Scheme).Build()
	wrappedClient := interceptor.NewClient(fakeClient, interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if _, ok := obj.(*corev1.Service); ok {
				return errSimulated
			}
			return c.Get(ctx, key, obj, opts...)
		},
	})

	r := newFakeReconciler(wrappedClient)
	adapter, _ := r.Registry.Get(clawv1alpha1.RuntimeOpenClaw)

	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default", UID: "test-uid"},
		Spec:       clawv1alpha1.ClawSpec{Runtime: clawv1alpha1.RuntimeOpenClaw},
	}

	err := r.ensureService(ctx, claw, adapter)
	if err == nil {
		t.Fatal("expected error from ensureService, got nil")
	}
	if !errors.Is(err, errSimulated) {
		t.Errorf("expected simulated error, got: %v", err)
	}
}

func TestEnsureService_UpdateError(t *testing.T) {
	errSimulated := errors.New("simulated service update error")

	// Pre-create a Service so the update path is triggered.
	existingSvc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
		Spec:       corev1.ServiceSpec{ClusterIP: corev1.ClusterIPNone},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme.Scheme).
		WithObjects(existingSvc).
		Build()
	wrappedClient := interceptor.NewClient(fakeClient, interceptor.Funcs{
		Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
			if _, ok := obj.(*corev1.Service); ok {
				return errSimulated
			}
			return c.Update(ctx, obj, opts...)
		},
	})

	r := newFakeReconciler(wrappedClient)
	adapter, _ := r.Registry.Get(clawv1alpha1.RuntimeOpenClaw)

	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default", UID: "test-uid"},
		Spec:       clawv1alpha1.ClawSpec{Runtime: clawv1alpha1.RuntimeOpenClaw},
	}

	err := r.ensureService(ctx, claw, adapter)
	if err == nil {
		t.Fatal("expected error from ensureService Update, got nil")
	}
	if !errors.Is(err, errSimulated) {
		t.Errorf("expected simulated update error, got: %v", err)
	}
}

func TestEnsureStatefulSet_GetError(t *testing.T) {
	errSimulated := errors.New("simulated sts get error")
	fakeClient := fake.NewClientBuilder().WithScheme(scheme.Scheme).Build()
	wrappedClient := interceptor.NewClient(fakeClient, interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if _, ok := obj.(*appsv1.StatefulSet); ok {
				return errSimulated
			}
			return c.Get(ctx, key, obj, opts...)
		},
	})

	r := newFakeReconciler(wrappedClient)
	adapter, _ := r.Registry.Get(clawv1alpha1.RuntimeOpenClaw)

	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default", UID: "test-uid"},
		Spec:       clawv1alpha1.ClawSpec{Runtime: clawv1alpha1.RuntimeOpenClaw},
	}

	err := r.ensureStatefulSet(ctx, claw, adapter)
	if err == nil {
		t.Fatal("expected error from ensureStatefulSet, got nil")
	}
	if !errors.Is(err, errSimulated) {
		t.Errorf("expected simulated error, got: %v", err)
	}
}

func TestEnsureStatefulSet_UpdateError(t *testing.T) {
	errSimulated := errors.New("simulated sts update error")

	existingSts := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme.Scheme).
		WithObjects(existingSts).
		Build()
	wrappedClient := interceptor.NewClient(fakeClient, interceptor.Funcs{
		Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
			if _, ok := obj.(*appsv1.StatefulSet); ok {
				return errSimulated
			}
			return c.Update(ctx, obj, opts...)
		},
	})

	r := newFakeReconciler(wrappedClient)
	adapter, _ := r.Registry.Get(clawv1alpha1.RuntimeOpenClaw)

	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default", UID: "test-uid"},
		Spec:       clawv1alpha1.ClawSpec{Runtime: clawv1alpha1.RuntimeOpenClaw},
	}

	err := r.ensureStatefulSet(ctx, claw, adapter)
	if err == nil {
		t.Fatal("expected error from ensureStatefulSet Update, got nil")
	}
	if !errors.Is(err, errSimulated) {
		t.Errorf("expected simulated update error, got: %v", err)
	}
}

func TestUpdateStatus_GetError(t *testing.T) {
	errSimulated := errors.New("simulated status get error")
	fakeClient := fake.NewClientBuilder().WithScheme(scheme.Scheme).Build()
	wrappedClient := interceptor.NewClient(fakeClient, interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if _, ok := obj.(*appsv1.StatefulSet); ok {
				return errSimulated
			}
			return c.Get(ctx, key, obj, opts...)
		},
	})

	r := newFakeReconciler(wrappedClient)

	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
		Spec:       clawv1alpha1.ClawSpec{Runtime: clawv1alpha1.RuntimeOpenClaw},
	}

	err := r.updateStatus(ctx, claw)
	if err == nil {
		t.Fatal("expected error from updateStatus, got nil")
	}
	if !errors.Is(err, errSimulated) {
		t.Errorf("expected simulated error, got: %v", err)
	}
}

func TestDeleteClawPVCs_ListError(t *testing.T) {
	errSimulated := errors.New("simulated list error")
	fakeClient := fake.NewClientBuilder().WithScheme(scheme.Scheme).Build()
	wrappedClient := interceptor.NewClient(fakeClient, interceptor.Funcs{
		List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			return errSimulated
		},
	})

	r := newFakeReconciler(wrappedClient)

	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
	}

	err := r.deleteClawPVCs(ctx, claw)
	if err == nil {
		t.Fatal("expected error from deleteClawPVCs, got nil")
	}
	if !errors.Is(err, errSimulated) {
		t.Errorf("expected simulated error, got: %v", err)
	}
}

func TestDeleteClawPVCs_DeleteError(t *testing.T) {
	errSimulated := errors.New("simulated delete error")

	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pvc-0",
			Namespace: "default",
			Labels:    map[string]string{"claw.prismer.ai/instance": "test"},
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")},
			},
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme.Scheme).
		WithObjects(pvc).
		Build()
	wrappedClient := interceptor.NewClient(fakeClient, interceptor.Funcs{
		Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
			return errSimulated
		},
	})

	r := newFakeReconciler(wrappedClient)

	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
	}

	err := r.deleteClawPVCs(ctx, claw)
	if err == nil {
		t.Fatal("expected error from deleteClawPVCs Delete, got nil")
	}
	if !errors.Is(err, errSimulated) {
		t.Errorf("expected simulated delete error, got: %v", err)
	}
}

func TestHandleDeletion_NoFinalizer(t *testing.T) {
	fakeClient := fake.NewClientBuilder().WithScheme(scheme.Scheme).Build()
	r := newFakeReconciler(fakeClient)

	// Create a Claw with DeletionTimestamp but no finalizer.
	now := metav1.Now()
	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "test",
			Namespace:         "default",
			DeletionTimestamp: &now,
			Finalizers:        []string{}, // No finalizer.
		},
		Spec: clawv1alpha1.ClawSpec{Runtime: clawv1alpha1.RuntimeOpenClaw},
	}

	result, err := r.handleDeletion(ctx, claw)
	if err != nil {
		t.Fatalf("expected nil error for handleDeletion without finalizer, got: %v", err)
	}
	if result.Requeue {
		t.Error("expected no requeue")
	}
}

func TestEnsureStatefulSet_CreateError(t *testing.T) {
	errSimulated := errors.New("simulated sts create error")
	fakeClient := fake.NewClientBuilder().WithScheme(scheme.Scheme).Build()
	wrappedClient := interceptor.NewClient(fakeClient, interceptor.Funcs{
		Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			if _, ok := obj.(*appsv1.StatefulSet); ok {
				return errSimulated
			}
			return c.Create(ctx, obj, opts...)
		},
	})

	r := newFakeReconciler(wrappedClient)
	adapter, _ := r.Registry.Get(clawv1alpha1.RuntimeOpenClaw)

	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default", UID: "test-uid"},
		Spec:       clawv1alpha1.ClawSpec{Runtime: clawv1alpha1.RuntimeOpenClaw},
	}

	err := r.ensureStatefulSet(ctx, claw, adapter)
	if err == nil {
		t.Fatal("expected error from ensureStatefulSet Create, got nil")
	}
	if !errors.Is(err, errSimulated) {
		t.Errorf("expected simulated create error, got: %v", err)
	}
}

func TestReconcile_EnsureServiceError(t *testing.T) {
	errSimulated := errors.New("simulated reconcile service error")

	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-reconcile-svc", Namespace: "default",
			UID: "test-uid", Generation: 1,
		},
		Spec: clawv1alpha1.ClawSpec{Runtime: clawv1alpha1.RuntimeOpenClaw},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme.Scheme).
		WithObjects(claw).
		WithStatusSubresource(claw).
		Build()
	wrappedClient := interceptor.NewClient(fakeClient, interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if _, ok := obj.(*corev1.Service); ok {
				return errSimulated
			}
			return c.Get(ctx, key, obj, opts...)
		},
	})

	r := newFakeReconciler(wrappedClient)

	_, err := r.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "test-reconcile-svc", Namespace: "default"},
	})
	if err == nil {
		t.Fatal("expected error from Reconcile, got nil")
	}
}

func TestReconcile_EnsureConfigMapError(t *testing.T) {
	errSimulated := errors.New("simulated reconcile cm error")

	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-reconcile-cm", Namespace: "default",
			UID: "test-uid-2", Generation: 1,
		},
		Spec: clawv1alpha1.ClawSpec{Runtime: clawv1alpha1.RuntimeOpenClaw},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme.Scheme).
		WithObjects(claw).
		WithStatusSubresource(claw).
		Build()
	wrappedClient := interceptor.NewClient(fakeClient, interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if _, ok := obj.(*corev1.ConfigMap); ok {
				return errSimulated
			}
			return c.Get(ctx, key, obj, opts...)
		},
	})

	r := newFakeReconciler(wrappedClient)

	_, err := r.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "test-reconcile-cm", Namespace: "default"},
	})
	if err == nil {
		t.Fatal("expected error from Reconcile, got nil")
	}
}

func TestReconcile_EnsureStatefulSetError(t *testing.T) {
	errSimulated := errors.New("simulated reconcile sts error")

	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-reconcile-sts", Namespace: "default",
			UID: "test-uid-3", Generation: 1,
		},
		Spec: clawv1alpha1.ClawSpec{Runtime: clawv1alpha1.RuntimeOpenClaw},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme.Scheme).
		WithObjects(claw).
		WithStatusSubresource(claw).
		Build()
	wrappedClient := interceptor.NewClient(fakeClient, interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if _, ok := obj.(*appsv1.StatefulSet); ok {
				return errSimulated
			}
			return c.Get(ctx, key, obj, opts...)
		},
	})

	r := newFakeReconciler(wrappedClient)

	_, err := r.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "test-reconcile-sts", Namespace: "default"},
	})
	if err == nil {
		t.Fatal("expected error from Reconcile, got nil")
	}
}

func TestReconcile_UpdateStatusError(t *testing.T) {
	errSimulated := errors.New("simulated status update error")

	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-reconcile-status", Namespace: "default",
			UID: "test-uid-4", Generation: 1,
		},
		Spec: clawv1alpha1.ClawSpec{Runtime: clawv1alpha1.RuntimeOpenClaw},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme.Scheme).
		WithObjects(claw).
		WithStatusSubresource(claw).
		Build()
	wrappedClient := interceptor.NewClient(fakeClient, interceptor.Funcs{
		SubResourceUpdate: func(ctx context.Context, c client.Client, subResourceName string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
			return errSimulated
		},
	})

	r := newFakeReconciler(wrappedClient)

	_, err := r.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "test-reconcile-status", Namespace: "default"},
	})
	if err == nil {
		t.Fatal("expected error from Reconcile status update, got nil")
	}
}

func TestEnsureService_CreateError(t *testing.T) {
	errSimulated := errors.New("simulated service create error")
	fakeClient := fake.NewClientBuilder().WithScheme(scheme.Scheme).Build()
	wrappedClient := interceptor.NewClient(fakeClient, interceptor.Funcs{
		Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			if _, ok := obj.(*corev1.Service); ok {
				return errSimulated
			}
			return c.Create(ctx, obj, opts...)
		},
	})

	r := newFakeReconciler(wrappedClient)
	adapter, _ := r.Registry.Get(clawv1alpha1.RuntimeOpenClaw)

	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default", UID: "test-uid"},
		Spec:       clawv1alpha1.ClawSpec{Runtime: clawv1alpha1.RuntimeOpenClaw},
	}

	err := r.ensureService(ctx, claw, adapter)
	if err == nil {
		t.Fatal("expected error from ensureService Create, got nil")
	}
	if !errors.Is(err, errSimulated) {
		t.Errorf("expected simulated create error, got: %v", err)
	}
}
