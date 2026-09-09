#!/usr/bin/env python3
"""
test_direct_api.py - Reproducible test script for Google Antigravity direct internal PredictionService API.
Reads Bearer token directly from local macOS Keychain (Service: gemini, Account: antigravity).
Zero external API keys required.
"""

import sys
import json
import time
import base64
import subprocess
import urllib.request
import urllib.error

ENDPOINT = "https://daily-cloudcode-pa.googleapis.com/v1internal:streamGenerateContent?alt=sse"

def get_keychain_token():
    cmd = ["security", "find-generic-password", "-s", "gemini", "-a", "antigravity", "-w"]
    out = subprocess.check_output(cmd).decode("utf-8").strip()
    if out.startswith("go-keyring-base64:"):
        out = out[len("go-keyring-base64:"):]
    cred = json.loads(base64.b64decode(out).decode("utf-8"))
    return cred["token"]["access_token"]

def run_test(prompt="Say hello in 3 words", model="gemini-3.8-flash-high", thinking_level="high"):
    token = get_keychain_token()
    print(f"[*] Retrieved Keychain token (prefix: {token[:12]}...)")
    
    payload = {
        "project": "aicode-consumers",
        "requestId": f"verify-{int(time.time()*1000)}",
        "request": {
            "contents": [
                {
                    "role": "user",
                    "parts": [{"text": prompt}]
                }
            ],
            "generationConfig": {
                "maxOutputTokens": 2048,
                "thinkingConfig": {
                    "thinkingLevel": thinking_level
                }
            }
        },
        "model": model,
        "userAgent": "antigravity"
    }
    
    body = json.dumps(payload).encode("utf-8")
    headers = {
        "Authorization": f"Bearer {token}",
        "Content-Type": "application/json",
        "User-Agent": "antigravity/cli/1.1.27 (aidev_client; os_type=darwin; arch=arm64; cl=976543523; auth_method=consumer)",
        "Content-Length": str(len(body))
    }
    
    print(f"[*] POST {ENDPOINT}")
    print(f"[*] Model: {model}, ThinkingLevel: {thinking_level}")
    print(f"[*] Request:\n{json.dumps(payload, indent=2)}\n")
    
    req = urllib.request.Request(ENDPOINT, data=body, headers=headers, method="POST")
    t0 = time.time()
    first_byte_time = None
    accumulated_text = ""
    signatures = []
    
    with urllib.request.urlopen(req, timeout=30) as resp:
        print(f"[*] HTTP Response Status: {resp.status}")
        for raw_line in resp:
            line = raw_line.decode("utf-8").strip()
            if not line:
                continue
            if line.startswith("data:"):
                if first_byte_time is None:
                    first_byte_time = time.time() - t0
                event_data = json.loads(line[5:].strip())
                candidates = event_data.get("response", {}).get("candidates", [])
                for cand in candidates:
                    for part in cand.get("content", {}).get("parts", []):
                        if "text" in part and part["text"]:
                            accumulated_text += part["text"]
                        if "thoughtSignature" in part and part["thoughtSignature"]:
                            signatures.append(part["thoughtSignature"][:30] + "...")
                usage = event_data.get("response", {}).get("usageMetadata")
                if usage:
                    last_usage = usage
    
    total_time = time.time() - t0
    print(f"\n[*] Results:")
    print(f"    TTFB: {first_byte_time:.2f}s | Total: {total_time:.2f}s")
    print(f"    Text: {accumulated_text.strip()!r}")
    print(f"    Signatures captured: {len(signatures)} (sample: {signatures[:2]})")
    print(f"    Usage metadata: {last_usage}")
    return True

if __name__ == "__main__":
    prompt = sys.argv[1] if len(sys.argv) > 1 else "Explain what makes Go channels unique in one concise sentence."
    run_test(prompt=prompt)
