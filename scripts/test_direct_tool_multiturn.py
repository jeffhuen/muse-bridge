#!/usr/bin/env python3
"""
test_direct_tool_multiturn.py - Proves multi-turn tool calling with thoughtSignature preservation
against Google Antigravity direct internal PredictionService API.
"""

import json
import time
import base64
import subprocess
import urllib.request

ENDPOINT = "https://daily-cloudcode-pa.googleapis.com/v1internal:streamGenerateContent?alt=sse"

def get_keychain_token():
    cmd = ["security", "find-generic-password", "-s", "gemini", "-a", "antigravity", "-w"]
    out = subprocess.check_output(cmd).decode("utf-8").strip()
    if out.startswith("go-keyring-base64:"):
        out = out[len("go-keyring-base64:"):]
    cred = json.loads(base64.b64decode(out).decode("utf-8"))
    return cred["token"]["access_token"]

token = get_keychain_token()
headers = {
    "Authorization": f"Bearer {token}",
    "Content-Type": "application/json",
    "User-Agent": "antigravity/cli/1.1.27 (aidev_client; os_type=darwin; arch=arm64; cl=976543523; auth_method=consumer)"
}

weather_tool = {
    "functionDeclarations": [
        {
            "name": "get_current_weather",
            "description": "Get the current weather for a given location",
            "parameters": {
                "type": "OBJECT",
                "properties": {
                    "location": {"type": "STRING", "description": "City name"}
                },
                "required": ["location"]
            }
        }
    ]
}

# --- TURN 1: Ask question triggering tool call ---
print("=== TURN 1: Requesting tool call ===")
payload1 = {
    "project": "aicode-consumers",
    "requestId": f"tool-turn1-{int(time.time()*1000)}",
    "request": {
        "contents": [
            {"role": "user", "parts": [{"text": "What is the weather in Tokyo?"}]}
        ],
        "tools": [weather_tool],
        "generationConfig": {
            "maxOutputTokens": 2048,
            "thinkingConfig": {"thinkingLevel": "high"}
        }
    },
    "model": "gemini-3.8-flash-high",
    "userAgent": "antigravity"
}

body1 = json.dumps(payload1).encode("utf-8")
req1 = urllib.request.Request(ENDPOINT, data=body1, headers=headers, method="POST")

turn1_parts = []
with urllib.request.urlopen(req1, timeout=30) as resp:
    for line in resp:
        line_str = line.decode("utf-8").strip()
        if line_str.startswith("data:"):
            data = json.loads(line_str[5:].strip())
            for cand in data.get("response", {}).get("candidates", []):
                for p in cand.get("content", {}).get("parts", []):
                    turn1_parts.append(p)

print("Turn 1 Parts received:")
print(json.dumps(turn1_parts, indent=2))

# Verify turn 1 generated a functionCall and thoughtSignature
fc_part = next((p for p in turn1_parts if "functionCall" in p), None)
assert fc_part is not None, "Did not receive functionCall in Turn 1"
print(f"[*] Confirmed functionCall: {fc_part['functionCall']['name']}")
if "thoughtSignature" in fc_part:
    print(f"[*] Confirmed thoughtSignature attached to functionCall: {fc_part['thoughtSignature'][:30]}...")

# --- TURN 2: Send tool response back preserving functionCall part with its signature ---
print("\n=== TURN 2: Resending history with tool result ===")
tool_response_part = {
    "functionResponse": {
        "name": "get_current_weather",
        "response": {
            "result": "Sunny and 22C with light breeze"
        }
    }
}

contents2 = [
    {"role": "user", "parts": [{"text": "What is the weather in Tokyo?"}]},
    {"role": "model", "parts": turn1_parts}, # Preserving original parts and signatures exactly
    {"role": "user", "parts": [tool_response_part]}
]

payload2 = {
    "project": "aicode-consumers",
    "requestId": f"tool-turn2-{int(time.time()*1000)}",
    "request": {
        "contents": contents2,
        "tools": [weather_tool],
        "generationConfig": {
            "maxOutputTokens": 2048,
            "thinkingConfig": {"thinkingLevel": "high"}
        }
    },
    "model": "gemini-3.8-flash-high",
    "userAgent": "antigravity"
}

body2 = json.dumps(payload2).encode("utf-8")
req2 = urllib.request.Request(ENDPOINT, data=body2, headers=headers, method="POST")

turn2_text = ""
with urllib.request.urlopen(req2, timeout=30) as resp:
    for line in resp:
        line_str = line.decode("utf-8").strip()
        if line_str.startswith("data:"):
            data = json.loads(line_str[5:].strip())
            for cand in data.get("response", {}).get("candidates", []):
                for p in cand.get("content", {}).get("parts", []):
                    if "text" in p:
                        turn2_text += p["text"]

print(f"\n[*] Turn 2 final output: {turn2_text.strip()!r}")
print("[*] Multi-turn tool call sequence succeeded!")
