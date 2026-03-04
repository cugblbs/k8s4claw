package controller

import (
	"context"
	"crypto/sha256"
	"fmt"
	"sort"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"

	clawv1alpha1 "github.com/Prismer-AI/k8s4claw/api/v1alpha1"
)

// injectCredentials modifies the pod template to include credential sources.
func (r *ClawReconciler) injectCredentials(ctx context.Context, claw *clawv1alpha1.Claw, podTemplate *corev1.PodTemplateSpec) error {
	if claw.Spec.Credentials == nil {
		return nil
	}

	// Find the runtime container index.
	runtimeIdx := -1
	for i, c := range podTemplate.Spec.Containers {
		if c.Name == "runtime" {
			runtimeIdx = i
			break
		}
	}
	if runtimeIdx == -1 {
		return fmt.Errorf("runtime container not found in pod template")
	}

	creds := claw.Spec.Credentials

	// Handle secretRef: inject envFrom + hash annotation for rolling updates.
	if creds.SecretRef != nil {
		podTemplate.Spec.Containers[runtimeIdx].EnvFrom = append(
			podTemplate.Spec.Containers[runtimeIdx].EnvFrom,
			corev1.EnvFromSource{
				SecretRef: &corev1.SecretEnvSource{
					LocalObjectReference: *creds.SecretRef,
				},
			},
		)

		hash, err := r.computeSecretHash(ctx, claw.Namespace, creds.SecretRef.Name)
		if err != nil {
			return fmt.Errorf("failed to compute secret hash: %w", err)
		}

		if podTemplate.Annotations == nil {
			podTemplate.Annotations = make(map[string]string)
		}
		podTemplate.Annotations["claw.prismer.ai/secret-hash"] = hash
	}

	// Handle per-key mappings.
	for _, km := range creds.Keys {
		podTemplate.Spec.Containers[runtimeIdx].Env = append(
			podTemplate.Spec.Containers[runtimeIdx].Env,
			corev1.EnvVar{
				Name: km.Name,
				ValueFrom: &corev1.EnvVarSource{
					SecretKeyRef: &corev1.SecretKeySelector{
						LocalObjectReference: km.SecretKeyRef.LocalObjectReference,
						Key:                  km.SecretKeyRef.Key,
					},
				},
			},
		)
	}

	return nil
}

// computeSecretHash fetches a Secret and returns a SHA-256 hash of its data.
func (r *ClawReconciler) computeSecretHash(ctx context.Context, namespace, name string) (string, error) {
	var secret corev1.Secret
	if err := r.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, &secret); err != nil {
		return "", fmt.Errorf("failed to get Secret %s/%s: %w", namespace, name, err)
	}

	h := sha256.New()
	keys := make([]string, 0, len(secret.Data))
	for k := range secret.Data {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		h.Write([]byte(k))
		h.Write([]byte("="))
		h.Write(secret.Data[k])
	}
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}
