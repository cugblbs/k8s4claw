package controller

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	clawv1alpha1 "github.com/Prismer-AI/k8s4claw/api/v1alpha1"
)

// ensureNetworkPolicy creates, updates, or deletes the per-instance NetworkPolicy.
func (r *ClawReconciler) ensureNetworkPolicy(ctx context.Context, claw *clawv1alpha1.Claw, gatewayPort int) error {
	npName := claw.Name + "-netpol"

	// If security is nil or networkPolicy is disabled, clean up any existing NetworkPolicy.
	if !networkPolicyEnabled(claw) {
		return r.deleteOwnedNetworkPolicy(ctx, claw, npName)
	}

	logger := log.FromContext(ctx)

	desired := buildNetworkPolicy(claw, gatewayPort)
	if err := controllerutil.SetControllerReference(claw, desired, r.Scheme); err != nil {
		return fmt.Errorf("failed to set controller reference on NetworkPolicy: %w", err)
	}

	var existing networkingv1.NetworkPolicy
	err := r.Get(ctx, types.NamespacedName{Name: desired.Name, Namespace: desired.Namespace}, &existing)
	if apierrors.IsNotFound(err) {
		logger.Info("creating NetworkPolicy", "name", desired.Name, "namespace", desired.Namespace)
		if err := r.Create(ctx, desired); err != nil {
			return fmt.Errorf("failed to create NetworkPolicy: %w", err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("failed to get NetworkPolicy: %w", err)
	}

	if !metav1.IsControlledBy(&existing, claw) {
		return fmt.Errorf("NetworkPolicy %s/%s already exists and is not owned by this Claw", existing.Namespace, existing.Name)
	}

	existing.Labels = desired.Labels
	existing.Spec = desired.Spec
	if err := r.Update(ctx, &existing); err != nil {
		return fmt.Errorf("failed to update NetworkPolicy: %w", err)
	}

	return nil
}

// networkPolicyEnabled returns true when a NetworkPolicy should be created.
// Defaults to false (opt-in) when security spec is nil.
func networkPolicyEnabled(claw *clawv1alpha1.Claw) bool {
	if claw.Spec.Security == nil || claw.Spec.Security.NetworkPolicy == nil {
		return false
	}
	return claw.Spec.Security.NetworkPolicy.Enabled
}

// buildNetworkPolicy constructs the desired NetworkPolicy for a Claw instance.
// It implements a default-deny policy with selective allow rules:
//   - Egress: DNS (53 UDP+TCP), HTTPS (443 TCP), user-defined CIDRs
//   - Ingress: same-namespace pods, optionally cross-namespace and ingress-controller
func buildNetworkPolicy(claw *clawv1alpha1.Claw, gatewayPort int) *networkingv1.NetworkPolicy {
	labels := clawLabels(claw)
	protocolUDP := corev1.ProtocolUDP
	protocolTCP := corev1.ProtocolTCP
	dnsPort := intstr.FromInt32(53)
	httpsPort := intstr.FromInt32(443)
	gwPort := intstr.FromInt32(int32(gatewayPort))

	// Egress rules: DNS + HTTPS always allowed.
	egressRules := []networkingv1.NetworkPolicyEgressRule{
		{
			// DNS (UDP + TCP)
			Ports: []networkingv1.NetworkPolicyPort{
				{Protocol: &protocolUDP, Port: &dnsPort},
				{Protocol: &protocolTCP, Port: &dnsPort},
			},
		},
		{
			// HTTPS
			Ports: []networkingv1.NetworkPolicyPort{
				{Protocol: &protocolTCP, Port: &httpsPort},
			},
		},
	}

	// User-defined egress CIDRs.
	if claw.Spec.Security != nil && claw.Spec.Security.NetworkPolicy != nil {
		for _, cidr := range claw.Spec.Security.NetworkPolicy.AllowedEgressCIDRs {
			egressRules = append(egressRules, networkingv1.NetworkPolicyEgressRule{
				To: []networkingv1.NetworkPolicyPeer{
					{
						IPBlock: &networkingv1.IPBlock{CIDR: cidr},
					},
				},
			})
		}
	}

	// Ingress rules: same-namespace pods on gateway port.
	ingressRules := []networkingv1.NetworkPolicyIngressRule{
		{
			From: []networkingv1.NetworkPolicyPeer{
				{
					// Same namespace (empty podSelector = all pods in namespace).
					PodSelector: &metav1.LabelSelector{},
				},
			},
			Ports: []networkingv1.NetworkPolicyPort{
				{Protocol: &protocolTCP, Port: &gwPort},
			},
		},
	}

	// Cross-namespace ingress.
	if claw.Spec.Security != nil && claw.Spec.Security.NetworkPolicy != nil {
		for _, ns := range claw.Spec.Security.NetworkPolicy.AllowedIngressNamespaces {
			ingressRules = append(ingressRules, networkingv1.NetworkPolicyIngressRule{
				From: []networkingv1.NetworkPolicyPeer{
					{
						NamespaceSelector: &metav1.LabelSelector{
							MatchLabels: map[string]string{
								"kubernetes.io/metadata.name": ns,
							},
						},
					},
				},
				Ports: []networkingv1.NetworkPolicyPort{
					{Protocol: &protocolTCP, Port: &gwPort},
				},
			})
		}
	}

	// If Ingress is enabled, allow ingress-controller access.
	if claw.Spec.Ingress != nil && claw.Spec.Ingress.Enabled {
		ingressRules = append(ingressRules, networkingv1.NetworkPolicyIngressRule{
			From: []networkingv1.NetworkPolicyPeer{
				{
					NamespaceSelector: &metav1.LabelSelector{
						MatchLabels: map[string]string{
							"kubernetes.io/metadata.name": "ingress-nginx",
						},
					},
				},
			},
			Ports: []networkingv1.NetworkPolicyPort{
				{Protocol: &protocolTCP, Port: &gwPort},
			},
		})
	}

	return &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      claw.Name + "-netpol",
			Namespace: claw.Namespace,
			Labels:    labels,
		},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{
				MatchLabels: map[string]string{
					"claw.prismer.ai/instance": claw.Name,
				},
			},
			PolicyTypes: []networkingv1.PolicyType{
				networkingv1.PolicyTypeIngress,
				networkingv1.PolicyTypeEgress,
			},
			Ingress: ingressRules,
			Egress:  egressRules,
		},
	}
}

// deleteOwnedNetworkPolicy deletes a NetworkPolicy if it exists and is owned by this Claw.
func (r *ClawReconciler) deleteOwnedNetworkPolicy(ctx context.Context, claw *clawv1alpha1.Claw, name string) error {
	var existing networkingv1.NetworkPolicy
	err := r.Get(ctx, types.NamespacedName{Name: name, Namespace: claw.Namespace}, &existing)
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("failed to get NetworkPolicy for cleanup: %w", err)
	}

	if !metav1.IsControlledBy(&existing, claw) {
		return nil
	}

	logger := log.FromContext(ctx)
	logger.Info("deleting stale NetworkPolicy", "name", name, "namespace", claw.Namespace)
	if err := r.Delete(ctx, &existing); err != nil {
		return fmt.Errorf("failed to delete stale NetworkPolicy: %w", err)
	}
	return nil
}
