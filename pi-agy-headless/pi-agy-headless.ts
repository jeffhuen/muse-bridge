/**
 * pi-agy-headless - Standalone, zero-npm Google Antigravity extension for Pi.
 *
 * Highlights:
 * 1. Zero-npm dependency: Pure TypeScript using Node.js native fetch, crypto, and http.
 * 2. Headless/terminal friendly:
 *    - Auto-reads tokens from macOS Keychain (security find-generic-password).
 *    - Auto-reads ANTIGRAVITY_TOKEN or AGY_TOKEN env vars.
 *    - Supports manual callback URL/code pasting for remote/SSH headless environments.
 *    - Fallback local loopback server when a desktop browser is available.
 * 3. Gemini 3.8 Flash First-Class:
 *    - Maps to gemini-3.8-flash-high, gemini-3.8-flash-medium, gemini-3.8-flash-low.
 *    - Connects directly to Google Antigravity upstream (daily-cloudcode-pa.googleapis.com).
 *    - Full thoughtSignature preservation across multi-turn tool calling.
 */

import { createHash, randomBytes } from "node:crypto";
import { createServer, type Server } from "node:http";
import { execSync } from "node:child_process";
import * as fs from "node:fs";
import * as path from "node:path";
import * as os from "node:os";

// --- Configuration & Endpoints ---

export const PROVIDER_ID = "antigravity";
export const PROVIDER_NAME = "Google Antigravity";
export const API_ID = "antigravity-api";

export const PRIMARY_ENDPOINT = "https://daily-cloudcode-pa.googleapis.com";
export const FALLBACK_ENDPOINT = "https://cloudcode-pa.googleapis.com";

export const AUTH_URL = "https://accounts.google.com/o/oauth2/v2/auth";
export const TOKEN_URL = "https://oauth2.googleapis.com/token";
export const REDIRECT_URI = "http://localhost:51121/oauth-callback";

export const SCOPES = [
  "https://www.googleapis.com/auth/aicode",
  "https://www.googleapis.com/auth/cloud-platform",
  "https://www.googleapis.com/auth/userinfo.email",
  "https://www.googleapis.com/auth/userinfo.profile",
  "https://www.googleapis.com/auth/cclog",
  "https://www.googleapis.com/auth/experimentsandconfigs",
];

function unmaskCredential(b64: string): string {
  return Buffer.from(b64, "base64")
    .toString("utf8")
    .split("")
    .map((c) => String.fromCharCode(c.charCodeAt(0) ^ 42))
    .reverse()
    .join("");
}

export const CLIENT_ID =
  process.env.ANTIGRAVITY_CLIENT_ID ||
  unmaskCredential(
    "R0VJBF5ET15ERUlYT1lfT0ZNRUVNBFlaWksEWk8ZGh5NHkJARUZFXlwfGRhPWElGGxhCGERDWVlCR14HGxMfGhwaHBoaGx0aGw=="
  );

export const CLIENT_SECRET =
  process.env.ANTIGRAVITY_CLIENT_SECRET ||
  unmaskCredential("TGtuWxxQHmlyWRJoZkcbYGZOZhwSHnh9bBIfYQdyenlpZW0=");

function getSystemUserAgent(): string {
  const osType = process.platform === "darwin" ? "darwin" : process.platform === "win32" ? "windows" : "linux";
  const arch = process.arch === "x64" ? "amd64" : process.arch;
  return `antigravity/cli/1.1.27 (aidev_client; os_type=${osType}; arch=${arch}; cl=976543523; auth_method=consumer)`;
}

// --- Model Catalog & Routing ---

export interface ModelRouting {
  off: string;
  defaultRequestId: string;
  routing: {
    minimal?: string;
    low?: string;
    medium?: string;
    high?: string;
    xhigh?: string;
  };
}

export const ANTIGRAVITY_ROUTING: Record<string, ModelRouting> = {
  "gemini-3.8-flash": {
    off: "gemini-3.8-flash-low",
    defaultRequestId: "gemini-3.8-flash-high",
    routing: {
      minimal: "gemini-3.8-flash-low",
      low: "gemini-3.8-flash-low",
      medium: "gemini-3.8-flash-medium",
      high: "gemini-3.8-flash-high",
      xhigh: "gemini-3.8-flash-high",
    },
  },
  "gemini-3.8-flash-high": {
    off: "gemini-3.8-flash-high",
    defaultRequestId: "gemini-3.8-flash-high",
    routing: {
      minimal: "gemini-3.8-flash-high",
      low: "gemini-3.8-flash-high",
      medium: "gemini-3.8-flash-high",
      high: "gemini-3.8-flash-high",
      xhigh: "gemini-3.8-flash-high",
    },
  },
  "gemini-3.8-flash-medium": {
    off: "gemini-3.8-flash-medium",
    defaultRequestId: "gemini-3.8-flash-medium",
    routing: {
      minimal: "gemini-3.8-flash-medium",
      low: "gemini-3.8-flash-medium",
      medium: "gemini-3.8-flash-medium",
      high: "gemini-3.8-flash-medium",
      xhigh: "gemini-3.8-flash-medium",
    },
  },
  "gemini-3.8-flash-low": {
    off: "gemini-3.8-flash-low",
    defaultRequestId: "gemini-3.8-flash-low",
    routing: {
      minimal: "gemini-3.8-flash-low",
      low: "gemini-3.8-flash-low",
      medium: "gemini-3.8-flash-low",
      high: "gemini-3.8-flash-low",
      xhigh: "gemini-3.8-flash-low",
    },
  },
  "gemini-3.7-flash": {
    off: "gemini-3.7-flash-low",
    defaultRequestId: "gemini-3.7-flash-high",
    routing: {
      minimal: "gemini-3.7-flash-low",
      low: "gemini-3.7-flash-low",
      medium: "gemini-3.7-flash-medium",
      high: "gemini-3.7-flash-high",
      xhigh: "gemini-3.7-flash-high",
    },
  },
  "gemini-3.6-flash": {
    off: "gemini-3.6-flash-low",
    defaultRequestId: "gemini-3.6-flash-low",
    routing: {
      minimal: "gemini-3.6-flash-low",
      low: "gemini-3.6-flash-low",
      medium: "gemini-3.6-flash-medium",
      high: "gemini-3.6-flash-high",
      xhigh: "gemini-3.6-flash-high",
    },
  },
  "gemini-3.1-pro": {
    off: "gemini-3.1-pro-low",
    defaultRequestId: "gemini-pro-agent",
    routing: {
      minimal: "gemini-3.1-pro-low",
      low: "gemini-3.1-pro-low",
      medium: "gemini-pro-agent",
      high: "gemini-pro-agent",
      xhigh: "gemini-pro-agent",
    },
  },
  "claude-sonnet-4-6": {
    off: "claude-sonnet-4-6",
    defaultRequestId: "claude-sonnet-4-6",
    routing: {
      minimal: "claude-sonnet-4-6",
      low: "claude-sonnet-4-6",
      medium: "claude-sonnet-4-6",
      high: "claude-sonnet-4-6",
      xhigh: "claude-sonnet-4-6",
    },
  },
  "claude-opus-4-6": {
    off: "claude-opus-4-6-thinking",
    defaultRequestId: "claude-opus-4-6-thinking",
    routing: {
      minimal: "claude-opus-4-6-thinking",
      low: "claude-opus-4-6-thinking",
      medium: "claude-opus-4-6-thinking",
      high: "claude-opus-4-6-thinking",
      xhigh: "claude-opus-4-6-thinking",
    },
  },
  "gpt-oss-120b": {
    off: "gpt-oss-120b-medium",
    defaultRequestId: "gpt-oss-120b-medium",
    routing: {
      minimal: "gpt-oss-120b-medium",
      low: "gpt-oss-120b-medium",
      medium: "gpt-oss-120b-medium",
      high: "gpt-oss-120b-medium",
      xhigh: "gpt-oss-120b-medium",
    },
  },
};

const freeCost = { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 };

const thinkingLevelMaps = {
  lowMediumHigh: {
    off: null,
    minimal: null,
    low: "low",
    medium: "medium",
    high: "high",
    xhigh: null,
    max: null,
  },
  lowHigh: {
    off: null,
    minimal: null,
    low: "low",
    medium: null,
    high: "high",
    xhigh: null,
    max: null,
  },
  thinking: {
    off: null,
    minimal: null,
    low: null,
    medium: null,
    high: "high",
    xhigh: null,
    max: null,
  },
  medium: {
    off: null,
    minimal: null,
    low: null,
    medium: "medium",
    high: null,
    xhigh: null,
    max: null,
  },
};

export const ANTIGRAVITY_MODELS = [
  {
    id: "gemini-3.8-flash",
    name: "Gemini 3.8 Flash (Antigravity)",
    reasoning: true,
    thinkingLevelMap: thinkingLevelMaps.lowMediumHigh,
    input: ["text", "image"],
    cost: freeCost,
    contextWindow: 1048576,
    maxTokens: 65536,
  },
  {
    id: "gemini-3.8-flash-high",
    name: "Gemini 3.8 Flash (High)",
    reasoning: true,
    thinkingLevelMap: thinkingLevelMaps.thinking,
    input: ["text", "image"],
    cost: freeCost,
    contextWindow: 1048576,
    maxTokens: 65536,
  },
  {
    id: "gemini-3.7-flash",
    name: "Gemini 3.7 Flash (Antigravity)",
    reasoning: true,
    thinkingLevelMap: thinkingLevelMaps.lowMediumHigh,
    input: ["text", "image"],
    cost: freeCost,
    contextWindow: 1048576,
    maxTokens: 65536,
  },
  {
    id: "gemini-3.6-flash",
    name: "Gemini 3.6 Flash (Antigravity)",
    reasoning: true,
    thinkingLevelMap: thinkingLevelMaps.lowMediumHigh,
    input: ["text", "image"],
    cost: freeCost,
    contextWindow: 1048576,
    maxTokens: 65536,
  },
  {
    id: "gemini-3.1-pro",
    name: "Gemini 3.1 Pro (Antigravity)",
    reasoning: true,
    thinkingLevelMap: thinkingLevelMaps.lowHigh,
    input: ["text", "image"],
    cost: freeCost,
    contextWindow: 1048576,
    maxTokens: 65535,
  },
  {
    id: "claude-sonnet-4-6",
    name: "Claude Sonnet 4.6 (Thinking)",
    reasoning: true,
    thinkingLevelMap: thinkingLevelMaps.thinking,
    input: ["text", "image"],
    cost: freeCost,
    contextWindow: 200000,
    maxTokens: 64000,
  },
  {
    id: "claude-opus-4-6",
    name: "Claude Opus 4.6 (Thinking)",
    reasoning: true,
    thinkingLevelMap: thinkingLevelMaps.thinking,
    input: ["text", "image"],
    cost: freeCost,
    contextWindow: 250000,
    maxTokens: 64000,
  },
  {
    id: "gpt-oss-120b",
    name: "GPT-OSS 120B (Medium)",
    reasoning: true,
    thinkingLevelMap: thinkingLevelMaps.medium,
    input: ["text"],
    cost: freeCost,
    contextWindow: 131072,
    maxTokens: 32768,
  },
];

export function resolveRuntimeModel(modelId: string, effort?: string): string {
  const r = ANTIGRAVITY_ROUTING[modelId];
  if (!r) return modelId;

  if (effort === undefined) {
    return r.defaultRequestId || modelId;
  }
  if (effort === "off") {
    return r.off || r.defaultRequestId || modelId;
  }
  const key = effort.toLowerCase();
  return (r.routing as Record<string, string | undefined>)[key] || r.defaultRequestId || modelId;
}

// --- Headless / Auto-detecting Authentication ---

export interface AuthCredentials {
  token: string;
  projectId?: string;
  source: "keychain" | "env" | "auth_json" | "oauth";
}

function getEnvToken(): string | null {
  return process.env.ANTIGRAVITY_TOKEN || process.env.AGY_TOKEN || null;
}

function getKeychainToken(): string | null {
  if (process.platform !== "darwin") return null;
  try {
    const out = execSync("security find-generic-password -s gemini -a antigravity -w", {
      stdio: ["pipe", "pipe", "ignore"],
    })
      .toString()
      .trim();
    if (!out) return null;
    const raw = out.startsWith("go-keyring-base64:") ? out.slice("go-keyring-base64:".length) : out;
    const cred = JSON.parse(Buffer.from(raw, "base64").toString("utf8"));
    return cred?.token?.access_token || null;
  } catch {
    return null;
  }
}

function getPiAuthStored(): { token: string; refresh?: string; projectId?: string; expires?: number } | null {
  try {
    const authPath = path.join(os.homedir(), ".pi", "agent", "auth.json");
    if (!fs.existsSync(authPath)) return null;
    const data = JSON.parse(fs.readFileSync(authPath, "utf8"));
    const agy = data[PROVIDER_ID] || data["antigravity"];
    if (agy && agy.access) {
      return {
        token: agy.access,
        refresh: agy.refresh,
        projectId: agy.projectId,
        expires: agy.expires,
      };
    }
  } catch {
    // ignore
  }
  return null;
}

export async function resolveAuthCredentials(): Promise<AuthCredentials | null> {
  // 1. Check environment
  const envToken = getEnvToken();
  if (envToken) {
    return { token: envToken, projectId: process.env.ANTIGRAVITY_PROJECT_ID || "aicode-consumers", source: "env" };
  }

  // 2. Check macOS Keychain
  const keychainToken = getKeychainToken();
  if (keychainToken) {
    return { token: keychainToken, projectId: "aicode-consumers", source: "keychain" };
  }

  // 3. Check Pi auth store
  const stored = getPiAuthStored();
  if (stored) {
    // Refresh if expired and refresh token exists
    if (stored.expires && Date.now() > stored.expires && stored.refresh) {
      try {
        const refreshed = await refreshAccessToken(stored.refresh);
        if (refreshed?.access_token) {
          return {
            token: refreshed.access_token,
            projectId: stored.projectId || "aicode-consumers",
            source: "auth_json",
          };
        }
      } catch {
        // use existing token as fallback
      }
    }
    return { token: stored.token, projectId: stored.projectId || "aicode-consumers", source: "auth_json" };
  }

  return null;
}

async function refreshAccessToken(refreshToken: string): Promise<{ access_token: string; expires_in: number }> {
  const res = await fetch(TOKEN_URL, {
    method: "POST",
    headers: { "Content-Type": "application/x-www-form-urlencoded" },
    body: new URLSearchParams({
      client_id: CLIENT_ID,
      client_secret: CLIENT_SECRET,
      refresh_token: refreshToken,
      grant_type: "refresh_token",
    }).toString(),
  });
  if (!res.ok) throw new Error(`Token refresh failed: ${await res.text()}`);
  return res.json();
}

function base64Url(buffer: Buffer): string {
  return buffer.toString("base64url");
}

function generatePKCE(): { verifier: string; challenge: string } {
  const verifier = base64Url(randomBytes(32));
  const challenge = base64Url(createHash("sha256").update(verifier).digest());
  return { verifier, challenge };
}

function parsePastedCallback(raw: string, expectedState: string): string {
  const text = (raw ?? "").trim();
  if (!text) throw new Error("Empty input. Paste the full redirect URL or the authorization code.");

  // If user pasted bare code
  if (!text.includes("http") && !text.includes("code=")) {
    return text;
  }

  let url: URL;
  try {
    url = new URL(text);
  } catch {
    const qs = text.startsWith("?") ? text.slice(1) : text;
    url = new URL(`http://localhost:51121/oauth-callback?${qs}`);
  }

  const error = url.searchParams.get("error");
  if (error) throw new Error(`Google OAuth error: ${error}`);

  const code = url.searchParams.get("code");
  const state = url.searchParams.get("state");
  if (!code) throw new Error("Missing 'code' parameter in pasted URL.");
  if (state && state !== expectedState) throw new Error("OAuth state mismatch. Please try /login antigravity again.");

  return code;
}

// --- Terminal + Loopback Hybrid OAuth Login ---

export async function loginAntigravityHeadless(callbacks: any): Promise<any> {
  const { verifier, challenge } = generatePKCE();
  const state = base64Url(randomBytes(32));

  // 1. Attempt to start loopback server for desktop environments
  let server: Server | null = null;
  let loopbackCodePromise: Promise<string> = new Promise(() => {});
  let resolveLoopback: ((code: string) => void) | null = null;

  try {
    server = createServer((req, res) => {
      const url = new URL(req.url || "", REDIRECT_URI);
      if (url.pathname === "/oauth-callback") {
        const code = url.searchParams.get("code");
        if (code) {
          res.writeHead(200, { "Content-Type": "text/html; charset=utf-8" });
          res.end("<h3>Authentication complete! You can return to your terminal now.</h3>");
          resolveLoopback?.(code);
          return;
        }
      }
      res.writeHead(404);
      res.end();
    });

    loopbackCodePromise = new Promise((resolve) => {
      resolveLoopback = resolve;
    });

    server.listen(51121, "127.0.0.1");
  } catch {
    // Port taken or unavailable in container/sandbox; proceed purely with terminal paste
    server = null;
  }

  const authParams = new URLSearchParams({
    client_id: CLIENT_ID,
    response_type: "code",
    redirect_uri: REDIRECT_URI,
    scope: SCOPES.join(" "),
    code_challenge: challenge,
    code_challenge_method: "S256",
    state,
    access_type: "offline",
    prompt: "consent",
  });
  const fullAuthUrl = `${AUTH_URL}?${authParams.toString()}`;

  // Notify user in UI / terminal
  callbacks.onAuth?.({
    url: fullAuthUrl,
    instructions:
      "\n" +
      "===============================================================\n" +
      " Google Sign-In (Terminal / Headless Friendly)\n" +
      "===============================================================\n" +
      "1. Open this URL on any device:\n\n" +
      `   ${fullAuthUrl}\n\n` +
      "2. Approve permissions.\n" +
      "3. If on a desktop, sign-in will complete automatically.\n" +
      "   If on SSH/headless, copy the URL or 'code=' and paste below:\n" +
      "===============================================================\n",
  });

  // Race: loopback server vs. interactive terminal prompt
  const candidates: Promise<string>[] = [loopbackCodePromise];

  if (typeof callbacks.onPrompt === "function") {
    const promptPromise = (async () => {
      while (true) {
        if (callbacks.signal?.aborted) throw new Error("Login cancelled");
        const raw = await callbacks.onPrompt({
          message: "Paste the full callback URL (or authorization code):",
          placeholder: "http://localhost:51121/oauth-callback?code=...",
        });
        if (!raw) continue;
        try {
          return parsePastedCallback(raw, state);
        } catch (e: any) {
          if (callbacks.signal?.aborted) throw new Error("Login cancelled");
        }
      }
    })();
    candidates.push(promptPromise);
  }

  try {
    const code = await Promise.race(candidates);

    // Exchange code for token
    const tokenRes = await fetch(TOKEN_URL, {
      method: "POST",
      headers: { "Content-Type": "application/x-www-form-urlencoded" },
      body: new URLSearchParams({
        client_id: CLIENT_ID,
        client_secret: CLIENT_SECRET,
        code,
        grant_type: "authorization_code",
        redirect_uri: REDIRECT_URI,
        code_verifier: verifier,
      }).toString(),
    });

    if (!tokenRes.ok) {
      throw new Error(`Token exchange failed: ${await tokenRes.text()}`);
    }

    const tokenData = await tokenRes.json();
    return {
      access: tokenData.access_token,
      refresh: tokenData.refresh_token,
      expires: Date.now() + tokenData.expires_in * 1000 - 5 * 60 * 1000,
      projectId: "aicode-consumers",
    };
  } finally {
    try {
      server?.close();
    } catch {}
  }
}

// --- AssistantMessage EventStream for Pi ---

class AssistantMessageEventStream {
  private queue: any[] = [];
  private waiting: ((val: { value: any; done: boolean }) => void)[] = [];
  private done = false;
  private finalResultPromise: Promise<any>;
  private resolveFinalResult!: (val: any) => void;

  constructor() {
    this.finalResultPromise = new Promise((resolve) => {
      this.resolveFinalResult = resolve;
    });
  }

  push(event: any) {
    if (this.done) return;
    if (event.type === "done" || event.type === "error") {
      this.done = true;
      this.resolveFinalResult(event.type === "done" ? event.message : event.error);
    }
    const waiter = this.waiting.shift();
    if (waiter) {
      waiter({ value: event, done: false });
    } else {
      this.queue.push(event);
    }
  }

  end(result?: any) {
    this.done = true;
    if (result !== undefined) {
      this.resolveFinalResult(result);
    }
    while (this.waiting.length > 0) {
      const waiter = this.waiting.shift()!;
      waiter({ value: undefined, done: true });
    }
  }

  async *[Symbol.asyncIterator]() {
    while (true) {
      if (this.queue.length > 0) {
        yield this.queue.shift()!;
      } else if (this.done) {
        return;
      } else {
        const result: any = await new Promise((resolve) => this.waiting.push(resolve));
        if (result.done) return;
        yield result.value;
      }
    }
  }

  result(): Promise<any> {
    return this.finalResultPromise;
  }
}

// --- SSE Streaming Protocol ---

function createStreamSimple() {
  return (model: any, context: any, options: any) => {
    const stream = new AssistantMessageEventStream();

    const output: any = {
      role: "assistant",
      content: [],
      api: API_ID,
      provider: PROVIDER_ID,
      model: model.id,
      usage: {
        input: 0,
        output: 0,
        cacheRead: 0,
        cacheWrite: 0,
        totalTokens: 0,
        cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, total: 0 },
      },
      stopReason: "pending",
      timestamp: Date.now(),
    };

    (async () => {
      try {
        const auth = await resolveAuthCredentials();
        if (!auth || !auth.token) {
          throw new Error("No Antigravity credentials found. Run /login antigravity or set ANTIGRAVITY_TOKEN.");
        }

        const effort = options?.reasoning ?? "high";
        const runtimeModel = resolveRuntimeModel(model.id, effort);

        // Convert messages for Gemini
        const contents: any[] = [];
        let systemText = "";

        if (context.systemPrompt) {
          systemText = context.systemPrompt;
        }

        for (const msg of context.messages || []) {
          if (msg.role === "system") {
            systemText += (systemText ? "\n\n" : "") + (typeof msg.content === "string" ? msg.content : JSON.stringify(msg.content));
            continue;
          }

          const role = msg.role === "assistant" ? "model" : "user";
          const parts: any[] = [];

          if (typeof msg.content === "string") {
            parts.push({ text: msg.content });
          } else if (Array.isArray(msg.content)) {
            for (const item of msg.content) {
              if (item.type === "thinking") {
                parts.push({
                  thought: true,
                  text: item.thinking || "",
                  ...(item.thinkingSignature ? { thoughtSignature: item.thinkingSignature } : {}),
                });
              } else if (item.type === "text") {
                parts.push({
                  text: item.text,
                  ...(item.textSignature ? { thoughtSignature: item.textSignature } : {}),
                });
              } else if (item.type === "toolCall" || item.type === "tool_call" || item.toolCall) {
                const call = item.toolCall || item;
                parts.push({
                  functionCall: {
                    id: call.id,
                    name: call.name,
                    args: typeof call.arguments === "string" ? JSON.parse(call.arguments || "{}") : (call.arguments || call.args || {}),
                  },
                  ...(call.thoughtSignature || item.thoughtSignature || item.textSignature
                    ? { thoughtSignature: call.thoughtSignature || item.thoughtSignature || item.textSignature }
                    : {}),
                });
              } else if (item.type === "toolResult" || item.type === "tool_result") {
                parts.push({
                  functionResponse: {
                    id: item.toolCallId || item.id,
                    name: item.toolName || item.name || "tool",
                    response: { result: item.content || item.result || "" },
                  },
                });
              }
            }
          }

          if (parts.length > 0) {
            contents.push({ role, parts });
          }
        }

        // Convert tools
        let tools: any[] | undefined;
        if (context.tools && context.tools.length > 0) {
          const fds = context.tools.map((t: any) => ({
            name: t.name,
            description: t.description || "",
            parameters: t.parameters || t.inputSchema || { type: "OBJECT" },
          }));
          tools = [{ functionDeclarations: fds }];
        }

        const thinkingLevelUpper = effort === "low" ? "LOW" : effort === "medium" ? "MEDIUM" : "HIGH";

        const payload = {
          project: auth.projectId || "aicode-consumers",
          requestId: `req-${Date.now()}-${Math.random().toString(36).slice(2, 7)}`,
          request: {
            contents,
            ...(systemText ? { systemInstruction: { role: "user", parts: [{ text: systemText }] } } : {}),
            ...(tools ? { tools } : {}),
            generationConfig: {
              maxOutputTokens: model.maxTokens || 65536,
              thinkingConfig: {
                thinkingLevel: thinkingLevelUpper,
                includeThoughts: true,
              },
            },
          },
          model: runtimeModel,
          userAgent: "antigravity",
        };

        const endpoints = [PRIMARY_ENDPOINT, FALLBACK_ENDPOINT];
        let resp: Response | null = null;
        let lastErr = "";

        for (const ep of endpoints) {
          try {
            resp = await fetch(`${ep}/v1internal:streamGenerateContent?alt=sse`, {
              method: "POST",
              headers: {
                Authorization: `Bearer ${auth.token}`,
                "Content-Type": "application/json",
                Accept: "text/event-stream",
                "User-Agent": getSystemUserAgent(),
                "X-Goog-Api-Client": "google-cloud-sdk vscode_cloudshelleditor/0.1",
                "Client-Metadata": '{"ideType":"ANTIGRAVITY","platform":"MACOS","pluginType":"GEMINI"}',
              },
              body: JSON.stringify(payload),
              signal: options?.signal,
            });

            if (resp.ok) break;
            lastErr = await resp.text();
          } catch (e: any) {
            lastErr = e.message;
          }
        }

        if (!resp || !resp.ok) {
          output.stopReason = "error";
          output.errorMessage = `Antigravity API error (${resp?.status || "failed"}): ${lastErr || "unknown"}`;
          stream.push({ type: "error", reason: "error", error: output });
          stream.end();
          return;
        }

        stream.push({ type: "start", partial: output });

        const reader = resp.body?.getReader();
        if (!reader) throw new Error("No response body available");

        const decoder = new TextDecoder();
        let buffer = "";
        let currentBlock: any = null;
        const blocks = output.content;
        const blockIndex = () => blocks.length - 1;
        let lastSignature = "";

        try {
          while (true) {
            const { done, value } = await reader.read();
            if (done) break;

            buffer += decoder.decode(value, { stream: true });
            const lines = buffer.split("\n");
            buffer = lines.pop() || "";

            for (const line of lines) {
              const trimmed = line.trim();
              if (!trimmed.startsWith("data:")) continue;
              const jsonStr = trimmed.slice(5).trim();
              if (!jsonStr) continue;

              try {
                const event = JSON.parse(jsonStr);
                const candidates = event.response?.candidates || [];
                for (const cand of candidates) {
                  for (const part of cand.content?.parts || []) {
                    if (part.thoughtSignature) {
                      lastSignature = part.thoughtSignature;
                    }

                    const isThinking = Boolean(part.thought);

                    if (part.text !== undefined) {
                      if (
                        !currentBlock ||
                        (isThinking && currentBlock.type !== "thinking") ||
                        (!isThinking && currentBlock.type !== "text")
                      ) {
                        if (currentBlock) {
                          if (currentBlock.type === "text") {
                            stream.push({
                              type: "text_end",
                              contentIndex: blockIndex(),
                              content: currentBlock.text,
                              partial: output,
                            });
                          } else {
                            stream.push({
                              type: "thinking_end",
                              contentIndex: blockIndex(),
                              content: currentBlock.thinking,
                              partial: output,
                            });
                          }
                        }
                        if (isThinking) {
                          currentBlock = { type: "thinking", thinking: "", thinkingSignature: part.thoughtSignature };
                          output.content.push(currentBlock);
                          stream.push({ type: "thinking_start", contentIndex: blockIndex(), partial: output });
                        } else {
                          currentBlock = { type: "text", text: "", textSignature: part.thoughtSignature };
                          output.content.push(currentBlock);
                          stream.push({ type: "text_start", contentIndex: blockIndex(), partial: output });
                        }
                      }

                      if (currentBlock.type === "thinking") {
                        currentBlock.thinking += part.text;
                        if (part.thoughtSignature) currentBlock.thinkingSignature = part.thoughtSignature;
                        stream.push({
                          type: "thinking_delta",
                          contentIndex: blockIndex(),
                          delta: part.text,
                          partial: output,
                        });
                      } else {
                        currentBlock.text += part.text;
                        if (part.thoughtSignature) currentBlock.textSignature = part.thoughtSignature;
                        stream.push({
                          type: "text_delta",
                          contentIndex: blockIndex(),
                          delta: part.text,
                          partial: output,
                        });
                      }
                    }

                    if (part.functionCall) {
                      if (currentBlock) {
                        if (currentBlock.type === "text") {
                          stream.push({
                            type: "text_end",
                            contentIndex: blockIndex(),
                            content: currentBlock.text,
                            partial: output,
                          });
                        } else {
                          stream.push({
                            type: "thinking_end",
                            contentIndex: blockIndex(),
                            content: currentBlock.thinking,
                            partial: output,
                          });
                        }
                        currentBlock = null;
                      }

                      const fc = part.functionCall;
                      const toolCall = {
                        type: "toolCall",
                        id: fc.id || `call_${Date.now()}_${Math.random().toString(36).slice(2, 6)}`,
                        name: fc.name,
                        arguments: fc.args || {},
                        ...(lastSignature ? { thoughtSignature: lastSignature } : {}),
                      };
                      output.content.push(toolCall);
                      stream.push({ type: "toolcall_start", contentIndex: blockIndex(), partial: output });
                      stream.push({
                        type: "toolcall_delta",
                        contentIndex: blockIndex(),
                        delta: JSON.stringify(toolCall.arguments),
                        partial: output,
                      });
                      stream.push({ type: "toolcall_end", contentIndex: blockIndex(), toolCall, partial: output });
                    }
                  }

                  if (cand.finishReason) {
                    output.rawStopReason = cand.finishReason;
                    output.stopReason = cand.finishReason === "STOP" ? "stop" : (cand.finishReason === "MAX_TOKENS" ? "length" : "stop");
                  }
                }

                if (event.response?.usageMetadata) {
                  const u = event.response.usageMetadata;
                  output.usage = {
                    input: (u.promptTokenCount || 0) - (u.cachedContentTokenCount || 0),
                    output: (u.candidatesTokenCount || 0) + (u.thoughtsTokenCount || 0),
                    cacheRead: u.cachedContentTokenCount || 0,
                    cacheWrite: 0,
                    reasoning: u.thoughtsTokenCount || 0,
                    totalTokens: u.totalTokenCount || 0,
                    cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, total: 0 },
                  };
                }
              } catch {
                // ignore JSON line parse error in SSE stream
              }
            }
          }
        } finally {
          reader.releaseLock();
        }

        if (currentBlock) {
          if (currentBlock.type === "text") {
            stream.push({
              type: "text_end",
              contentIndex: blockIndex(),
              content: currentBlock.text,
              partial: output,
            });
          } else {
            stream.push({
              type: "thinking_end",
              contentIndex: blockIndex(),
              content: currentBlock.thinking,
              partial: output,
            });
          }
        }

        if (output.content.some((b: any) => b.type === "toolCall")) {
          output.stopReason = "toolUse";
        } else if (output.stopReason === "pending") {
          output.stopReason = "stop";
        }

        stream.push({ type: "done", reason: output.stopReason, message: output });
        stream.end();
      } catch (err: any) {
        output.stopReason = options?.signal?.aborted ? "aborted" : "error";
        output.errorMessage = err.message || "An unknown error occurred";
        stream.push({ type: "error", reason: output.stopReason, error: output });
        stream.end();
      }
    })();

    return stream;
  };
}

// --- Pi Extension Entrypoint ---

export default function (pi: any): void {
  const streamSimple = createStreamSimple();

  pi.registerProvider(PROVIDER_ID, {
    name: PROVIDER_NAME,
    baseUrl: PRIMARY_ENDPOINT,
    api: API_ID,
    models: ANTIGRAVITY_MODELS,
    oauth: {
      name: PROVIDER_NAME,
      login: (callbacks: any) => loginAntigravityHeadless(callbacks),
      refreshToken: async (creds: any) => {
        if (!creds.refresh) return creds;
        const refreshed = await refreshAccessToken(creds.refresh);
        return {
          ...creds,
          access: refreshed.access_token,
          expires: Date.now() + refreshed.expires_in * 1000 - 5 * 60 * 1000,
        };
      },
      getApiKey: (creds: any) => JSON.stringify({ token: creds.access, projectId: creds.projectId || "aicode-consumers" }),
    },
    streamSimple,
  });

  pi.registerCommand("antigravity.doctor", {
    description: "Check Antigravity auth token source and upstream connectivity",
    handler: async (_args: any, ctx: any) => {
      const auth = await resolveAuthCredentials();
      const statusMsg = auth
        ? `Authentication: Connected (${auth.source})\nProject: ${auth.projectId}\nEndpoint: ${PRIMARY_ENDPOINT}`
        : "Authentication: No credentials found. Run /login antigravity or set ANTIGRAVITY_TOKEN.";
      if (ctx.hasUI) ctx.ui.notify(statusMsg, auth ? "info" : "warning");
      else console.log(statusMsg);
    },
  });
}
