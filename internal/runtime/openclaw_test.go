package runtime

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"

	"github.com/Prismer-AI/k8s4claw/api/v1alpha1"
)

func TestOpenClawValidate_RequiresCredentials(t *testing.T) {
	t.Parallel()
	a := &OpenClawAdapter{}
	ctx := context.Background()

	// No credentials → error.
	spec := &v1alpha1.ClawSpec{Runtime: v1alpha1.RuntimeOpenClaw}
	errs := a.Validate(ctx, spec)
	if len(errs) == 0 {
		t.Fatal("expected error when credentials are missing")
	}
	if errs[0].Field != "spec.credentials" {
		t.Errorf("field = %q; want spec.credentials", errs[0].Field)
	}
}

func TestOpenClawValidate_AcceptsSecretRef(t *testing.T) {
	t.Parallel()
	a := &OpenClawAdapter{}
	ctx := context.Background()

	spec := &v1alpha1.ClawSpec{
		Runtime: v1alpha1.RuntimeOpenClaw,
		Credentials: &v1alpha1.CredentialSpec{
			SecretRef: &corev1.LocalObjectReference{Name: "my-secret"},
		},
	}
	errs := a.Validate(ctx, spec)
	if len(errs) != 0 {
		t.Fatalf("expected no errors, got %v", errs)
	}
}

func TestOpenClawValidate_AcceptsExternalSecret(t *testing.T) {
	t.Parallel()
	a := &OpenClawAdapter{}
	ctx := context.Background()

	spec := &v1alpha1.ClawSpec{
		Runtime: v1alpha1.RuntimeOpenClaw,
		Credentials: &v1alpha1.CredentialSpec{
			ExternalSecret: &v1alpha1.ExternalSecretRef{
				Provider: "vault",
				Store:    "main",
				Path:     "secret/openclaw",
			},
		},
	}
	errs := a.Validate(ctx, spec)
	if len(errs) != 0 {
		t.Fatalf("expected no errors, got %v", errs)
	}
}

func TestOpenClawValidate_AcceptsKeyMappingsOnly(t *testing.T) {
	t.Parallel()
	a := &OpenClawAdapter{}
	ctx := context.Background()

	spec := &v1alpha1.ClawSpec{
		Runtime: v1alpha1.RuntimeOpenClaw,
		Credentials: &v1alpha1.CredentialSpec{
			Keys: []v1alpha1.KeyMapping{
				{
					Name:         "OPENAI_API_KEY",
					SecretKeyRef: corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "keys"}, Key: "openai"},
				},
			},
		},
	}
	errs := a.Validate(ctx, spec)
	if len(errs) != 0 {
		t.Fatalf("expected no errors, got %v", errs)
	}
}

func TestOpenClawValidate_EmptyCredentialSpec(t *testing.T) {
	t.Parallel()
	a := &OpenClawAdapter{}
	ctx := context.Background()

	// Credentials struct set but all fields nil/empty → error.
	spec := &v1alpha1.ClawSpec{
		Runtime:     v1alpha1.RuntimeOpenClaw,
		Credentials: &v1alpha1.CredentialSpec{},
	}
	errs := a.Validate(ctx, spec)
	if len(errs) == 0 {
		t.Fatal("expected error for empty credential spec")
	}
}

func TestOpenClawValidateUpdate_StorageClassImmutable(t *testing.T) {
	t.Parallel()
	a := &OpenClawAdapter{}
	ctx := context.Background()

	oldSpec := &v1alpha1.ClawSpec{
		Runtime: v1alpha1.RuntimeOpenClaw,
		Persistence: &v1alpha1.PersistenceSpec{
			Session: &v1alpha1.VolumeSpec{
				Enabled:      true,
				Size:         "2Gi",
				MountPath:    "/data/session",
				StorageClass: "gp3",
			},
		},
	}
	newSpec := &v1alpha1.ClawSpec{
		Runtime: v1alpha1.RuntimeOpenClaw,
		Persistence: &v1alpha1.PersistenceSpec{
			Session: &v1alpha1.VolumeSpec{
				Enabled:      true,
				Size:         "2Gi",
				MountPath:    "/data/session",
				StorageClass: "io2",
			},
		},
	}

	errs := a.ValidateUpdate(ctx, oldSpec, newSpec)
	if len(errs) == 0 {
		t.Fatal("expected error for storage class change")
	}
	found := false
	for _, e := range errs {
		if e.Field == "spec.persistence.session.storageClass" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected storageClass field error, got %v", errs)
	}
}

func TestOpenClawValidateUpdate_StorageClassImmutable_AllVolumes(t *testing.T) {
	t.Parallel()
	a := &OpenClawAdapter{}
	ctx := context.Background()

	oldSpec := &v1alpha1.ClawSpec{
		Runtime: v1alpha1.RuntimeOpenClaw,
		Persistence: &v1alpha1.PersistenceSpec{
			Session:   &v1alpha1.VolumeSpec{Enabled: true, Size: "2Gi", MountPath: "/s", StorageClass: "gp3"},
			Output:    &v1alpha1.OutputVolumeSpec{VolumeSpec: v1alpha1.VolumeSpec{Enabled: true, Size: "5Gi", MountPath: "/o", StorageClass: "gp3"}},
			Workspace: &v1alpha1.VolumeSpec{Enabled: true, Size: "10Gi", MountPath: "/w", StorageClass: "gp3"},
		},
	}
	newSpec := &v1alpha1.ClawSpec{
		Runtime: v1alpha1.RuntimeOpenClaw,
		Persistence: &v1alpha1.PersistenceSpec{
			Session:   &v1alpha1.VolumeSpec{Enabled: true, Size: "2Gi", MountPath: "/s", StorageClass: "io2"},
			Output:    &v1alpha1.OutputVolumeSpec{VolumeSpec: v1alpha1.VolumeSpec{Enabled: true, Size: "5Gi", MountPath: "/o", StorageClass: "io2"}},
			Workspace: &v1alpha1.VolumeSpec{Enabled: true, Size: "10Gi", MountPath: "/w", StorageClass: "io2"},
		},
	}

	errs := a.ValidateUpdate(ctx, oldSpec, newSpec)
	if len(errs) != 3 {
		t.Fatalf("expected 3 errors (one per volume), got %d: %v", len(errs), errs)
	}
}

func TestOpenClawValidateUpdate_SizeCannotShrink(t *testing.T) {
	t.Parallel()
	a := &OpenClawAdapter{}
	ctx := context.Background()

	oldSpec := &v1alpha1.ClawSpec{
		Runtime: v1alpha1.RuntimeOpenClaw,
		Persistence: &v1alpha1.PersistenceSpec{
			Session: &v1alpha1.VolumeSpec{
				Enabled:   true,
				Size:      "10Gi",
				MountPath: "/data/session",
			},
		},
	}
	newSpec := &v1alpha1.ClawSpec{
		Runtime: v1alpha1.RuntimeOpenClaw,
		Persistence: &v1alpha1.PersistenceSpec{
			Session: &v1alpha1.VolumeSpec{
				Enabled:   true,
				Size:      "5Gi",
				MountPath: "/data/session",
			},
		},
	}

	errs := a.ValidateUpdate(ctx, oldSpec, newSpec)
	if len(errs) == 0 {
		t.Fatal("expected error for size shrink")
	}
}

func TestOpenClawValidateUpdate_SizeCanGrow(t *testing.T) {
	t.Parallel()
	a := &OpenClawAdapter{}
	ctx := context.Background()

	oldSpec := &v1alpha1.ClawSpec{
		Runtime: v1alpha1.RuntimeOpenClaw,
		Persistence: &v1alpha1.PersistenceSpec{
			Session: &v1alpha1.VolumeSpec{
				Enabled:   true,
				Size:      "5Gi",
				MountPath: "/data/session",
			},
		},
	}
	newSpec := &v1alpha1.ClawSpec{
		Runtime: v1alpha1.RuntimeOpenClaw,
		Persistence: &v1alpha1.PersistenceSpec{
			Session: &v1alpha1.VolumeSpec{
				Enabled:   true,
				Size:      "10Gi",
				MountPath: "/data/session",
			},
		},
	}

	errs := a.ValidateUpdate(ctx, oldSpec, newSpec)
	if len(errs) != 0 {
		t.Fatalf("expected no errors for size growth, got %v", errs)
	}
}

func TestOpenClawValidateUpdate_NilPersistence(t *testing.T) {
	t.Parallel()
	a := &OpenClawAdapter{}
	ctx := context.Background()

	// Both nil → no errors.
	errs := a.ValidateUpdate(ctx, &v1alpha1.ClawSpec{}, &v1alpha1.ClawSpec{})
	if len(errs) != 0 {
		t.Fatalf("expected no errors, got %v", errs)
	}
}

func TestOpenClawValidateUpdate_AddPersistence(t *testing.T) {
	t.Parallel()
	a := &OpenClawAdapter{}
	ctx := context.Background()

	oldSpec := &v1alpha1.ClawSpec{Runtime: v1alpha1.RuntimeOpenClaw}
	newSpec := &v1alpha1.ClawSpec{
		Runtime: v1alpha1.RuntimeOpenClaw,
		Persistence: &v1alpha1.PersistenceSpec{
			Session: &v1alpha1.VolumeSpec{Enabled: true, Size: "2Gi", MountPath: "/s"},
		},
	}

	errs := a.ValidateUpdate(ctx, oldSpec, newSpec)
	if len(errs) != 0 {
		t.Fatalf("expected no errors when adding persistence, got %v", errs)
	}
}
