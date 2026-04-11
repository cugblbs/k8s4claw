#!/usr/bin/env bash
set -euo pipefail

# Stop any existing demo container
docker rm -f openclaw-demo 2>/dev/null || true

echo "▸ Starting OpenClaw runtime in mock mode on :18900..."
docker run --rm -d \
  --name openclaw-demo \
  -p 18900:18900 \
  -e OPENCLAW_MODE=mock \
  k8s4claw-openclaw:dev

sleep 1
echo ""
echo "╔══════════════════════════════════════════════════╗"
echo "║  OpenClaw runtime is running on port 18900       ║"
echo "║                                                  ║"
echo "║  Try it:                                         ║"
echo "║    curl http://localhost:18900/health             ║"
echo "║                                                  ║"
echo "║  Send a message via WebSocket:                   ║"
echo "║    python3 scripts/ws-demo.py                    ║"
echo "║                                                  ║"
echo "║  Stop:                                           ║"
echo "║    docker stop openclaw-demo                     ║"
echo "╚══════════════════════════════════════════════════╝"
