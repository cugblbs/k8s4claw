# Quick Start: Deploy a Claude-powered Slack Bot

Deploy an AI agent on Kubernetes in 5 minutes.

## Prerequisites

- Kubernetes cluster (1.28+)
- kubectl configured
- [Anthropic API key](https://console.anthropic.com/)
- [Slack App](https://api.slack.com/apps) with Socket Mode enabled

## Step 1: Install CRDs

```bash
make install
```

## Step 2: Run the operator

```bash
# Option A: Run locally (for development)
make run

# Option B: Deploy to cluster
make docker-build-all
make deploy
```

## Step 3: Create secrets

```bash
# Edit the files with your actual keys first!
kubectl apply -f config/samples/quickstart/secret-llm.yaml
kubectl apply -f config/samples/quickstart/secret-slack.yaml
```

## Step 4: Deploy the agent

```bash
# Create the Slack channel
kubectl apply -f config/samples/quickstart/channel-slack.yaml

# Deploy the agent
kubectl apply -f config/samples/quickstart/agent.yaml
```

## Step 5: Verify

```bash
kubectl get claws
# NAME              RUNTIME    STATUS   AGE
# my-first-agent    openclaw   Ready    30s

kubectl get pods
# NAME                  READY   STATUS    AGE
# my-first-agent-0      4/4     Running   30s
```

Go to Slack and mention your bot — it will respond using Claude!

## What's in the pod?

```
my-first-agent-0
├── claw-init          (init container: config merge)
├── ipc-bus            (native sidecar: message routing)
├── channel-slack      (native sidecar: Slack bridge)
└── runtime            (OpenClaw: Claude API gateway)
```

## Local development (no Kubernetes)

```bash
# Test the runtime standalone
export ANTHROPIC_API_KEY=sk-ant-xxx
docker compose up

# In another terminal, test with wscat:
npx wscat -c ws://localhost:18900
> {"id":"test","type":"message","channel":"test","timestamp":"2026-01-01T00:00:00Z","payload":{"text":"Hello!"}}
```

## Cleanup

```bash
kubectl delete -f config/samples/quickstart/
```
