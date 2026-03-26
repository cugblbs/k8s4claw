package runtime

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/validation/field"

	"github.com/Prismer-AI/k8s4claw/api/v1alpha1"
)

// OpenClawAdapter implements RuntimeAdapter for the OpenClaw runtime.
type OpenClawAdapter struct{}

var _ RuntimeAdapter = (*OpenClawAdapter)(nil)

func (a *OpenClawAdapter) PodTemplate(claw *v1alpha1.Claw) *corev1.PodTemplateSpec {
	return BuildPodTemplate(claw, a.runtimeSpec(claw))
}

func (a *OpenClawAdapter) runtimeSpec(claw *v1alpha1.Claw) *RuntimeSpec {
	return &RuntimeSpec{
		Image:          "ghcr.io/prismer-ai/k8s4claw-openclaw:latest",
		Command:        []string{"/usr/bin/claw-entrypoint"},
		Ports:          []corev1.ContainerPort{{Name: "gateway", ContainerPort: 18900, Protocol: corev1.ProtocolTCP}},
		Resources:      resources("500m", "1Gi", "2000m", "4Gi"),
		ConfigMode:     ConfigModeDeepMerge,
		WorkspacePath:  "/workspace",
		Env:            []corev1.EnvVar{{Name: "OPENCLAW_MODE", Value: "gateway"}},
		LivenessProbe:  a.HealthProbe(claw),
		ReadinessProbe: a.ReadinessProbe(claw),
	}
}

func (a *OpenClawAdapter) HealthProbe(_ *v1alpha1.Claw) *corev1.Probe {
	return &corev1.Probe{
		ProbeHandler: corev1.ProbeHandler{
			HTTPGet: &corev1.HTTPGetAction{
				Path: "/health",
				Port: portIntStr(18900),
			},
		},
		InitialDelaySeconds: 10,
		PeriodSeconds:       15,
	}
}

func (a *OpenClawAdapter) ReadinessProbe(_ *v1alpha1.Claw) *corev1.Probe {
	return &corev1.Probe{
		ProbeHandler: corev1.ProbeHandler{
			HTTPGet: &corev1.HTTPGetAction{
				Path: "/ready",
				Port: portIntStr(18900),
			},
		},
		InitialDelaySeconds: 5,
		PeriodSeconds:       10,
	}
}

func (a *OpenClawAdapter) DefaultConfig() *RuntimeConfig {
	return &RuntimeConfig{
		GatewayPort:   18900,
		WorkspacePath: "/workspace",
		Environment: map[string]string{
			"OPENCLAW_MODE": "gateway",
		},
	}
}

func (a *OpenClawAdapter) GracefulShutdownSeconds() int32 {
	return 30
}

func (a *OpenClawAdapter) Validate(_ context.Context, spec *v1alpha1.ClawSpec) field.ErrorList {
	var allErrs field.ErrorList

	// OpenClaw requires credentials (LLM API keys) to function.
	if !hasCredentials(spec) {
		allErrs = append(allErrs, field.Required(
			field.NewPath("spec", "credentials"),
			"OpenClaw requires credentials (secretRef, externalSecret, or keys)",
		))
	}

	return allErrs
}

func (a *OpenClawAdapter) ValidateUpdate(_ context.Context, oldSpec, newSpec *v1alpha1.ClawSpec) field.ErrorList {
	return validatePersistenceUpdate(oldSpec, newSpec)
}

// hasCredentials returns true if the spec has at least one credential source configured.
func hasCredentials(spec *v1alpha1.ClawSpec) bool {
	c := spec.Credentials
	if c == nil {
		return false
	}
	return c.SecretRef != nil || c.ExternalSecret != nil || len(c.Keys) > 0
}

// validatePersistenceUpdate checks that PVC storage classes are not changed
// and sizes are not shrunk (PVCs only support expansion).
func validatePersistenceUpdate(oldSpec, newSpec *v1alpha1.ClawSpec) field.ErrorList {
	if oldSpec.Persistence == nil || newSpec.Persistence == nil {
		return nil
	}

	var allErrs field.ErrorList
	basePath := field.NewPath("spec", "persistence")

	allErrs = append(allErrs, validateVolumeUpdate(
		basePath.Child("session"),
		oldSpec.Persistence.Session,
		newSpec.Persistence.Session,
	)...)

	var oldOutput, newOutput *v1alpha1.VolumeSpec
	if oldSpec.Persistence.Output != nil {
		oldOutput = &oldSpec.Persistence.Output.VolumeSpec
	}
	if newSpec.Persistence.Output != nil {
		newOutput = &newSpec.Persistence.Output.VolumeSpec
	}
	allErrs = append(allErrs, validateVolumeUpdate(
		basePath.Child("output"),
		oldOutput,
		newOutput,
	)...)

	allErrs = append(allErrs, validateVolumeUpdate(
		basePath.Child("workspace"),
		oldSpec.Persistence.Workspace,
		newSpec.Persistence.Workspace,
	)...)

	return allErrs
}

// validateVolumeUpdate checks a single volume for immutable storage class and size shrink.
func validateVolumeUpdate(fldPath *field.Path, oldVol, newVol *v1alpha1.VolumeSpec) field.ErrorList {
	if oldVol == nil || newVol == nil || !oldVol.Enabled || !newVol.Enabled {
		return nil
	}

	var allErrs field.ErrorList

	if oldVol.StorageClass != "" && newVol.StorageClass != oldVol.StorageClass {
		allErrs = append(allErrs, field.Forbidden(
			fldPath.Child("storageClass"),
			fmt.Sprintf("storage class is immutable after creation (was %q, got %q)", oldVol.StorageClass, newVol.StorageClass),
		))
	}

	oldSize, oldErr := resource.ParseQuantity(oldVol.Size)
	newSize, newErr := resource.ParseQuantity(newVol.Size)
	if oldErr == nil && newErr == nil && newSize.Cmp(oldSize) < 0 {
		allErrs = append(allErrs, field.Forbidden(
			fldPath.Child("size"),
			fmt.Sprintf("PVC size cannot be reduced (was %s, got %s)", oldVol.Size, newVol.Size),
		))
	}

	return allErrs
}
