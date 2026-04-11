#!/usr/bin/env bash
set -euo pipefail

echo "╔══════════════════════════════════════════╗"
echo "║  k8s4claw — Setting up dev environment   ║"
echo "╚══════════════════════════════════════════╝"

# Install Go tools
echo "▸ Installing Go tools..."
go install sigs.k8s.io/controller-runtime/tools/setup-envtest@latest
go install sigs.k8s.io/controller-tools/cmd/controller-gen@latest
go install golang.org/x/tools/cmd/goimports@latest

# Download dependencies
echo "▸ Downloading Go modules..."
go mod download

# Build binaries
echo "▸ Building operator..."
make build

# Build OpenClaw runtime image (for demo)
echo "▸ Building OpenClaw runtime image..."
docker build -t k8s4claw-openclaw:dev -f runtimes/openclaw/Dockerfile runtimes/openclaw/

echo ""
echo "✓ Setup complete!"
echo ""
echo "Quick commands:"
echo "  make test              — Run all tests"
echo "  make lint              — Run linter"
echo "  .devcontainer/start.sh — Start OpenClaw demo"
echo "  make run               — Run operator (needs a K8s cluster)"
