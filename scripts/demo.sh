#!/usr/bin/env bash
# k8s4claw demo script — run with: asciinema rec -c ./scripts/demo.sh demo.cast
set -euo pipefail

# Typing simulation
type_cmd() {
    echo ""
    echo -e "\033[1;32m$\033[0m $1"
    sleep 0.8
    eval "$1"
    sleep 1.2
}

clear
echo ""
echo -e "\033[1;36m╔══════════════════════════════════════════════════╗\033[0m"
echo -e "\033[1;36m║   k8s4claw — AI Agent Runtime on Kubernetes      ║\033[0m"
echo -e "\033[1;36m║   Demo: OpenClaw runtime in mock mode            ║\033[0m"
echo -e "\033[1;36m╚══════════════════════════════════════════════════╝\033[0m"
sleep 2

echo ""
echo -e "\033[1;33m▸ Step 1: Build the OpenClaw runtime image\033[0m"
sleep 1
type_cmd "docker build -t k8s4claw-openclaw:dev -f runtimes/openclaw/Dockerfile runtimes/openclaw/ 2>&1 | tail -3"

echo ""
echo -e "\033[1;33m▸ Step 2: Start runtime in mock mode (no API key needed)\033[0m"
sleep 1
type_cmd "docker run --rm -d --name demo-agent -p 18900:18900 -e OPENCLAW_MODE=mock k8s4claw-openclaw:dev"
sleep 1
type_cmd "docker logs demo-agent"

echo ""
echo -e "\033[1;33m▸ Step 3: Health check\033[0m"
sleep 1
type_cmd "docker run --rm --network host curlimages/curl:latest -s http://localhost:18900/health"
type_cmd "docker run --rm --network host curlimages/curl:latest -s http://localhost:18900/ready"

echo ""
echo -e "\033[1;33m▸ Step 4: Send messages via WebSocket (IPC bus protocol)\033[0m"
sleep 1

echo ""
echo -e "\033[1;32m$\033[0m # Saying hello..."
sleep 0.5
python3 -c "
import asyncio, json, websockets, textwrap

async def demo():
    async with websockets.connect('ws://localhost:18900') as ws:
        # Message 1: Hello
        msg = json.dumps({'id':'demo-1','type':'message','channel':'slack','timestamp':'2026-04-11T00:00:00Z','payload':{'text':'Hello!','user':'alice'}})
        await ws.send(msg)
        resp = json.loads(await asyncio.wait_for(ws.recv(), timeout=5))
        print(f\"  \033[1;34m← Agent:\033[0m {resp['payload']['text']}\")

        await asyncio.sleep(1.5)

        # Message 2: What can you do?
        print(f\"  \033[1;32m→ User:\033[0m What does k8s4claw do?\")
        await asyncio.sleep(0.5)
        msg2 = json.dumps({'id':'demo-2','type':'message','channel':'slack','timestamp':'2026-04-11T00:00:01Z','payload':{'text':'Tell me about k8s4claw','user':'alice'}})
        await ws.send(msg2)
        resp2 = json.loads(await asyncio.wait_for(ws.recv(), timeout=5))
        print(f\"  \033[1;34m← Agent:\033[0m\")
        for line in resp2['payload']['text'].split('\n'):
            print(f\"    {line}\")

        await asyncio.sleep(1.5)

        # Message 3: Status
        print(f\"  \033[1;32m→ User:\033[0m What's the status?\")
        await asyncio.sleep(0.5)
        msg3 = json.dumps({'id':'demo-3','type':'message','channel':'slack','timestamp':'2026-04-11T00:00:02Z','payload':{'text':'status','user':'bob'}})
        await ws.send(msg3)
        resp3 = json.loads(await asyncio.wait_for(ws.recv(), timeout=5))
        print(f\"  \033[1;34m← Agent:\033[0m\")
        for line in resp3['payload']['text'].split('\n'):
            print(f\"    {line}\")

asyncio.run(demo())
"
sleep 2

echo ""
echo -e "\033[1;33m▸ Step 5: Clean up\033[0m"
sleep 1
type_cmd "docker stop demo-agent"

echo ""
echo -e "\033[1;36m╔══════════════════════════════════════════════════╗\033[0m"
echo -e "\033[1;36m║   Demo complete!                                 ║\033[0m"
echo -e "\033[1;36m║                                                  ║\033[0m"
echo -e "\033[1;36m║   To use with real Claude API:                   ║\033[0m"
echo -e "\033[1;36m║   export ANTHROPIC_API_KEY=sk-ant-xxx            ║\033[0m"
echo -e "\033[1;36m║   docker run -e ANTHROPIC_API_KEY ...            ║\033[0m"
echo -e "\033[1;36m║                                                  ║\033[0m"
echo -e "\033[1;36m║   GitHub: github.com/Prismer-AI/k8s4claw         ║\033[0m"
echo -e "\033[1;36m╚══════════════════════════════════════════════════╝\033[0m"
sleep 3
