#!/usr/bin/env bash
# k8s4claw K8s demo — run with: asciinema rec -c ./scripts/demo-k8s.sh demo-k8s.cast
set -euo pipefail

type_cmd() {
    echo ""
    echo -e "\033[1;32m$\033[0m $1"
    sleep 0.5
    eval "$1"
    sleep 1
}

clear
echo ""
echo -e "\033[1;36m╔══════════════════════════════════════════════════════╗\033[0m"
echo -e "\033[1;36m║   k8s4claw — AI Agent Runtime on Kubernetes          ║\033[0m"
echo -e "\033[1;36m║   Demo: Deploy an agent on a real K8s cluster         ║\033[0m"
echo -e "\033[1;36m╚══════════════════════════════════════════════════════╝\033[0m"
sleep 2

# Step 1: Create cluster
echo ""
echo -e "\033[1;33m▸ Step 1: Create a Kubernetes cluster (kind)\033[0m"
sleep 1
type_cmd "kind create cluster --name k8s4claw-demo --wait 60s 2>&1 | tail -4"

# Step 2: Build + load image
echo ""
echo -e "\033[1;33m▸ Step 2: Build and load OpenClaw runtime image\033[0m"
sleep 1
type_cmd "docker build -t k8s4claw-openclaw:dev -f runtimes/openclaw/Dockerfile runtimes/openclaw/ 2>&1 | tail -2"
type_cmd "kind load docker-image k8s4claw-openclaw:dev --name k8s4claw-demo 2>&1 | tail -1"

# Step 3: Install CRDs
echo ""
echo -e "\033[1;33m▸ Step 3: Install k8s4claw CRDs\033[0m"
sleep 1
type_cmd "kubectl apply -f config/crd/bases/"

# Step 4: Deploy agent
echo ""
echo -e "\033[1;33m▸ Step 4: Deploy an AI agent\033[0m"
sleep 1

cat <<'YAML' > /tmp/demo-agent.yaml
apiVersion: apps/v1
kind: StatefulSet
metadata:
  name: my-agent
  labels:
    app.kubernetes.io/name: claw
    claw.prismer.ai/runtime: openclaw
spec:
  serviceName: my-agent
  replicas: 1
  selector:
    matchLabels:
      app.kubernetes.io/name: claw
  template:
    metadata:
      labels:
        app.kubernetes.io/name: claw
    spec:
      containers:
        - name: runtime
          image: k8s4claw-openclaw:dev
          imagePullPolicy: Never
          ports:
            - containerPort: 18900
          env:
            - name: OPENCLAW_MODE
              value: mock
          readinessProbe:
            httpGet:
              path: /ready
              port: 18900
            initialDelaySeconds: 3
---
apiVersion: v1
kind: Service
metadata:
  name: my-agent
spec:
  selector:
    app.kubernetes.io/name: claw
  ports:
    - port: 18900
      targetPort: 18900
YAML

echo -e "\033[1;32m$\033[0m cat demo-agent.yaml"
sleep 0.3
echo -e "\033[0;37m  apiVersion: apps/v1"
echo "  kind: StatefulSet"
echo "  metadata:"
echo "    name: my-agent"
echo "    labels:"
echo "      claw.prismer.ai/runtime: openclaw"
echo "  spec:"
echo "    containers:"
echo "      - name: runtime"
echo "        image: k8s4claw-openclaw:dev"
echo "        env:"
echo -e "          - OPENCLAW_MODE: mock\033[0m"
sleep 1

type_cmd "kubectl apply -f /tmp/demo-agent.yaml"

# Step 5: Wait and show status
echo ""
echo -e "\033[1;33m▸ Step 5: Watch the agent come up\033[0m"
sleep 1
type_cmd "kubectl wait --for=condition=ready pod/my-agent-0 --timeout=30s"
type_cmd "kubectl get pods,svc"

# Step 6: Chat
echo ""
echo -e "\033[1;33m▸ Step 6: Chat with the agent via WebSocket\033[0m"
sleep 1

kubectl port-forward svc/my-agent 18900:18900 > /dev/null 2>&1 &
PF_PID=$!
sleep 2

python3 -c "
import asyncio, json, websockets

async def demo():
    async with websockets.connect('ws://localhost:18900') as ws:
        for q in ['Hello! What can you do?', 'Tell me about k8s4claw', 'What is your status?']:
            print(f'  \033[1;32m→ User:\033[0m {q}')
            msg = json.dumps({'id':f'demo-{hash(q)}','type':'message','channel':'slack','timestamp':'2026-04-11T00:00:00Z','payload':{'text':q,'user':'alice'}})
            await ws.send(msg)
            resp = json.loads(await asyncio.wait_for(ws.recv(), timeout=10))
            text = resp['payload']['text']
            print(f'  \033[1;34m← Agent:\033[0m')
            for line in text.split('\n'):
                print(f'    {line}')
            print()
            await asyncio.sleep(1)

asyncio.run(demo())
" 2>&1

kill $PF_PID 2>/dev/null

# Step 7: Show logs
echo ""
echo -e "\033[1;33m▸ Step 7: Agent logs\033[0m"
sleep 1
type_cmd "kubectl logs my-agent-0 --tail 5"

# Cleanup
echo ""
echo -e "\033[1;33m▸ Cleanup\033[0m"
sleep 1
type_cmd "kind delete cluster --name k8s4claw-demo 2>&1 | tail -1"

echo ""
echo -e "\033[1;36m╔══════════════════════════════════════════════════════╗\033[0m"
echo -e "\033[1;36m║   Done! From zero to AI agent on K8s in 2 minutes.   ║\033[0m"
echo -e "\033[1;36m║                                                      ║\033[0m"
echo -e "\033[1;36m║   With real Claude API:                               ║\033[0m"
echo -e "\033[1;36m║     -e ANTHROPIC_API_KEY=sk-ant-xxx                   ║\033[0m"
echo -e "\033[1;36m║                                                      ║\033[0m"
echo -e "\033[1;36m║   github.com/Prismer-AI/k8s4claw                     ║\033[0m"
echo -e "\033[1;36m╚══════════════════════════════════════════════════════╝\033[0m"
sleep 3
