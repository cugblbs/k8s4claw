package runtime

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"

	"github.com/Prismer-AI/k8s4claw/api/v1alpha1"
)

func TestPicoClawValidate_RequiresCredentials(t *testing.T) {
	t.Parallel()
	a := &PicoClawAdapter{}
	ctx := context.Background()

	spec := &v1alpha1.ClawSpec{Runtime: v1alpha1.RuntimePicoClaw}
	errs := a.Validate(ctx, spec)
	if len(errs) == 0 {
		t.Fatal("expected error when credentials are missing")
	}
	if errs[0].Field != "spec.credentials" {
		t.Errorf("field = %q; want spec.credentials", errs[0].Field)
	}
}

func TestPicoClawValidate_AcceptsSecretRef(t *testing.T) {
	t.Parallel()
	a := &PicoClawAdapter{}
	ctx := context.Background()

	spec := &v1alpha1.ClawSpec{
		Runtime: v1alpha1.RuntimePicoClaw,
		Credentials: &v1alpha1.CredentialSpec{
			SecretRef: &corev1.LocalObjectReference{Name: "my-secret"},
		},
	}
	errs := a.Validate(ctx, spec)
	if len(errs) != 0 {
		t.Fatalf("expected no errors, got %v", errs)
	}
}

func TestPicoClawValidate_AcceptsExternalSecret(t *testing.T) {
	t.Parallel()
	a := &PicoClawAdapter{}
	ctx := context.Background()

	spec := &v1alpha1.ClawSpec{
		Runtime: v1alpha1.RuntimePicoClaw,
		Credentials: &v1alpha1.CredentialSpec{
			ExternalSecret: &v1alpha1.ExternalSecretRef{
				Provider: "vault",
				Store:    "main",
				Path:     "secret/picoclaw",
			},
		},
	}
	errs := a.Validate(ctx, spec)
	if len(errs) != 0 {
		t.Fatalf("expected no errors, got %v", errs)
	}
}

func TestPicoClawValidate_AcceptsKeyMappingsOnly(t *testing.T) {
	t.Parallel()
	a := &PicoClawAdapter{}
	ctx := context.Background()

	spec := &v1alpha1.ClawSpec{
		Runtime: v1alpha1.RuntimePicoClaw,
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

func TestPicoClawValidate_EmptyCredentialSpec(t *testing.T) {
	t.Parallel()
	a := &PicoClawAdapter{}
	ctx := context.Background()

	spec := &v1alpha1.ClawSpec{
		Runtime:     v1alpha1.RuntimePicoClaw,
		Credentials: &v1alpha1.CredentialSpec{},
	}
	errs := a.Validate(ctx, spec)
	if len(errs) == 0 {
		t.Fatal("expected error for empty credential spec")
	}
}

func TestPicoClawValidateUpdate_StorageClassImmutable(t *testing.T) {
	t.Parallel()
	a := &PicoClawAdapter{}
	ctx := context.Background()

	oldSpec := &v1alpha1.ClawSpec{
		Runtime: v1alpha1.RuntimePicoClaw,
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
		Runtime: v1alpha1.RuntimePicoClaw,
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

func TestPicoClawValidateUpdate_SizeCannotShrink(t *testing.T) {
	t.Parallel()
	a := &PicoClawAdapter{}
	ctx := context.Background()

	oldSpec := &v1alpha1.ClawSpec{
		Runtime: v1alpha1.RuntimePicoClaw,
		Persistence: &v1alpha1.PersistenceSpec{
			Session: &v1alpha1.VolumeSpec{Enabled: true, Size: "10Gi", MountPath: "/data/session"},
		},
	}
	newSpec := &v1alpha1.ClawSpec{
		Runtime: v1alpha1.RuntimePicoClaw,
		Persistence: &v1alpha1.PersistenceSpec{
			Session: &v1alpha1.VolumeSpec{Enabled: true, Size: "5Gi", MountPath: "/data/session"},
		},
	}

	errs := a.ValidateUpdate(ctx, oldSpec, newSpec)
	if len(errs) == 0 {
		t.Fatal("expected error for size shrink")
	}
}

func TestPicoClawValidateUpdate_SizeCanGrow(t *testing.T) {
	t.Parallel()
	a := &PicoClawAdapter{}
	ctx := context.Background()

	oldSpec := &v1alpha1.ClawSpec{
		Runtime: v1alpha1.RuntimePicoClaw,
		Persistence: &v1alpha1.PersistenceSpec{
			Session: &v1alpha1.VolumeSpec{Enabled: true, Size: "5Gi", MountPath: "/data/session"},
		},
	}
	newSpec := &v1alpha1.ClawSpec{
		Runtime: v1alpha1.RuntimePicoClaw,
		Persistence: &v1alpha1.PersistenceSpec{
			Session: &v1alpha1.VolumeSpec{Enabled: true, Size: "10Gi", MountPath: "/data/session"},
		},
	}

	errs := a.ValidateUpdate(ctx, oldSpec, newSpec)
	if len(errs) != 0 {
		t.Fatalf("expected no errors for size growth, got %v", errs)
	}
}

func TestPicoClawValidateUpdate_NilPersistence(t *testing.T) {
	t.Parallel()
	a := &PicoClawAdapter{}
	ctx := context.Background()

	errs := a.ValidateUpdate(ctx, &v1alpha1.ClawSpec{}, &v1alpha1.ClawSpec{})
	if len(errs) != 0 {
		t.Fatalf("expected no errors, got %v", errs)
	}
}
