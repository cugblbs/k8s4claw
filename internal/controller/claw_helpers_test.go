package controller

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	clawv1alpha1 "github.com/Prismer-AI/k8s4claw/api/v1alpha1"
)

// ---------------------------------------------------------------------------
// claw_pdb.go — additional coverage
// ---------------------------------------------------------------------------

func TestPdbEnabled_AllBranches(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		claw *clawv1alpha1.Claw
		want bool
	}{
		{"nil availability", &clawv1alpha1.Claw{}, true},
		{"nil pdb", &clawv1alpha1.Claw{Spec: clawv1alpha1.ClawSpec{Availability: &clawv1alpha1.AvailabilitySpec{}}}, true},
		{"enabled true", &clawv1alpha1.Claw{Spec: clawv1alpha1.ClawSpec{Availability: &clawv1alpha1.AvailabilitySpec{PDB: &clawv1alpha1.PDBSpec{Enabled: true}}}}, true},
		{"enabled false", &clawv1alpha1.Claw{Spec: clawv1alpha1.ClawSpec{Availability: &clawv1alpha1.AvailabilitySpec{PDB: &clawv1alpha1.PDBSpec{Enabled: false}}}}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := pdbEnabled(tt.claw); got != tt.want {
				t.Errorf("pdbEnabled() = %v; want %v", got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// claw_pvc.go — additional coverage
// ---------------------------------------------------------------------------

func TestHasOwnerReference_Empty(t *testing.T) {
	t.Parallel()
	claw := &clawv1alpha1.Claw{ObjectMeta: metav1.ObjectMeta{UID: "uid-1"}}
	pvc := &corev1.PersistentVolumeClaim{}
	if hasOwnerReference(pvc, claw) {
		t.Error("expected false for empty refs")
	}
}

func TestRemoveOwnerRef_NoMatch(t *testing.T) {
	t.Parallel()
	gvk := schema.GroupVersionKind{}
	refs := []metav1.OwnerReference{{UID: "uid-1", Name: "a"}}
	result := removeOwnerRef(refs, types.UID("uid-99"), gvk)
	if len(result) != 1 {
		t.Errorf("len = %d; want 1", len(result))
	}
}

func TestRemoveOwnerRef_MultipleRefs(t *testing.T) {
	t.Parallel()
	gvk := schema.GroupVersionKind{Group: "claw.prismer.ai", Version: "v1alpha1", Kind: "Claw"}
	refs := []metav1.OwnerReference{
		{UID: "uid-1", Name: "a"},
		{UID: "uid-2", Name: "b"},
		{UID: "uid-3", Name: "c"},
	}
	result := removeOwnerRef(refs, types.UID("uid-2"), gvk)
	if len(result) != 2 {
		t.Fatalf("len = %d; want 2", len(result))
	}
	for _, r := range result {
		if r.UID == "uid-2" {
			t.Error("uid-2 should have been removed")
		}
	}
}

func TestBoolPtr_Values(t *testing.T) {
	t.Parallel()
	tr := boolPtr(true)
	fa := boolPtr(false)
	if *tr != true || *fa != false {
		t.Error("boolPtr mismatch")
	}
}

// ---------------------------------------------------------------------------
// claw_ingress.go — additional: user annotations override basic auth
// ---------------------------------------------------------------------------

func TestBuildIngress_UserAnnotationsOverrideAuth(t *testing.T) {
	t.Parallel()
	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{Name: "c", Namespace: "ns"},
		Spec: clawv1alpha1.ClawSpec{
			Ingress: &clawv1alpha1.IngressSpec{
				Enabled:   true,
				Host:      "h.com",
				BasicAuth: &clawv1alpha1.BasicAuthSpec{Enabled: true, SecretName: "htpasswd"},
				Annotations: map[string]string{
					"nginx.ingress.kubernetes.io/auth-type": "custom-override",
				},
			},
		},
	}
	ing := buildIngress(claw, 3000)
	// User annotation should override the basic auth annotation.
	if ing.Annotations["nginx.ingress.kubernetes.io/auth-type"] != "custom-override" {
		t.Errorf("expected custom-override, got %s", ing.Annotations["nginx.ingress.kubernetes.io/auth-type"])
	}
}

func TestBuildIngress_WithClassName(t *testing.T) {
	t.Parallel()
	claw := &clawv1alpha1.Claw{
		ObjectMeta: metav1.ObjectMeta{Name: "c", Namespace: "ns"},
		Spec: clawv1alpha1.ClawSpec{
			Ingress: &clawv1alpha1.IngressSpec{
				Enabled:   true,
				Host:      "h.com",
				ClassName: "nginx",
			},
		},
	}
	ing := buildIngress(claw, 18900)
	if ing.Spec.IngressClassName == nil || *ing.Spec.IngressClassName != "nginx" {
		t.Error("expected className=nginx")
	}
}

// ---------------------------------------------------------------------------
// claw_archiver.go — additional coverage
// ---------------------------------------------------------------------------

func TestFormatMountPath_AllBranches(t *testing.T) {
	t.Parallel()

	t.Run("with output mount", func(t *testing.T) {
		t.Parallel()
		claw := &clawv1alpha1.Claw{
			Spec: clawv1alpha1.ClawSpec{Persistence: &clawv1alpha1.PersistenceSpec{
				Output: &clawv1alpha1.OutputVolumeSpec{VolumeSpec: clawv1alpha1.VolumeSpec{MountPath: "/data/out"}},
			}},
		}
		if got := formatMountPath(claw); got != "/data/out" {
			t.Errorf("formatMountPath = %q", got)
		}
	})

	t.Run("nil persistence fallback", func(t *testing.T) {
		t.Parallel()
		claw := &clawv1alpha1.Claw{ObjectMeta: metav1.ObjectMeta{Name: "x"}}
		if got := formatMountPath(claw); got != "/data/output/x" {
			t.Errorf("formatMountPath = %q", got)
		}
	})

	t.Run("nil output fallback", func(t *testing.T) {
		t.Parallel()
		claw := &clawv1alpha1.Claw{
			ObjectMeta: metav1.ObjectMeta{Name: "y"},
			Spec:       clawv1alpha1.ClawSpec{Persistence: &clawv1alpha1.PersistenceSpec{}},
		}
		if got := formatMountPath(claw); got != "/data/output/y" {
			t.Errorf("formatMountPath = %q", got)
		}
	})
}

func TestInjectArchiverIfNeeded_SkipsWhenNotNeeded(t *testing.T) {
	t.Parallel()
	claw := &clawv1alpha1.Claw{}
	pt := &corev1.PodTemplateSpec{}
	injectArchiverIfNeeded(claw, pt)
	if len(pt.Spec.InitContainers) != 0 {
		t.Error("expected no injection")
	}
}

func TestInjectArchiverIfNeeded_NoDuplicate(t *testing.T) {
	t.Parallel()
	claw := &clawv1alpha1.Claw{
		Spec: clawv1alpha1.ClawSpec{Persistence: &clawv1alpha1.PersistenceSpec{
			Output: &clawv1alpha1.OutputVolumeSpec{
				VolumeSpec: clawv1alpha1.VolumeSpec{MountPath: "/out"},
				Archive: &clawv1alpha1.ArchiveSpec{
					Enabled:     true,
					Destination: clawv1alpha1.ArchiveDestination{Type: "s3", Bucket: "b", SecretRef: corev1.LocalObjectReference{Name: "s"}},
					Trigger:     clawv1alpha1.ArchiveTrigger{Schedule: "0 * * * *"},
				},
			},
		}},
	}
	pt := &corev1.PodTemplateSpec{}
	injectArchiverIfNeeded(claw, pt)
	if len(pt.Spec.InitContainers) != 1 {
		t.Fatalf("expected 1, got %d", len(pt.Spec.InitContainers))
	}
	// Call again — should not double-inject.
	injectArchiverIfNeeded(claw, pt)
	if len(pt.Spec.InitContainers) != 1 {
		t.Errorf("double injection: got %d", len(pt.Spec.InitContainers))
	}
}

func TestInjectArchiverSidecar_WithLifecycle(t *testing.T) {
	t.Parallel()
	claw := &clawv1alpha1.Claw{
		Spec: clawv1alpha1.ClawSpec{Persistence: &clawv1alpha1.PersistenceSpec{
			Output: &clawv1alpha1.OutputVolumeSpec{
				VolumeSpec: clawv1alpha1.VolumeSpec{MountPath: "/out"},
				Archive: &clawv1alpha1.ArchiveSpec{
					Enabled:     true,
					Destination: clawv1alpha1.ArchiveDestination{Type: "s3", Bucket: "b", SecretRef: corev1.LocalObjectReference{Name: "s"}},
					Trigger:     clawv1alpha1.ArchiveTrigger{Schedule: "0 * * * *", Inotify: true},
					Lifecycle:   &clawv1alpha1.ArchiveLifecycle{LocalRetention: "7d", Compress: true},
				},
			},
		}},
	}
	pt := &corev1.PodTemplateSpec{}
	injectArchiverSidecar(claw, pt)
	if len(pt.Spec.InitContainers) != 1 {
		t.Fatalf("expected 1, got %d", len(pt.Spec.InitContainers))
	}
	args := pt.Spec.InitContainers[0].Args
	hasInotify, hasRetention, hasCompress := false, false, false
	for i, a := range args {
		if a == "--inotify" {
			hasInotify = true
		}
		if a == "--local-retention" && i+1 < len(args) && args[i+1] == "7d" {
			hasRetention = true
		}
		if a == "--compress" {
			hasCompress = true
		}
	}
	if !hasInotify {
		t.Error("missing --inotify")
	}
	if !hasRetention {
		t.Error("missing --local-retention 7d")
	}
	if !hasCompress {
		t.Error("missing --compress")
	}
}

// ---------------------------------------------------------------------------
// claw_rbac.go — additional: needsRBAC enabled
// ---------------------------------------------------------------------------

func TestNeedsRBAC_AllBranches(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		claw *clawv1alpha1.Claw
		want bool
	}{
		{"nil selfConfigure", &clawv1alpha1.Claw{}, false},
		{"disabled", &clawv1alpha1.Claw{Spec: clawv1alpha1.ClawSpec{SelfConfigure: &clawv1alpha1.SelfConfigureSpec{Enabled: false}}}, false},
		{"enabled", &clawv1alpha1.Claw{Spec: clawv1alpha1.ClawSpec{SelfConfigure: &clawv1alpha1.SelfConfigureSpec{Enabled: true}}}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := needsRBAC(tt.claw); got != tt.want {
				t.Errorf("needsRBAC() = %v; want %v", got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// selfconfig_controller.go — validateActions
// ---------------------------------------------------------------------------

func TestValidateActions_AllCases(t *testing.T) {
	t.Parallel()
	r := &ClawSelfConfigReconciler{}

	tests := []struct {
		name    string
		sc      *clawv1alpha1.ClawSelfConfig
		allowed []string
		wantErr bool
	}{
		{
			name:    "skills allowed",
			sc:      &clawv1alpha1.ClawSelfConfig{Spec: clawv1alpha1.ClawSelfConfigSpec{AddSkills: []string{"a"}}},
			allowed: []string{"skills"},
		},
		{
			name:    "skills denied",
			sc:      &clawv1alpha1.ClawSelfConfig{Spec: clawv1alpha1.ClawSelfConfigSpec{AddSkills: []string{"a"}}},
			allowed: []string{"config"},
			wantErr: true,
		},
		{
			name:    "removeSkills denied",
			sc:      &clawv1alpha1.ClawSelfConfig{Spec: clawv1alpha1.ClawSelfConfigSpec{RemoveSkills: []string{"a"}}},
			allowed: []string{},
			wantErr: true,
		},
		{
			name:    "config denied",
			sc:      &clawv1alpha1.ClawSelfConfig{Spec: clawv1alpha1.ClawSelfConfigSpec{ConfigPatch: map[string]string{"k": "v"}}},
			allowed: []string{},
			wantErr: true,
		},
		{
			name:    "workspaceFiles denied",
			sc:      &clawv1alpha1.ClawSelfConfig{Spec: clawv1alpha1.ClawSelfConfigSpec{AddWorkspaceFiles: map[string]string{"f": "c"}}},
			allowed: []string{},
			wantErr: true,
		},
		{
			name:    "removeWorkspaceFiles denied",
			sc:      &clawv1alpha1.ClawSelfConfig{Spec: clawv1alpha1.ClawSelfConfigSpec{RemoveWorkspaceFiles: []string{"f"}}},
			allowed: []string{},
			wantErr: true,
		},
		{
			name:    "envVars denied",
			sc:      &clawv1alpha1.ClawSelfConfig{Spec: clawv1alpha1.ClawSelfConfigSpec{AddEnvVars: []clawv1alpha1.EnvVar{{Name: "K", Value: "V"}}}},
			allowed: []string{},
			wantErr: true,
		},
		{
			name:    "removeEnvVars denied",
			sc:      &clawv1alpha1.ClawSelfConfig{Spec: clawv1alpha1.ClawSelfConfigSpec{RemoveEnvVars: []string{"K"}}},
			allowed: []string{},
			wantErr: true,
		},
		{
			name:    "addSkills exceeds limit",
			sc:      &clawv1alpha1.ClawSelfConfig{Spec: clawv1alpha1.ClawSelfConfigSpec{AddSkills: make([]string, 11)}},
			allowed: []string{"skills"},
			wantErr: true,
		},
		{
			name:    "removeSkills exceeds limit",
			sc:      &clawv1alpha1.ClawSelfConfig{Spec: clawv1alpha1.ClawSelfConfigSpec{RemoveSkills: make([]string, 11)}},
			allowed: []string{"skills"},
			wantErr: true,
		},
		{
			name:    "removeWorkspaceFiles exceeds limit",
			sc:      &clawv1alpha1.ClawSelfConfig{Spec: clawv1alpha1.ClawSelfConfigSpec{RemoveWorkspaceFiles: make([]string, 11)}},
			allowed: []string{"workspaceFiles"},
			wantErr: true,
		},
		{
			name:    "addEnvVars exceeds limit",
			sc:      &clawv1alpha1.ClawSelfConfig{Spec: clawv1alpha1.ClawSelfConfigSpec{AddEnvVars: make([]clawv1alpha1.EnvVar, 11)}},
			allowed: []string{"envVars"},
			wantErr: true,
		},
		{
			name:    "removeEnvVars exceeds limit",
			sc:      &clawv1alpha1.ClawSelfConfig{Spec: clawv1alpha1.ClawSelfConfigSpec{RemoveEnvVars: make([]string, 11)}},
			allowed: []string{"envVars"},
			wantErr: true,
		},
		{
			name:    "empty spec passes",
			sc:      &clawv1alpha1.ClawSelfConfig{},
			allowed: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := r.validateActions(tt.sc, tt.allowed)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateActions() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
