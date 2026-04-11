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
  kind load docker-image claw-ipcbus:dev --name k8s4claw
  kind load docker-image claw-init:dev --name k8s4claw
  kubectl apply -f config/crd/bases/
fi

# Start operator in background
echo "▸ Starting operator..."
pkill -f 'bin/operator' 2>/dev/null || true
nohup bin/operator > /tmp/operator.log 2>&1 &
OPERATOR_PID=$!
echo "  Operator PID: $OPERATOR_PID (logs: /tmp/operator.log)"
sleep 3

# Create demo secret (mock mode doesn't need real keys)
echo "▸ Creating demo secret..."
kubectl create secret generic llm-api-keys \
  --from-literal=ANTHROPIC_API_KEY=mock-key \
  --dry-run=client -o yaml | kubectl apply -f -

# Deploy the demo agent
echo "▸ Deploying demo agent..."
cat <<EOF | kubectl apply -f -
apiVersion: claw.prismer.ai/v1alpha1
kind: Claw
metadata:
  name: demo-agent
spec:
  runtime: openclaw
  image: k8s4claw-openclaw:dev
  config:
    model: "claude-sonnet-4-20250514"
    systemPrompt: "You are a helpful team assistant."
  credentials:
    secretRef:
      name: llm-api-keys
EOF

echo ""
echo "▸ Waiting for agent..."
sleep 5

# Show status
echo ""
echo "▸ Cluster status:"
kubectl get claws 2>/dev/null || echo "  (CRDs loading...)"
echo ""
kubectl get pods 2>/dev/null || true

# Port-forward for WebSocket access
echo ""
echo "▸ Setting up port-forward to agent on :18900..."
pkill -f 'kubectl port-forward.*demo-agent' 2>/dev/null || true
kubectl port-forward statefulset/demo-agent 18900:18900 > /dev/null 2>&1 &
sleep 2

echo ""
echo "╔══════════════════════════════════════════════════╗"
echo "║  Demo agent deployed on kind cluster!            ║"
echo "║                                                  ║"
echo "║  Check status:                                   ║"
echo "║    kubectl get claws                             ║"
echo "║    kubectl get pods                              ║"
echo "║    kubectl describe claw demo-agent              ║"
echo "║                                                  ║"
echo "║  Chat with the agent:                            ║"
echo "║    python3 scripts/ws-demo.py                    ║"
echo "║                                                  ║"
echo "║  View operator logs:                             ║"
echo "║    tail -f /tmp/operator.log                     ║"
echo "║                                                  ║"
echo "║  Clean up:                                       ║"
echo "║    kubectl delete claw demo-agent                ║"
echo "║    kind delete cluster --name k8s4claw           ║"
echo "╚══════════════════════════════════════════════════╝"
