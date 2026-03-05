package webhook

import (
	"context"

	clawv1alpha1 "github.com/Prismer-AI/k8s4claw/api/v1alpha1"
)

// ClawDefaulter implements admission.Defaulter for the Claw CRD.
type ClawDefaulter struct{}

func (d *ClawDefaulter) Default(_ context.Context, obj *clawv1alpha1.Claw) error {
	if obj.Spec.Persistence != nil && obj.Spec.Persistence.ReclaimPolicy == "" {
		obj.Spec.Persistence.ReclaimPolicy = clawv1alpha1.ReclaimRetain
	}
	return nil
}
