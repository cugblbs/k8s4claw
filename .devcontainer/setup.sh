#!/usr/bin/env bash
set -euo pipefail

echo "╔══════════════════════════════════════════╗"
echo "║  k8s4claw — Setting up dev environment   ║"
echo "╚══════════════════════════════════════════╝"
echo ""

# Install Go tools
echo "▸ Installing Go tools..."
go install sigs.k8s.io/controller-runtime/tools/setup-envtest@latest
go install sigs.k8s.io/controller-tools/cmd/controller-gen@latest
go install golang.org/x/tools/cmd/goimports@latest
go install sigs.k8s.io/kind@latest

# Install Python websockets for demo
pip install -q websockets 2>/dev/null || true

# Download dependencies
echo "▸ Downloading Go modules..."
go mod download

# Build operator
echo "▸ Building operator..."
make build

# Build OpenClaw runtime image (used in kind demo)
echo "▸ Building OpenClaw runtime image..."
docker build -t k8s4claw-openclaw:dev -f runtimes/openclaw/Dockerfile runtimes/openclaw/

# Create kind cluster + load image
echo "▸ Creating kind cluster..."
kind create cluster --name k8s4claw --wait 60s
kind load docker-image k8s4claw-openclaw:dev --name k8s4claw

# Install CRDs
echo "▸ Installing CRDs..."
kubectl apply -f config/crd/bases/

echo ""
echo "✓ Setup complete! Kind cluster is ready."
echo ""
echo "Quick commands:"
echo "  .devcontainer/start.sh        — Deploy demo agent"
echo "  kubectl get pods              — Check status"
echo "  python3 scripts/ws-demo.py    — Chat with agent"
echo "  make test                     — Run tests"
