#!/usr/bin/env python3
"""Interactive WebSocket demo for OpenClaw runtime.

Usage: python3 scripts/ws-demo.py [url]
Default: ws://localhost:18900
"""
import asyncio
import json
import sys
import uuid
from datetime import datetime, timezone


async def main():
    try:
        import websockets
    except ImportError:
        print("Installing websockets...")
        import subprocess
        subprocess.check_call([sys.executable, "-m", "pip", "install", "-q", "websockets"])
        import websockets

    url = sys.argv[1] if len(sys.argv) > 1 else "ws://localhost:18900"

    print(f"\033[1;36m╔══════════════════════════════════════════╗\033[0m")
    print(f"\033[1;36m║  k8s4claw — OpenClaw WebSocket Demo      ║\033[0m")
    print(f"\033[1;36m║  Connected to {url:<27s}║\033[0m")
    print(f"\033[1;36m║  Type a message and press Enter           ║\033[0m")
    print(f"\033[1;36m║  Ctrl+C to quit                           ║\033[0m")
    print(f"\033[1;36m╚══════════════════════════════════════════╝\033[0m")
    print()

    async with websockets.connect(url) as ws:
        while True:
            try:
                text = input("\033[1;32m→ You:\033[0m ")
            except (EOFError, KeyboardInterrupt):
                print("\nBye!")
                break

            if not text.strip():
                continue

            msg = json.dumps({
                "id": str(uuid.uuid4()),
                "type": "message",
                "channel": "demo",
                "timestamp": datetime.now(timezone.utc).isoformat(),
                "payload": {"text": text, "user": "demo-user"},
            })

            await ws.send(msg)
            resp = json.loads(await asyncio.wait_for(ws.recv(), timeout=30))
            reply = resp.get("payload", {}).get("text", "(no response)")

            print(f"\033[1;34m← Agent:\033[0m {reply}")
            print()


if __name__ == "__main__":
    asyncio.run(main())
