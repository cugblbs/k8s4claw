# k8s4claw Growth & Distribution Plan

Created: 2026-04-11

## Current Baseline

| Metric | Value |
|--------|-------|
| Stars | 6 |
| Forks | 4 |
| External Contributors | 3 |
| Commits | 140+ |
| Release | v0.1.0 |
| Container Images | 6 on GHCR |
| Demo | SVG + MP4 (Docker + K8s) |
| Codespaces | One-click kind cluster |
| Article Draft | `docs/articles/devto-introducing-k8s4claw.md` |

## Core Message

**One-liner:** One `kubectl apply` to deploy an AI agent on Kubernetes.

**Hook:** Your team has 5 AI bots scattered across Lambda, EC2, and someone's laptop. k8s4claw lets you manage them like microservices.

**CTA:** `github.com/Prismer-AI/k8s4claw` — Open in Codespaces, 3 minutes to try.

---

## Week 1: Content Launch

| Day | Action | Platform | Target Audience |
|-----|--------|----------|-----------------|
| Day 1 | Publish Dev.to article | Dev.to | Technical developers |
| Day 1 | Publish Chinese version | 掘金 (Juejin) | Chinese cloud-native community |
| Day 2 | Post Show HN | Hacker News | Broad tech community |
| Day 2 | Post to r/kubernetes + r/selfhosted | Reddit | K8s users, self-hosters |
| Day 3 | Twitter/X thread (5 tweets + demo GIF) | Twitter/X | AI + DevOps circles |
| Day 3 | LinkedIn long post | LinkedIn | Tech leads, architects |

### Show HN Template

```
Show HN: k8s4claw – Kubernetes operator for AI agent runtimes

We built a K8s operator that manages AI agent lifecycles with a single CRD.
One `kubectl apply` gives you: StatefulSet, IPC bus (WAL + DLQ), channel
sidecars (Slack/Discord), auto-updates with circuit breaker, and PVC persistence.

Demo (80s): https://github.com/Prismer-AI/k8s4claw/releases/download/v0.1.0/demo-k8s.mp4
Try it: Open in Codespaces (kind cluster, 3 min)

GitHub: https://github.com/Prismer-AI/k8s4claw
```

### Twitter Thread Template

```
1/ We built k8s4claw — a Kubernetes operator for AI agent runtimes.

One CRD. Any runtime. Production-ready from day one.

🧵 Thread: why we built it and how it works →

2/ The problem: 5 AI bots scattered across Lambda, EC2, and someone's laptop.
No unified management. No auto-updates. No observability.

k8s4claw wraps it all into a single `kubectl apply`.

3/ What you get per agent:
- StatefulSet + Service
- IPC Bus (WAL + DLQ + backpressure)
- Channel sidecars (Slack, Discord, Webhook)
- Auto-update with health checks + circuit breaker
- PVC persistence + CSI snapshots

4/ Try it in 3 minutes — no setup needed:
[Open in Codespaces badge link]

Or locally:
docker run -p 18900:18900 -e OPENCLAW_MODE=mock ghcr.io/prismer-ai/k8s4claw-openclaw:0.1.0

5/ Open source (Apache-2.0). Looking for contributors!

GitHub: github.com/Prismer-AI/k8s4claw
Demo: [demo-k8s.mp4 link]

Good first issues waiting for you 👋
```

### Reddit Post Template

```
Title: k8s4claw: Kubernetes operator for managing AI agent runtimes

We open-sourced k8s4claw — a K8s operator that manages heterogeneous AI agent
runtimes with a single CRD.

**What it does:**
- 5 built-in runtimes (OpenClaw, NanoClaw, ZeroClaw, PicoClaw, IronClaw)
- IPC Bus sidecar with WAL, DLQ, and backpressure
- Channel sidecars (Slack, Discord, Webhook)
- Auto-update with semver constraints and circuit breaker
- Helm chart with cert-manager integration

**Try it:** Open in GitHub Codespaces — kind cluster auto-provisioned, 3 min to first `kubectl get pods`.

**Demo video (80s):** [link to demo-k8s.mp4]

GitHub: https://github.com/Prismer-AI/k8s4claw

Looking for feedback and contributors!
```

---

## Week 2: Community Outreach

| Action | Where | Notes |
|--------|-------|-------|
| Post intro + demo link | CNCF Slack #kubernetes-operators | Be helpful, not spammy |
| Post intro + demo link | Kubernetes Slack #general | Same |
| Mention Hermes integration | NousResearch Discord | Reference Issue #10 |
| Submit to awesome-kubernetes | GitHub PR | Long-term SEO |
| Submit to awesome-selfhosted | GitHub PR | Self-hoster audience |
| Submit to awesome-operators | GitHub PR | K8s operator audience |
| Cross-post article | Medium | Wider reach |
| Cross-post article | 微信公众号 | Chinese tech community |

---

## Week 3: Interactive Experience

| Action | Notes |
|--------|-------|
| Create Killercoda tutorial | Browser-based, no local setup |
| Product Hunt launch | Non-technical audience |
| Record YouTube video (3-5 min) | Demo + architecture walkthrough |
| Submit to CNCF Landscape | AI/ML category |

---

## Monthly Recurring

| Action | Frequency |
|--------|-----------|
| Publish changelog / update post | Per release |
| Engage with issues and PRs | Daily |
| Share contributor spotlight | Bi-weekly |
| Answer K8s + AI questions on Reddit/SO | Weekly |
| Update awesome lists if new features | Per release |

---

## Tracking

| Metric | Current | Week 1 Target | Month 1 Target |
|--------|---------|---------------|----------------|
| GitHub Stars | 6 | 50 | 200 |
| Forks | 4 | 15 | 40 |
| Clones/week | ? | 100 | 300 |
| External PRs | 3 | 5 | 15 |
| Dev.to views | 0 | 2,000 | — |
| HN points | 0 | 30 | — |
| Codespace opens | 0 | 20 | 50 |

---

## Assets Ready

- [x] Dev.to article draft: `docs/articles/devto-introducing-k8s4claw.md`
- [x] Demo video (Docker): `docs/demo.mp4` (833KB, 31s)
- [x] Demo video (K8s): `docs/demo-k8s.mp4` (1.7MB, ~80s)
- [x] Demo SVG (animated): `docs/demo-k8s.svg`
- [x] Codespaces config: `.devcontainer/`
- [x] Quick start guide: `config/samples/quickstart/README.md`
- [x] Good first issues: #3, #4, #10
- [ ] Chinese article (translate from Dev.to draft)
- [ ] Twitter thread images/GIFs
- [ ] Killercoda tutorial
- [ ] YouTube video
