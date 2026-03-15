# Helm Chart Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Create a production-ready Helm chart at `charts/k8s4claw/` that packages the k8s4claw operator with configurable webhook TLS (cert-manager or self-signed), CRD lifecycle hooks, and optional Prometheus monitoring.

**Architecture:** Templatize existing Kustomize manifests from `config/` into Helm templates with `_helpers.tpl` for shared labels/names. CRDs use `helm.sh/hook` for install+upgrade lifecycle. Webhook TLS has two mutually exclusive paths: cert-manager (default) or self-signed Job fallback.

**Tech Stack:** Helm 3, Kubernetes 1.28+, cert-manager (optional), Prometheus Operator (optional)

**Design doc:** `docs/plans/2026-03-15-helm-chart-design.md`

---

### Task 1: Chart Scaffolding + values.yaml

**Files:**
- Create: `charts/k8s4claw/Chart.yaml`
- Create: `charts/k8s4claw/values.yaml`
- Create: `charts/k8s4claw/.helmignore`

**Step 1: Create Chart.yaml**

Create `charts/k8s4claw/Chart.yaml`:

```yaml
apiVersion: v2
name: k8s4claw
description: Kubernetes operator for managing Claw AI agent runtimes
type: application
version: 0.1.0
appVersion: "0.1.0"
kubeVersion: ">=1.28.0-0"
home: https://github.com/Prismer-AI/k8s4claw
maintainers:
  - name: Prismer AI
sources:
  - https://github.com/Prismer-AI/k8s4claw
keywords:
  - kubernetes
  - operator
  - ai
  - claw
```

**Step 2: Create values.yaml**

Create `charts/k8s4claw/values.yaml` with the full schema from the design doc:

```yaml
image:
  repository: ghcr.io/prismer-ai/k8s4claw
  tag: ""  # defaults to Chart.appVersion
  pullPolicy: IfNotPresent

replicaCount: 1

resources:
  requests:
    cpu: 100m
    memory: 128Mi
  limits:
    cpu: 500m
    memory: 256Mi

serviceAccount:
  create: true
  name: ""  # auto-generated if empty
  annotations: {}

leaderElection:
  enabled: true

nativeSidecars:
  enabled: true  # requires Kubernetes 1.28+

webhook:
  port: 9443
  certManager:
    enabled: true
    issuerRef:
      kind: Issuer
      name: ""  # auto-created if empty
  selfSigned:
    enabled: false  # auto-enabled when certManager.enabled=false

monitoring:
  enabled: false
  serviceMonitor:
    interval: 30s
    labels: {}
  prometheusRule:
    enabled: true

nodeSelector: {}
tolerations: []
affinity: {}
```

**Step 3: Create .helmignore**

Create `charts/k8s4claw/.helmignore`:

```
.git
.gitignore
*.md
```

**Step 4: Verify chart metadata parses**

Run: `helm lint charts/k8s4claw/ 2>&1 | head -20`
Expected: Warnings about missing templates (no errors about Chart.yaml/values.yaml)

**Step 5: Commit**

```bash
git add charts/k8s4claw/Chart.yaml charts/k8s4claw/values.yaml charts/k8s4claw/.helmignore
git commit -m "feat(helm): scaffold chart with Chart.yaml and values.yaml"
```

---

### Task 2: _helpers.tpl

**Files:**
- Create: `charts/k8s4claw/templates/_helpers.tpl`

**Step 1: Create _helpers.tpl**

Create `charts/k8s4claw/templates/_helpers.tpl`:

```gotemplate
{{/*
Expand the name of the chart.
*/}}
{{- define "k8s4claw.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
*/}}
{{- define "k8s4claw.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "k8s4claw.labels" -}}
helm.sh/chart: {{ include "k8s4claw.chart" . }}
{{ include "k8s4claw.selectorLabels" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/part-of: k8s4claw
{{- end }}

{{/*
Selector labels
*/}}
{{- define "k8s4claw.selectorLabels" -}}
app.kubernetes.io/name: {{ include "k8s4claw.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Chart label
*/}}
{{- define "k8s4claw.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
ServiceAccount name
*/}}
{{- define "k8s4claw.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "k8s4claw.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Webhook service name — fixed name for webhook config references
*/}}
{{- define "k8s4claw.webhookServiceName" -}}
{{- printf "%s-webhook" (include "k8s4claw.fullname" .) }}
{{- end }}

{{/*
Webhook cert secret name
*/}}
{{- define "k8s4claw.webhookCertSecretName" -}}
{{- printf "%s-webhook-cert" (include "k8s4claw.fullname" .) }}
{{- end }}

{{/*
Whether to use cert-manager for webhook TLS
*/}}
{{- define "k8s4claw.useCertManager" -}}
{{- if .Values.webhook.certManager.enabled }}true{{- end }}
{{- end }}

{{/*
Operator image
*/}}
{{- define "k8s4claw.image" -}}
{{- printf "%s:%s" .Values.image.repository (default .Chart.AppVersion .Values.image.tag) }}
{{- end }}
```

**Step 2: Verify lint passes**

Run: `helm lint charts/k8s4claw/ 2>&1 | head -20`
Expected: Warnings about missing templates only (no template parse errors)

**Step 3: Commit**

```bash
git add charts/k8s4claw/templates/_helpers.tpl
git commit -m "feat(helm): add template helpers for labels, names, and image"
```

---

### Task 3: RBAC Templates (ServiceAccount + ClusterRole + ClusterRoleBinding)

**Files:**
- Create: `charts/k8s4claw/templates/serviceaccount.yaml`
- Create: `charts/k8s4claw/templates/clusterrole.yaml`
- Create: `charts/k8s4claw/templates/clusterrolebinding.yaml`
- Reference: `config/rbac/role.yaml` (lines 1-55, copy rules verbatim)
- Reference: `config/rbac/role_binding.yaml`
- Reference: `config/rbac/service_account.yaml`

**Step 1: Create serviceaccount.yaml**

Create `charts/k8s4claw/templates/serviceaccount.yaml`:

```yaml
{{- if .Values.serviceAccount.create }}
apiVersion: v1
kind: ServiceAccount
metadata:
  name: {{ include "k8s4claw.serviceAccountName" . }}
  namespace: {{ .Release.Namespace }}
  labels:
    {{- include "k8s4claw.labels" . | nindent 4 }}
  {{- with .Values.serviceAccount.annotations }}
  annotations:
    {{- toYaml . | nindent 4 }}
  {{- end }}
{{- end }}
```

**Step 2: Create clusterrole.yaml**

Create `charts/k8s4claw/templates/clusterrole.yaml`. Copy all `rules` from `config/rbac/role.yaml` verbatim:

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: {{ include "k8s4claw.fullname" . }}
  labels:
    {{- include "k8s4claw.labels" . | nindent 4 }}
rules:
  # Core resources
  - apiGroups: [""]
    resources: [pods, services, persistentvolumeclaims, secrets, events, configmaps, serviceaccounts]
    verbs: [get, list, watch, create, update, patch, delete]

  # Apps (StatefulSets)
  - apiGroups: ["apps"]
    resources: [statefulsets]
    verbs: [get, list, watch, create, update, patch, delete]

  # CSI VolumeSnapshots
  - apiGroups: ["snapshot.storage.k8s.io"]
    resources: [volumesnapshots]
    verbs: [get, list, watch, create, delete]

  # Networking (NetworkPolicy + Ingress)
  - apiGroups: ["networking.k8s.io"]
    resources: [networkpolicies, ingresses]
    verbs: [get, list, watch, create, update, patch, delete]

  # Policy (PodDisruptionBudgets)
  - apiGroups: ["policy"]
    resources: [poddisruptionbudgets]
    verbs: [get, list, watch, create, update, patch, delete]

  # RBAC (per-instance Role + RoleBinding for selfConfigure)
  - apiGroups: ["rbac.authorization.k8s.io"]
    resources: [roles, rolebindings]
    verbs: [get, list, watch, create, update, patch, delete]

  # CRDs
  - apiGroups: ["claw.prismer.ai"]
    resources: [claws, claws/status, clawchannels, clawchannels/status]
    verbs: [get, list, watch, create, update, patch, delete]

  # CRD finalizer subresources
  - apiGroups: ["claw.prismer.ai"]
    resources: [claws/finalizers, clawchannels/finalizers]
    verbs: [update]

  # ExternalSecrets (optional)
  - apiGroups: ["external-secrets.io"]
    resources: [externalsecrets]
    verbs: [get, list, watch, create, update, patch, delete]

  # Leader election
  - apiGroups: ["coordination.k8s.io"]
    resources: [leases]
    verbs: [get, list, watch, create, update, patch, delete]
```

**Step 3: Create clusterrolebinding.yaml**

Create `charts/k8s4claw/templates/clusterrolebinding.yaml`:

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: {{ include "k8s4claw.fullname" . }}
  labels:
    {{- include "k8s4claw.labels" . | nindent 4 }}
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: {{ include "k8s4claw.fullname" . }}
subjects:
  - kind: ServiceAccount
    name: {{ include "k8s4claw.serviceAccountName" . }}
    namespace: {{ .Release.Namespace }}
```

**Step 4: Verify lint passes**

Run: `helm lint charts/k8s4claw/ 2>&1 | head -20`
Expected: No errors

**Step 5: Commit**

```bash
git add charts/k8s4claw/templates/serviceaccount.yaml \
        charts/k8s4claw/templates/clusterrole.yaml \
        charts/k8s4claw/templates/clusterrolebinding.yaml
git commit -m "feat(helm): add RBAC templates (SA, ClusterRole, ClusterRoleBinding)"
```

---

### Task 4: Deployment + Webhook Service

**Files:**
- Create: `charts/k8s4claw/templates/deployment.yaml`
- Create: `charts/k8s4claw/templates/service.yaml`
- Reference: `config/manager/deployment.yaml` (lines 1-72)

**Step 1: Create deployment.yaml**

Create `charts/k8s4claw/templates/deployment.yaml`. Templatize the existing deployment from `config/manager/deployment.yaml`:

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: {{ include "k8s4claw.fullname" . }}
  namespace: {{ .Release.Namespace }}
  labels:
    {{- include "k8s4claw.labels" . | nindent 4 }}
spec:
  replicas: {{ .Values.replicaCount }}
  selector:
    matchLabels:
      {{- include "k8s4claw.selectorLabels" . | nindent 6 }}
  template:
    metadata:
      labels:
        {{- include "k8s4claw.selectorLabels" . | nindent 8 }}
    spec:
      serviceAccountName: {{ include "k8s4claw.serviceAccountName" . }}
      terminationGracePeriodSeconds: 10
      containers:
        - name: operator
          image: {{ include "k8s4claw.image" . }}
          imagePullPolicy: {{ .Values.image.pullPolicy }}
          args:
            - --health-probe-bind-address=:8081
            - --metrics-bind-address=:8080
            - --leader-elect={{ .Values.leaderElection.enabled }}
            - --enable-native-sidecars={{ .Values.nativeSidecars.enabled }}
            - --webhook-port={{ .Values.webhook.port }}
          ports:
            - name: metrics
              containerPort: 8080
              protocol: TCP
            - name: health
              containerPort: 8081
              protocol: TCP
            - name: webhook
              containerPort: {{ .Values.webhook.port }}
              protocol: TCP
          livenessProbe:
            httpGet:
              path: /healthz
              port: 8081
            initialDelaySeconds: 15
            periodSeconds: 20
          readinessProbe:
            httpGet:
              path: /readyz
              port: 8081
            initialDelaySeconds: 5
            periodSeconds: 10
          volumeMounts:
            - name: webhook-server-cert
              mountPath: /tmp/k8s-webhook-server/serving-certs
              readOnly: true
          resources:
            {{- toYaml .Values.resources | nindent 12 }}
          securityContext:
            allowPrivilegeEscalation: false
            readOnlyRootFilesystem: true
            runAsNonRoot: true
      volumes:
        - name: webhook-server-cert
          secret:
            secretName: {{ include "k8s4claw.webhookCertSecretName" . }}
            defaultMode: 420
      securityContext:
        runAsNonRoot: true
      {{- with .Values.nodeSelector }}
      nodeSelector:
        {{- toYaml . | nindent 8 }}
      {{- end }}
      {{- with .Values.affinity }}
      affinity:
        {{- toYaml . | nindent 8 }}
      {{- end }}
      {{- with .Values.tolerations }}
      tolerations:
        {{- toYaml . | nindent 8 }}
      {{- end }}
```

**Step 2: Create service.yaml (webhook Service)**

Create `charts/k8s4claw/templates/service.yaml`. This Service is missing from the existing manifests — webhook configs reference it:

```yaml
apiVersion: v1
kind: Service
metadata:
  name: {{ include "k8s4claw.webhookServiceName" . }}
  namespace: {{ .Release.Namespace }}
  labels:
    {{- include "k8s4claw.labels" . | nindent 4 }}
spec:
  ports:
    - name: webhook
      port: 443
      targetPort: {{ .Values.webhook.port }}
      protocol: TCP
  selector:
    {{- include "k8s4claw.selectorLabels" . | nindent 4 }}
```

**Step 3: Verify lint passes**

Run: `helm lint charts/k8s4claw/ 2>&1 | head -20`
Expected: No errors

**Step 4: Verify template renders correctly**

Run: `helm template test charts/k8s4claw/ 2>&1 | grep -A 2 "kind: Deployment"`
Expected: Shows Deployment with correct metadata

**Step 5: Commit**

```bash
git add charts/k8s4claw/templates/deployment.yaml \
        charts/k8s4claw/templates/service.yaml
git commit -m "feat(helm): add operator Deployment and webhook Service templates"
```

---

### Task 5: CRD Templates with Hooks

**Files:**
- Create: `charts/k8s4claw/templates/crds/claw.yaml`
- Create: `charts/k8s4claw/templates/crds/clawchannel.yaml`
- Create: `charts/k8s4claw/templates/crds/clawselfconfig.yaml`
- Reference: `config/crd/bases/claw.prismer.ai_claws.yaml` (863 lines)
- Reference: `config/crd/bases/claw.prismer.ai_clawchannels.yaml` (884 lines)
- Reference: `config/crd/bases/claw.prismer.ai_clawselfconfigs.yaml` (124 lines)

**Step 1: Create CRD templates**

For each CRD, copy the entire content from `config/crd/bases/` and add Helm hook annotations after the existing `controller-gen` annotation. The CRD content is 800+ lines each — do NOT modify the schema, only add annotations.

For `charts/k8s4claw/templates/crds/claw.yaml`, take the full content of `config/crd/bases/claw.prismer.ai_claws.yaml` and replace the `metadata` block:

```yaml
# Original metadata (in config/crd/bases/):
metadata:
  annotations:
    controller-gen.kubebuilder.io/version: v0.20.1
  name: claws.claw.prismer.ai

# Replace with:
metadata:
  annotations:
    controller-gen.kubebuilder.io/version: v0.20.1
    "helm.sh/hook": pre-install,pre-upgrade
    "helm.sh/hook-weight": "-5"
    "helm.sh/resource-policy": keep
  name: claws.claw.prismer.ai
  labels:
    {{- include "k8s4claw.labels" . | nindent 4 }}
```

Apply the same pattern to `clawchannel.yaml` (from `claw.prismer.ai_clawchannels.yaml`) and `clawselfconfig.yaml` (from `claw.prismer.ai_clawselfconfigs.yaml`).

**Step 2: Verify lint passes**

Run: `helm lint charts/k8s4claw/ 2>&1 | head -20`
Expected: No errors

**Step 3: Verify CRD hooks render correctly**

Run: `helm template test charts/k8s4claw/ 2>&1 | grep -B 1 "helm.sh/hook"`
Expected: Three CRDs with `pre-install,pre-upgrade` hooks

**Step 4: Commit**

```bash
git add charts/k8s4claw/templates/crds/
git commit -m "feat(helm): add CRD templates with helm hook lifecycle annotations"
```

---

### Task 6: Webhook Configurations

**Files:**
- Create: `charts/k8s4claw/templates/webhook-configs.yaml`
- Reference: `config/webhook/manifests.yaml` (lines 1-45)

**Step 1: Create webhook-configs.yaml**

Create `charts/k8s4claw/templates/webhook-configs.yaml`. Templatize the existing webhook configs, adding cert-manager annotation when applicable:

```yaml
apiVersion: admissionregistration.k8s.io/v1
kind: MutatingWebhookConfiguration
metadata:
  name: {{ include "k8s4claw.fullname" . }}-mutating
  labels:
    {{- include "k8s4claw.labels" . | nindent 4 }}
  {{- if include "k8s4claw.useCertManager" . }}
  annotations:
    cert-manager.io/inject-ca-from: {{ .Release.Namespace }}/{{ include "k8s4claw.fullname" . }}-webhook
  {{- end }}
webhooks:
  - name: mclaw.kb.io
    admissionReviewVersions: ["v1"]
    clientConfig:
      service:
        name: {{ include "k8s4claw.webhookServiceName" . }}
        namespace: {{ .Release.Namespace }}
        path: /mutate-claw-prismer-ai-v1alpha1-claw
      {{- if not (include "k8s4claw.useCertManager" .) }}
      caBundle: ""  # patched by self-signed cert Job
      {{- end }}
    failurePolicy: Fail
    sideEffects: None
    rules:
      - apiGroups: ["claw.prismer.ai"]
        apiVersions: ["v1alpha1"]
        operations: ["CREATE", "UPDATE"]
        resources: ["claws"]
---
apiVersion: admissionregistration.k8s.io/v1
kind: ValidatingWebhookConfiguration
metadata:
  name: {{ include "k8s4claw.fullname" . }}-validating
  labels:
    {{- include "k8s4claw.labels" . | nindent 4 }}
  {{- if include "k8s4claw.useCertManager" . }}
  annotations:
    cert-manager.io/inject-ca-from: {{ .Release.Namespace }}/{{ include "k8s4claw.fullname" . }}-webhook
  {{- end }}
webhooks:
  - name: vclaw.kb.io
    admissionReviewVersions: ["v1"]
    clientConfig:
      service:
        name: {{ include "k8s4claw.webhookServiceName" . }}
        namespace: {{ .Release.Namespace }}
        path: /validate-claw-prismer-ai-v1alpha1-claw
      {{- if not (include "k8s4claw.useCertManager" .) }}
      caBundle: ""  # patched by self-signed cert Job
      {{- end }}
    failurePolicy: Fail
    sideEffects: None
    rules:
      - apiGroups: ["claw.prismer.ai"]
        apiVersions: ["v1alpha1"]
        operations: ["CREATE", "UPDATE"]
        resources: ["claws"]
```

**Step 2: Verify lint passes**

Run: `helm lint charts/k8s4claw/ 2>&1 | head -20`
Expected: No errors

**Step 3: Commit**

```bash
git add charts/k8s4claw/templates/webhook-configs.yaml
git commit -m "feat(helm): add webhook configuration templates with cert-manager support"
```

---

### Task 7: cert-manager TLS (Issuer + Certificate)

**Files:**
- Create: `charts/k8s4claw/templates/cert-manager.yaml`

**Step 1: Create cert-manager.yaml**

Create `charts/k8s4claw/templates/cert-manager.yaml`:

```yaml
{{- if include "k8s4claw.useCertManager" . }}
{{- $issuerName := default (printf "%s-selfsigned" (include "k8s4claw.fullname" .)) .Values.webhook.certManager.issuerRef.name -}}
apiVersion: cert-manager.io/v1
kind: Issuer
metadata:
  name: {{ $issuerName }}
  namespace: {{ .Release.Namespace }}
  labels:
    {{- include "k8s4claw.labels" . | nindent 4 }}
spec:
  selfSigned: {}
---
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: {{ include "k8s4claw.fullname" . }}-webhook
  namespace: {{ .Release.Namespace }}
  labels:
    {{- include "k8s4claw.labels" . | nindent 4 }}
spec:
  secretName: {{ include "k8s4claw.webhookCertSecretName" . }}
  duration: 8760h  # 1 year
  renewBefore: 720h  # 30 days
  issuerRef:
    kind: {{ .Values.webhook.certManager.issuerRef.kind }}
    name: {{ $issuerName }}
  dnsNames:
    - {{ include "k8s4claw.webhookServiceName" . }}
    - {{ include "k8s4claw.webhookServiceName" . }}.{{ .Release.Namespace }}
    - {{ include "k8s4claw.webhookServiceName" . }}.{{ .Release.Namespace }}.svc
    - {{ include "k8s4claw.webhookServiceName" . }}.{{ .Release.Namespace }}.svc.cluster.local
{{- end }}
```

**Step 2: Verify template renders with cert-manager enabled (default)**

Run: `helm template test charts/k8s4claw/ 2>&1 | grep -A 5 "kind: Certificate"`
Expected: Certificate resource with correct secretName and dnsNames

**Step 3: Verify template renders WITHOUT cert-manager**

Run: `helm template test charts/k8s4claw/ --set webhook.certManager.enabled=false 2>&1 | grep "kind: Certificate" | wc -l`
Expected: `0` (no Certificate rendered)

**Step 4: Commit**

```bash
git add charts/k8s4claw/templates/cert-manager.yaml
git commit -m "feat(helm): add cert-manager Issuer and Certificate for webhook TLS"
```

---

### Task 8: Self-Signed Cert Fallback (Job + RBAC)

**Files:**
- Create: `charts/k8s4claw/templates/selfsigned-cert.yaml`

**Step 1: Create selfsigned-cert.yaml**

Create `charts/k8s4claw/templates/selfsigned-cert.yaml`. This renders when cert-manager is disabled and creates a pre-install/pre-upgrade Job that generates a self-signed cert, creates the Secret, and patches webhook configs:

```yaml
{{- if not (include "k8s4claw.useCertManager" .) }}
apiVersion: v1
kind: ServiceAccount
metadata:
  name: {{ include "k8s4claw.fullname" . }}-cert-gen
  namespace: {{ .Release.Namespace }}
  labels:
    {{- include "k8s4claw.labels" . | nindent 4 }}
  annotations:
    "helm.sh/hook": pre-install,pre-upgrade
    "helm.sh/hook-weight": "-4"
    "helm.sh/hook-delete-policy": before-hook-creation,hook-succeeded
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: {{ include "k8s4claw.fullname" . }}-cert-gen
  labels:
    {{- include "k8s4claw.labels" . | nindent 4 }}
  annotations:
    "helm.sh/hook": pre-install,pre-upgrade
    "helm.sh/hook-weight": "-4"
    "helm.sh/hook-delete-policy": before-hook-creation,hook-succeeded
rules:
  - apiGroups: [""]
    resources: [secrets]
    verbs: [get, create, update, patch]
  - apiGroups: ["admissionregistration.k8s.io"]
    resources: [mutatingwebhookconfigurations, validatingwebhookconfigurations]
    verbs: [get, update, patch]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: {{ include "k8s4claw.fullname" . }}-cert-gen
  labels:
    {{- include "k8s4claw.labels" . | nindent 4 }}
  annotations:
    "helm.sh/hook": pre-install,pre-upgrade
    "helm.sh/hook-weight": "-4"
    "helm.sh/hook-delete-policy": before-hook-creation,hook-succeeded
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: {{ include "k8s4claw.fullname" . }}-cert-gen
subjects:
  - kind: ServiceAccount
    name: {{ include "k8s4claw.fullname" . }}-cert-gen
    namespace: {{ .Release.Namespace }}
---
apiVersion: batch/v1
kind: Job
metadata:
  name: {{ include "k8s4claw.fullname" . }}-cert-gen
  namespace: {{ .Release.Namespace }}
  labels:
    {{- include "k8s4claw.labels" . | nindent 4 }}
  annotations:
    "helm.sh/hook": pre-install,pre-upgrade
    "helm.sh/hook-weight": "-3"
    "helm.sh/hook-delete-policy": before-hook-creation,hook-succeeded
spec:
  backoffLimit: 3
  template:
    metadata:
      labels:
        {{- include "k8s4claw.selectorLabels" . | nindent 8 }}
        app.kubernetes.io/component: cert-gen
    spec:
      serviceAccountName: {{ include "k8s4claw.fullname" . }}-cert-gen
      restartPolicy: OnFailure
      containers:
        - name: cert-gen
          image: registry.k8s.io/ingress-nginx/kube-webhook-certgen:v1.5.0
          args:
            - create
            - --host={{ include "k8s4claw.webhookServiceName" . }},{{ include "k8s4claw.webhookServiceName" . }}.{{ .Release.Namespace }}.svc
            - --namespace={{ .Release.Namespace }}
            - --secret-name={{ include "k8s4claw.webhookCertSecretName" . }}
            - --cert-name=tls.crt
            - --key-name=tls.key
          securityContext:
            allowPrivilegeEscalation: false
            readOnlyRootFilesystem: true
            runAsNonRoot: true
---
apiVersion: batch/v1
kind: Job
metadata:
  name: {{ include "k8s4claw.fullname" . }}-cert-patch
  namespace: {{ .Release.Namespace }}
  labels:
    {{- include "k8s4claw.labels" . | nindent 4 }}
  annotations:
    "helm.sh/hook": post-install,post-upgrade
    "helm.sh/hook-weight": "5"
    "helm.sh/hook-delete-policy": before-hook-creation,hook-succeeded
spec:
  backoffLimit: 3
  template:
    metadata:
      labels:
        {{- include "k8s4claw.selectorLabels" . | nindent 8 }}
        app.kubernetes.io/component: cert-patch
    spec:
      serviceAccountName: {{ include "k8s4claw.fullname" . }}-cert-gen
      restartPolicy: OnFailure
      containers:
        - name: cert-patch
          image: registry.k8s.io/ingress-nginx/kube-webhook-certgen:v1.5.0
          args:
            - patch
            - --namespace={{ .Release.Namespace }}
            - --secret-name={{ include "k8s4claw.webhookCertSecretName" . }}
            - --webhook-name={{ include "k8s4claw.fullname" . }}-mutating,{{ include "k8s4claw.fullname" . }}-validating
            - --patch-mutating=true
            - --patch-validating=true
          securityContext:
            allowPrivilegeEscalation: false
            readOnlyRootFilesystem: true
            runAsNonRoot: true
{{- end }}
```

**Step 2: Verify template renders when cert-manager is disabled**

Run: `helm template test charts/k8s4claw/ --set webhook.certManager.enabled=false 2>&1 | grep "kind: Job"`
Expected: Two Job resources (cert-gen and cert-patch)

**Step 3: Verify template does NOT render when cert-manager is enabled (default)**

Run: `helm template test charts/k8s4claw/ 2>&1 | grep "kind: Job" | wc -l`
Expected: `0`

**Step 4: Commit**

```bash
git add charts/k8s4claw/templates/selfsigned-cert.yaml
git commit -m "feat(helm): add self-signed cert fallback Job for webhook TLS"
```

---

### Task 9: Monitoring Templates (ServiceMonitor + PrometheusRule)

**Files:**
- Create: `charts/k8s4claw/templates/monitoring/servicemonitor.yaml`
- Create: `charts/k8s4claw/templates/monitoring/prometheusrule.yaml`
- Reference: `config/prometheus/servicemonitor.yaml`
- Reference: `config/prometheus/prometheusrule.yaml` (lines 1-91)

**Step 1: Create servicemonitor.yaml**

Create `charts/k8s4claw/templates/monitoring/servicemonitor.yaml`:

```yaml
{{- if .Values.monitoring.enabled }}
apiVersion: monitoring.coreos.com/v1
kind: ServiceMonitor
metadata:
  name: {{ include "k8s4claw.fullname" . }}
  namespace: {{ .Release.Namespace }}
  labels:
    {{- include "k8s4claw.labels" . | nindent 4 }}
    {{- with .Values.monitoring.serviceMonitor.labels }}
    {{- toYaml . | nindent 4 }}
    {{- end }}
spec:
  selector:
    matchLabels:
      {{- include "k8s4claw.selectorLabels" . | nindent 6 }}
  endpoints:
    - port: metrics
      interval: {{ .Values.monitoring.serviceMonitor.interval }}
      path: /metrics
{{- end }}
```

**Step 2: Create prometheusrule.yaml**

Create `charts/k8s4claw/templates/monitoring/prometheusrule.yaml`. Copy all rules from `config/prometheus/prometheusrule.yaml`:

```yaml
{{- if and .Values.monitoring.enabled .Values.monitoring.prometheusRule.enabled }}
apiVersion: monitoring.coreos.com/v1
kind: PrometheusRule
metadata:
  name: {{ include "k8s4claw.fullname" . }}-alerts
  namespace: {{ .Release.Namespace }}
  labels:
    {{- include "k8s4claw.labels" . | nindent 4 }}
    {{- with .Values.monitoring.serviceMonitor.labels }}
    {{- toYaml . | nindent 4 }}
    {{- end }}
spec:
  groups:
    - name: k8s4claw.rules
      rules:
        - alert: ClawReconcileErrors
          expr: rate(claw_reconcile_total{result="error"}[5m]) > 0
          for: 5m
          labels:
            severity: warning
            service: k8s4claw
          annotations:
            summary: "Claw reconcile errors detected"

        - alert: ClawInstanceDegraded
          expr: claw_instance_phase{phase=~"Failed|Degraded"} == 1
          for: 2m
          labels:
            severity: critical
            service: k8s4claw
          annotations:
            summary: "Claw instance {{`{{ $labels.instance }}`}} in {{`{{ $labels.phase }}`}} state"

        - alert: ClawSlowReconciliation
          expr: histogram_quantile(0.99, rate(claw_reconcile_duration_seconds_bucket[10m])) > 30
          for: 10m
          labels:
            severity: warning
            service: k8s4claw
          annotations:
            summary: "Claw reconcile P99 latency above 30s"

        - alert: ClawPodCrashLooping
          expr: increase(kube_pod_container_status_restarts_total{container=~"claw-.*"}[10m]) > 2
          for: 0m
          labels:
            severity: critical
            service: k8s4claw
          annotations:
            summary: "Claw pod {{`{{ $labels.pod }}`}} crash looping"

        - alert: ClawPodOOMKilled
          expr: kube_pod_container_status_last_terminated_reason{reason="OOMKilled", container=~"claw-.*"} == 1
          for: 0m
          labels:
            severity: warning
            service: k8s4claw
          annotations:
            summary: "Claw pod {{`{{ $labels.pod }}`}} OOM killed"

        - alert: ClawPVCNearlyFull
          expr: kubelet_volume_stats_used_bytes{persistentvolumeclaim=~"claw-.*"} / kubelet_volume_stats_capacity_bytes{persistentvolumeclaim=~"claw-.*"} > 0.85
          for: 5m
          labels:
            severity: warning
            service: k8s4claw
          annotations:
            summary: "Claw PVC {{`{{ $labels.persistentvolumeclaim }}`}} above 85%"

        - alert: ClawDLQBacklog
          expr: claw_ipcbus_dlq_size > 100
          for: 5m
          labels:
            severity: warning
            service: k8s4claw
          annotations:
            summary: "IPC Bus DLQ has {{`{{ $value }}`}} pending entries"

        - alert: ClawChannelDisconnected
          expr: claw_ipcbus_sidecar_connections == 0 and claw_ipcbus_bridge_connected == 1
          for: 5m
          labels:
            severity: warning
            service: k8s4claw
          annotations:
            summary: "No channel sidecars connected to IPC Bus"
{{- end }}
```

Note: Prometheus template variables like `{{ $labels.pod }}` must be escaped with backtick-wrapping (`{{` `` ` ``}} ... {{` `` ` ``}}`) to prevent Helm from interpreting them.

**Step 3: Verify monitoring templates render when enabled**

Run: `helm template test charts/k8s4claw/ --set monitoring.enabled=true 2>&1 | grep "kind: ServiceMonitor\|kind: PrometheusRule"`
Expected: Both ServiceMonitor and PrometheusRule

**Step 4: Verify monitoring templates do NOT render by default**

Run: `helm template test charts/k8s4claw/ 2>&1 | grep "kind: ServiceMonitor\|kind: PrometheusRule" | wc -l`
Expected: `0`

**Step 5: Commit**

```bash
git add charts/k8s4claw/templates/monitoring/
git commit -m "feat(helm): add conditional ServiceMonitor and PrometheusRule templates"
```

---

### Task 10: Final Validation + Metrics Service

**Files:**
- Create: `charts/k8s4claw/templates/metrics-service.yaml`

The ServiceMonitor needs a Service with a `metrics` port to scrape. The webhook Service only exposes port 443. We need a separate metrics Service.

**Step 1: Create metrics-service.yaml**

Create `charts/k8s4claw/templates/metrics-service.yaml`:

```yaml
{{- if .Values.monitoring.enabled }}
apiVersion: v1
kind: Service
metadata:
  name: {{ include "k8s4claw.fullname" . }}-metrics
  namespace: {{ .Release.Namespace }}
  labels:
    {{- include "k8s4claw.labels" . | nindent 4 }}
spec:
  ports:
    - name: metrics
      port: 8080
      targetPort: 8080
      protocol: TCP
  selector:
    {{- include "k8s4claw.selectorLabels" . | nindent 4 }}
{{- end }}
```

**Step 2: Update ServiceMonitor selector**

The ServiceMonitor in Task 9 uses selector labels that match the metrics Service. Verify it will find the metrics Service correctly — both share `k8s4claw.selectorLabels`.

**Step 3: Run full lint**

Run: `helm lint charts/k8s4claw/`
Expected: `1 chart(s) linted, 0 chart(s) failed`

**Step 4: Run template with default values**

Run: `helm template test charts/k8s4claw/ 2>&1 | grep "^kind:" | sort | uniq -c | sort -rn`
Expected output (approximate):
```
  3 kind: CustomResourceDefinition
  1 kind: Deployment
  1 kind: ServiceAccount
  1 kind: Service
  1 kind: ClusterRole
  1 kind: ClusterRoleBinding
  1 kind: MutatingWebhookConfiguration
  1 kind: ValidatingWebhookConfiguration
  1 kind: Issuer
  1 kind: Certificate
```

**Step 5: Run template with self-signed + monitoring**

Run: `helm template test charts/k8s4claw/ --set webhook.certManager.enabled=false --set monitoring.enabled=true 2>&1 | grep "^kind:" | sort | uniq -c | sort -rn`
Expected: Same base resources, plus Jobs (cert-gen, cert-patch), ServiceMonitor, PrometheusRule, metrics Service, cert-gen RBAC, minus Issuer/Certificate

**Step 6: Commit**

```bash
git add charts/k8s4claw/templates/metrics-service.yaml
git commit -m "feat(helm): add metrics Service for Prometheus scraping"
```

---

### Task 11: Helm Chart Tests

**Files:**
- Create: `charts/k8s4claw/templates/tests/test-connection.yaml`

**Step 1: Create basic connection test**

Create `charts/k8s4claw/templates/tests/test-connection.yaml`:

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: {{ include "k8s4claw.fullname" . }}-test
  namespace: {{ .Release.Namespace }}
  labels:
    {{- include "k8s4claw.labels" . | nindent 4 }}
  annotations:
    "helm.sh/hook": test
spec:
  containers:
    - name: health-check
      image: busybox:1.36
      command: ["wget"]
      args:
        - --spider
        - --timeout=5
        - http://{{ include "k8s4claw.fullname" . }}-metrics.{{ .Release.Namespace }}.svc:8080/healthz
  restartPolicy: Never
```

Note: This test pod hits the metrics port (8080) at `/healthz` — this won't match the actual health endpoint (which is on 8081). Use a simpler DNS resolution test instead:

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: {{ include "k8s4claw.fullname" . }}-test
  namespace: {{ .Release.Namespace }}
  labels:
    {{- include "k8s4claw.labels" . | nindent 4 }}
  annotations:
    "helm.sh/hook": test
spec:
  containers:
    - name: webhook-check
      image: busybox:1.36
      command: ["sh", "-c"]
      args:
        - "nslookup {{ include "k8s4claw.webhookServiceName" . }}.{{ .Release.Namespace }}.svc.cluster.local"
  restartPolicy: Never
```

**Step 2: Run full lint one more time**

Run: `helm lint charts/k8s4claw/`
Expected: `1 chart(s) linted, 0 chart(s) failed`

**Step 3: Commit**

```bash
git add charts/k8s4claw/templates/tests/
git commit -m "feat(helm): add helm test for webhook service DNS resolution"
```

---

### Summary

| Task | Description | Files |
|------|-------------|-------|
| 1 | Chart scaffolding | `Chart.yaml`, `values.yaml`, `.helmignore` |
| 2 | Template helpers | `_helpers.tpl` |
| 3 | RBAC templates | `serviceaccount.yaml`, `clusterrole.yaml`, `clusterrolebinding.yaml` |
| 4 | Deployment + webhook Service | `deployment.yaml`, `service.yaml` |
| 5 | CRD templates with hooks | `crds/claw.yaml`, `crds/clawchannel.yaml`, `crds/clawselfconfig.yaml` |
| 6 | Webhook configurations | `webhook-configs.yaml` |
| 7 | cert-manager TLS | `cert-manager.yaml` |
| 8 | Self-signed cert fallback | `selfsigned-cert.yaml` |
| 9 | Monitoring templates | `monitoring/servicemonitor.yaml`, `monitoring/prometheusrule.yaml` |
| 10 | Metrics Service + validation | `metrics-service.yaml` |
| 11 | Helm test | `tests/test-connection.yaml` |
