package runtime

import (
	"context"
	"testing"

	"github.com/Prismer-AI/k8s4claw/api/v1alpha1"
)

func TestPicoClawValidate_CredentialsOptional(t *testing.T) {
	t.Parallel()
	a := &PicoClawAdapter{}
	ctx := context.Background()

	// PicoClaw supports non-LLM workloads; no credentials required.
	spec := &v1alpha1.ClawSpec{Runtime: v1alpha1.RuntimePicoClaw}
	errs := a.Validate(ctx, spec)
	if len(errs) != 0 {
		t.Fatalf("expected no errors for credential-less spec, got %v", errs)
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
