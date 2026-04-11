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

# Install kind
echo "▸ Installing kind..."
go install sigs.k8s.io/kind@latest

# Install Python websockets for demo
pip install -q websockets 2>/dev/null || true

# Download dependencies
echo "▸ Downloading Go modules..."
go mod download

# Build binaries
echo "▸ Building operator..."
make build
make build-ipcbus

# Build all container images
echo "▸ Building container images (this takes ~2 min)..."
docker build -t k8s4claw-openclaw:dev -f runtimes/openclaw/Dockerfile runtimes/openclaw/
docker build -t claw-ipcbus:dev -f Dockerfile.ipcbus .
docker build -t claw-init:dev -f Dockerfile.init .
docker build -t claw-channel-slack:dev -f Dockerfile.channel-slack .
docker build -t claw-channel-webhook:dev -f Dockerfile.channel-webhook .

# Create kind cluster
echo "▸ Creating kind cluster..."
kind create cluster --name k8s4claw --wait 60s 2>/dev/null || true

# Load images into kind (avoids GHCR pull)
echo "▸ Loading images into kind cluster..."
kind load docker-image k8s4claw-openclaw:dev --name k8s4claw
kind load docker-image claw-ipcbus:dev --name k8s4claw
kind load docker-image claw-init:dev --name k8s4claw
kind load docker-image claw-channel-slack:dev --name k8s4claw
kind load docker-image claw-channel-webhook:dev --name k8s4claw

# Install CRDs
echo "▸ Installing CRDs..."
kubectl apply -f config/crd/bases/

echo ""
echo "✓ Setup complete!"
echo ""
echo "Quick commands:"
echo "  .devcontainer/start.sh        — Deploy demo agent on kind cluster"
echo "  kubectl get claws             — List running agents"
echo "  python3 scripts/ws-demo.py    — Chat with agent via WebSocket"
echo "  make test                     — Run all tests"
echo "  make run                      — Run operator locally"
