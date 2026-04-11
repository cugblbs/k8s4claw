#!/usr/bin/env bash
set -euo pipefail

echo "╔══════════════════════════════════════════════════╗"
echo "║  k8s4claw — Starting demo on kind cluster        ║"
echo "╚══════════════════════════════════════════════════╝"
echo ""

# Ensure kind cluster is running
if ! kind get clusters 2>/dev/null | grep -q k8s4claw; then
  echo "▸ Creating kind cluster..."
  kind create cluster --name k8s4claw --wait 60s
  kind load docker-image k8s4claw-openclaw:dev --name k8s4claw
  kubectl apply -f config/crd/bases/
fi

# Deploy demo agent (direct StatefulSet — no operator needed for demo)
echo "▸ Deploying demo agent..."
cat <<'EOF' | kubectl apply -f -
apiVersion: apps/v1
kind: StatefulSet
metadata:
  name: demo-agent
  labels:
    app.kubernetes.io/name: claw
    app.kubernetes.io/instance: demo-agent
    claw.prismer.ai/runtime: openclaw
spec:
  serviceName: demo-agent
  replicas: 1
  selector:
    matchLabels:
      app.kubernetes.io/instance: demo-agent
  template:
    metadata:
      labels:
        app.kubernetes.io/name: claw
        app.kubernetes.io/instance: demo-agent
        claw.prismer.ai/runtime: openclaw
    spec:
      containers:
        - name: runtime
          image: k8s4claw-openclaw:dev
          imagePullPolicy: Never
          ports:
            - containerPort: 18900
              name: gateway
          env:
            - name: OPENCLAW_MODE
              value: mock
            - name: OPENCLAW_MODEL
              value: claude-sonnet-4-20250514
            - name: OPENCLAW_SYSTEM_PROMPT
              value: "You are a helpful team assistant running on Kubernetes via k8s4claw."
          livenessProbe:
            httpGet:
              path: /health
              port: 18900
            initialDelaySeconds: 5
            periodSeconds: 10
          readinessProbe:
            httpGet:
              path: /ready
              port: 18900
            initialDelaySeconds: 3
            periodSeconds: 5
          resources:
            requests:
              cpu: 50m
              memory: 64Mi
            limits:
              cpu: 200m
              memory: 128Mi
---
apiVersion: v1
kind: Service
metadata:
  name: demo-agent
  labels:
    app.kubernetes.io/name: claw
    app.kubernetes.io/instance: demo-agent
spec:
  selector:
    app.kubernetes.io/instance: demo-agent
  ports:
    - port: 18900
      targetPort: 18900
      name: gateway
EOF

echo "▸ Waiting for pod to be ready..."
kubectl wait --for=condition=ready pod/demo-agent-0 --timeout=60s 2>/dev/null || true

echo ""
echo "▸ Cluster status:"
kubectl get pods,svc -l app.kubernetes.io/instance=demo-agent
echo ""

# Port-forward
echo "▸ Starting port-forward on :18900..."
pkill -f 'kubectl port-forward.*demo-agent' 2>/dev/null || true
kubectl port-forward svc/demo-agent 18900:18900 > /dev/null 2>&1 &
sleep 2

echo ""
echo "╔══════════════════════════════════════════════════╗"
echo "║  Demo agent running on kind!                     ║"
echo "║                                                  ║"
echo "║  Check status:                                   ║"
echo "║    kubectl get pods                              ║"
echo "║    kubectl logs demo-agent-0                     ║"
echo "║                                                  ║"
echo "║  Chat with the agent:                            ║"
echo "║    python3 scripts/ws-demo.py                    ║"
echo "║                                                  ║"
echo "║  Clean up:                                       ║"
echo "║    kubectl delete sts,svc demo-agent             ║"
echo "║    kind delete cluster --name k8s4claw           ║"
echo "╚══════════════════════════════════════════════════╝"
