package controller

import (
	"context"
	"fmt"

	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	clawv1alpha1 "github.com/Prismer-AI/k8s4claw/api/v1alpha1"
)

// ensureIngress creates, updates, or deletes the Ingress for the given Claw.
func (r *ClawReconciler) ensureIngress(ctx context.Context, claw *clawv1alpha1.Claw, gatewayPort int) error {
	ingressName := claw.Name

	if claw.Spec.Ingress == nil || !claw.Spec.Ingress.Enabled {
		return r.deleteOwnedIngress(ctx, claw, ingressName)
	}

	logger := log.FromContext(ctx)

	desired := buildIngress(claw, gatewayPort)
	if err := controllerutil.SetControllerReference(claw, desired, r.Scheme); err != nil {
		return fmt.Errorf("failed to set controller reference on Ingress: %w", err)
	}

	var existing networkingv1.Ingress
	err := r.Get(ctx, types.NamespacedName{Name: desired.Name, Namespace: desired.Namespace}, &existing)
	if apierrors.IsNotFound(err) {
		logger.Info("creating Ingress", "name", desired.Name, "namespace", desired.Namespace)
		if err := r.Create(ctx, desired); err != nil {
			return fmt.Errorf("failed to create Ingress: %w", err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("failed to get Ingress: %w", err)
	}

	if !metav1.IsControlledBy(&existing, claw) {
		return fmt.Errorf("Ingress %s/%s already exists and is not owned by this Claw", existing.Namespace, existing.Name)
	}

	existing.Labels = desired.Labels
	existing.Annotations = desired.Annotations
	existing.Spec = desired.Spec
	if err := r.Update(ctx, &existing); err != nil {
		return fmt.Errorf("failed to update Ingress: %w", err)
	}

	return nil
}

// buildIngress constructs the desired Ingress for a Claw instance.
func buildIngress(claw *clawv1alpha1.Claw, gatewayPort int) *networkingv1.Ingress {
	labels := clawLabels(claw)
	pathType := networkingv1.PathTypePrefix

	annotations := make(map[string]string)

	// Basic auth annotations.
	if claw.Spec.Ingress.BasicAuth != nil && claw.Spec.Ingress.BasicAuth.Enabled {
		annotations["nginx.ingress.kubernetes.io/auth-type"] = "basic"
		annotations["nginx.ingress.kubernetes.io/auth-secret"] = claw.Spec.Ingress.BasicAuth.SecretName
		annotations["nginx.ingress.kubernetes.io/auth-realm"] = fmt.Sprintf("Authentication Required - %s", claw.Name)
	}

	// Merge user annotations on top.
	for k, v := range claw.Spec.Ingress.Annotations {
		annotations[k] = v
	}

	ing := &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{
			Name:        claw.Name,
			Namespace:   claw.Namespace,
			Labels:      labels,
			Annotations: annotations,
		},
		Spec: networkingv1.IngressSpec{
			Rules: []networkingv1.IngressRule{
				{
					Host: claw.Spec.Ingress.Host,
					IngressRuleValue: networkingv1.IngressRuleValue{
						HTTP: &networkingv1.HTTPIngressRuleValue{
							Paths: []networkingv1.HTTPIngressPath{
								{
									Path:     "/",
									PathType: &pathType,
									Backend: networkingv1.IngressBackend{
										Service: &networkingv1.IngressServiceBackend{
											Name: claw.Name,
											Port: networkingv1.ServiceBackendPort{
												Number: int32(gatewayPort),
											},
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}

	// IngressClassName.
	if claw.Spec.Ingress.ClassName != "" {
		ing.Spec.IngressClassName = &claw.Spec.Ingress.ClassName
	}

	// TLS.
	if claw.Spec.Ingress.TLS != nil {
		ing.Spec.TLS = []networkingv1.IngressTLS{
			{
				Hosts:      []string{claw.Spec.Ingress.Host},
				SecretName: claw.Spec.Ingress.TLS.SecretName,
			},
		}
	}

	return ing
}

// deleteOwnedIngress deletes an Ingress if it exists and is owned by this Claw.
func (r *ClawReconciler) deleteOwnedIngress(ctx context.Context, claw *clawv1alpha1.Claw, name string) error {
	var existing networkingv1.Ingress
	err := r.Get(ctx, types.NamespacedName{Name: name, Namespace: claw.Namespace}, &existing)
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("failed to get Ingress for cleanup: %w", err)
	}

	if !metav1.IsControlledBy(&existing, claw) {
		return nil
	}

	logger := log.FromContext(ctx)
	logger.Info("deleting stale Ingress", "name", name, "namespace", claw.Namespace)
	if err := r.Delete(ctx, &existing); err != nil {
		return fmt.Errorf("failed to delete stale Ingress: %w", err)
	}
	return nil
}
