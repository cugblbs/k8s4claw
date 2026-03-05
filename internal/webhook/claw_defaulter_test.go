package webhook

import (
	"context"
	"testing"

	clawv1alpha1 "github.com/Prismer-AI/k8s4claw/api/v1alpha1"
)

func TestDefault_ReclaimPolicy_SetsRetain(t *testing.T) {
	d := &ClawDefaulter{}
	claw := baseClaw()
	claw.Spec.Persistence = &clawv1alpha1.PersistenceSpec{}

	if err := d.Default(context.Background(), claw); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if claw.Spec.Persistence.ReclaimPolicy != clawv1alpha1.ReclaimRetain {
		t.Errorf("expected ReclaimRetain, got %q", claw.Spec.Persistence.ReclaimPolicy)
	}
}

func TestDefault_ReclaimPolicy_DoesNotOverwrite(t *testing.T) {
	d := &ClawDefaulter{}
	claw := baseClaw()
	claw.Spec.Persistence = &clawv1alpha1.PersistenceSpec{
		ReclaimPolicy: clawv1alpha1.ReclaimDelete,
	}

	if err := d.Default(context.Background(), claw); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if claw.Spec.Persistence.ReclaimPolicy != clawv1alpha1.ReclaimDelete {
		t.Errorf("expected ReclaimDelete to be preserved, got %q", claw.Spec.Persistence.ReclaimPolicy)
	}
}

func TestDefault_NilPersistence(t *testing.T) {
	d := &ClawDefaulter{}
	claw := baseClaw()

	if err := d.Default(context.Background(), claw); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if claw.Spec.Persistence != nil {
		t.Error("expected nil persistence to remain nil")
	}
}
