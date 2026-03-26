package runtime

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"

	"github.com/Prismer-AI/k8s4claw/api/v1alpha1"
)

func TestIronClawValidate_RequiresCredentials(t *testing.T) {
	t.Parallel()
	a := &IronClawAdapter{}
	ctx := context.Background()

	spec := &v1alpha1.ClawSpec{Runtime: v1alpha1.RuntimeIronClaw}
	errs := a.Validate(ctx, spec)
	if len(errs) == 0 {
		t.Fatal("expected error when credentials are missing")
	}
	if errs[0].Field != "spec.credentials" {
		t.Errorf("field = %q; want spec.credentials", errs[0].Field)
	}
}

func TestIronClawValidate_AcceptsSecretRef(t *testing.T) {
	t.Parallel()
	a := &IronClawAdapter{}
	ctx := context.Background()

	spec := &v1alpha1.ClawSpec{
		Runtime: v1alpha1.RuntimeIronClaw,
		Credentials: &v1alpha1.CredentialSpec{
			SecretRef: &corev1.LocalObjectReference{Name: "my-secret"},
		},
	}
	errs := a.Validate(ctx, spec)
	if len(errs) != 0 {
		t.Fatalf("expected no errors, got %v", errs)
	}
}

func TestIronClawValidate_EmptyCredentialSpec(t *testing.T) {
	t.Parallel()
	a := &IronClawAdapter{}
	ctx := context.Background()

	spec := &v1alpha1.ClawSpec{
		Runtime:     v1alpha1.RuntimeIronClaw,
		Credentials: &v1alpha1.CredentialSpec{},
	}
	errs := a.Validate(ctx, spec)
	if len(errs) == 0 {
		t.Fatal("expected error for empty credential spec")
	}
}

func TestIronClawValidateUpdate_StorageClassImmutable(t *testing.T) {
	t.Parallel()
	a := &IronClawAdapter{}
	ctx := context.Background()

	oldSpec := &v1alpha1.ClawSpec{
		Runtime: v1alpha1.RuntimeIronClaw,
		Persistence: &v1alpha1.PersistenceSpec{
			Session: &v1alpha1.VolumeSpec{
				Enabled: true, Size: "2Gi", MountPath: "/s", StorageClass: "gp3",
			},
		},
	}
	newSpec := &v1alpha1.ClawSpec{
		Runtime: v1alpha1.RuntimeIronClaw,
		Persistence: &v1alpha1.PersistenceSpec{
			Session: &v1alpha1.VolumeSpec{
				Enabled: true, Size: "2Gi", MountPath: "/s", StorageClass: "io2",
			},
		},
	}

	errs := a.ValidateUpdate(ctx, oldSpec, newSpec)
	if len(errs) == 0 {
		t.Fatal("expected error for storage class change")
	}
}

func TestIronClawValidateUpdate_SizeCannotShrink(t *testing.T) {
	t.Parallel()
	a := &IronClawAdapter{}
	ctx := context.Background()

	oldSpec := &v1alpha1.ClawSpec{
		Runtime: v1alpha1.RuntimeIronClaw,
		Persistence: &v1alpha1.PersistenceSpec{
			Session: &v1alpha1.VolumeSpec{Enabled: true, Size: "10Gi", MountPath: "/s"},
		},
	}
	newSpec := &v1alpha1.ClawSpec{
		Runtime: v1alpha1.RuntimeIronClaw,
		Persistence: &v1alpha1.PersistenceSpec{
			Session: &v1alpha1.VolumeSpec{Enabled: true, Size: "5Gi", MountPath: "/s"},
		},
	}

	errs := a.ValidateUpdate(ctx, oldSpec, newSpec)
	if len(errs) == 0 {
		t.Fatal("expected error for size shrink")
	}
}

func TestIronClawValidateUpdate_SizeCanGrow(t *testing.T) {
	t.Parallel()
	a := &IronClawAdapter{}
	ctx := context.Background()

	oldSpec := &v1alpha1.ClawSpec{
		Runtime: v1alpha1.RuntimeIronClaw,
		Persistence: &v1alpha1.PersistenceSpec{
			Session: &v1alpha1.VolumeSpec{Enabled: true, Size: "5Gi", MountPath: "/s"},
		},
	}
	newSpec := &v1alpha1.ClawSpec{
		Runtime: v1alpha1.RuntimeIronClaw,
		Persistence: &v1alpha1.PersistenceSpec{
			Session: &v1alpha1.VolumeSpec{Enabled: true, Size: "10Gi", MountPath: "/s"},
		},
	}

	errs := a.ValidateUpdate(ctx, oldSpec, newSpec)
	if len(errs) != 0 {
		t.Fatalf("expected no errors, got %v", errs)
	}
}
