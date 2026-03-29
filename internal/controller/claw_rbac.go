package controller

import (
	"context"
	"fmt"

	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	clawv1alpha1 "github.com/Prismer-AI/k8s4claw/api/v1alpha1"
)

// needsRBAC returns true when the Claw requires a per-instance Role and RoleBinding.
// This is gated on spec.selfConfigure.enabled.
func needsRBAC(claw *clawv1alpha1.Claw) bool {
	return claw.Spec.SelfConfigure != nil && claw.Spec.SelfConfigure.Enabled
}

// ensureRole creates or updates the per-instance Role scoped to the Claw's
// own resources. The Role is only created when needsRBAC returns true;
// otherwise stale Roles are cleaned up.
func (r *ClawReconciler) ensureRole(ctx context.Context, claw *clawv1alpha1.Claw) error {
	roleName := claw.Name

	if !needsRBAC(claw) {
		return r.deleteOwnedRole(ctx, claw, roleName)
	}

	logger := log.FromContext(ctx)

	desired := buildRole(claw)
	if err := controllerutil.SetControllerReference(claw, desired, r.Scheme); err != nil {
		return fmt.Errorf("failed to set controller reference on Role: %w", err)
	}

	var existing rbacv1.Role
	err := r.Get(ctx, types.NamespacedName{Name: desired.Name, Namespace: desired.Namespace}, &existing)
	if apierrors.IsNotFound(err) {
		logger.Info("creating Role", "name", desired.Name, "namespace", desired.Namespace)
		if err := r.Create(ctx, desired); err != nil {
			return fmt.Errorf("failed to create Role: %w", err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("failed to get Role: %w", err)
	}

	if !metav1.IsControlledBy(&existing, claw) {
		return fmt.Errorf("role %s/%s already exists and is not owned by this Claw", existing.Namespace, existing.Name)
	}

	existing.Labels = desired.Labels
	existing.Rules = desired.Rules
	if err := r.Update(ctx, &existing); err != nil {
		return fmt.Errorf("failed to update Role: %w", err)
	}

	return nil
}

// ensureRoleBinding creates or updates the per-instance RoleBinding that binds
// the Role to the Claw's ServiceAccount.
func (r *ClawReconciler) ensureRoleBinding(ctx context.Context, claw *clawv1alpha1.Claw) error {
	bindingName := claw.Name

	if !needsRBAC(claw) {
		return r.deleteOwnedRoleBinding(ctx, claw, bindingName)
	}

	logger := log.FromContext(ctx)

	desired := buildRoleBinding(claw)
	if err := controllerutil.SetControllerReference(claw, desired, r.Scheme); err != nil {
		return fmt.Errorf("failed to set controller reference on RoleBinding: %w", err)
	}

	var existing rbacv1.RoleBinding
	err := r.Get(ctx, types.NamespacedName{Name: desired.Name, Namespace: desired.Namespace}, &existing)
	if apierrors.IsNotFound(err) {
		logger.Info("creating RoleBinding", "name", desired.Name, "namespace", desired.Namespace)
		if err := r.Create(ctx, desired); err != nil {
			return fmt.Errorf("failed to create RoleBinding: %w", err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("failed to get RoleBinding: %w", err)
	}

	if !metav1.IsControlledBy(&existing, claw) {
		return fmt.Errorf("RoleBinding %s/%s already exists and is not owned by this Claw", existing.Namespace, existing.Name)
	}

	existing.Labels = desired.Labels
	existing.RoleRef = desired.RoleRef
	existing.Subjects = desired.Subjects
	if err := r.Update(ctx, &existing); err != nil {
		return fmt.Errorf("failed to update RoleBinding: %w", err)
	}

	return nil
}

// buildRole constructs the per-instance Role allowing the Claw Pod to read its
// own CR and create ClawSelfConfig resources (Section 3.6 / 9.3.2).
func buildRole(claw *clawv1alpha1.Claw) *rbacv1.Role {
	return &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{
			Name:      claw.Name,
			Namespace: claw.Namespace,
			Labels:    clawLabels(claw),
		},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups:     []string{"claw.prismer.ai"},
				Resources:     []string{"claws"},
				Verbs:         []string{"get"},
				ResourceNames: []string{claw.Name},
			},
			{
				APIGroups: []string{"claw.prismer.ai"},
				Resources: []string{"clawselfconfigs"},
				Verbs:     []string{"create", "get", "list"},
			},
		},
	}
}

// buildRoleBinding constructs the per-instance RoleBinding.
func buildRoleBinding(claw *clawv1alpha1.Claw) *rbacv1.RoleBinding {
	return &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      claw.Name,
			Namespace: claw.Namespace,
			Labels:    clawLabels(claw),
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "Role",
			Name:     claw.Name,
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:      "ServiceAccount",
				Name:      serviceAccountName(claw),
				Namespace: claw.Namespace,
			},
		},
	}
}

// deleteOwnedRole deletes a Role if it exists and is owned by this Claw.
func (r *ClawReconciler) deleteOwnedRole(ctx context.Context, claw *clawv1alpha1.Claw, name string) error {
	var existing rbacv1.Role
	err := r.Get(ctx, types.NamespacedName{Name: name, Namespace: claw.Namespace}, &existing)
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("failed to get Role for cleanup: %w", err)
	}

	if !metav1.IsControlledBy(&existing, claw) {
		return nil
	}

	logger := log.FromContext(ctx)
	logger.Info("deleting stale Role", "name", name, "namespace", claw.Namespace)
	if err := r.Delete(ctx, &existing); err != nil {
		return fmt.Errorf("failed to delete stale Role: %w", err)
	}
	return nil
}

// deleteOwnedRoleBinding deletes a RoleBinding if it exists and is owned by this Claw.
func (r *ClawReconciler) deleteOwnedRoleBinding(ctx context.Context, claw *clawv1alpha1.Claw, name string) error {
	var existing rbacv1.RoleBinding
	err := r.Get(ctx, types.NamespacedName{Name: name, Namespace: claw.Namespace}, &existing)
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("failed to get RoleBinding for cleanup: %w", err)
	}

	if !metav1.IsControlledBy(&existing, claw) {
		return nil
	}

	logger := log.FromContext(ctx)
	logger.Info("deleting stale RoleBinding", "name", name, "namespace", claw.Namespace)
	if err := r.Delete(ctx, &existing); err != nil {
		return fmt.Errorf("failed to delete stale RoleBinding: %w", err)
	}
	return nil
}
