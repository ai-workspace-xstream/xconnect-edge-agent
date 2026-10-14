import { Container } from "@cloudflare/containers";
import { env as workerEnv } from "cloudflare:workers";

export class AgentRuntimeContainer extends Container {
  defaultPort = 8080;
  sleepAfter = "10m";
  envVars = {
    HEALTH_PORT: "8080",
    AGENT_ID: workerEnv.AGENT_ID ?? "",
    AGENT_CONTROLLER_URL: workerEnv.AGENT_CONTROLLER_URL ?? "",
    AGENT_API_TOKEN: workerEnv.AGENT_API_TOKEN ?? "",
    DOMAIN: workerEnv.DOMAIN ?? "",
    CONTROLLER_URL: workerEnv.CONTROLLER_URL ?? "",
    INTERNAL_SERVICE_TOKEN: workerEnv.INTERNAL_SERVICE_TOKEN ?? "",
  };
}

function json(payload, status = 200) {
  return new Response(JSON.stringify(payload), {
    status,
    headers: {
      "content-type": "application/json; charset=utf-8",
      "cache-control": "no-store",
    },
  });
}

export default {
  async fetch(request, env) {
    const url = new URL(request.url);
    const nodeID = url.searchParams.get("node") || "default";
    const container = env.AGENT_RUNTIME.getByName(nodeID);

    // ── Worker-level health (no container needed) ──
    if (url.pathname === "/healthz") {
      return json({
        ok: true,
        service: "hk-xhttp-svc-plus",
        worker: true,
      });
    }

    // ── Container health endpoints ──
    if (url.pathname === "/container/healthz") {
      return container.fetch("http://container/healthz");
    }

    if (url.pathname === "/container/readyz") {
      return container.fetch("http://container/readyz");
    }

    // ── Default: forward ALL other requests to container ──
    // This replaces Caddy's reverse-proxy role.
    // Xray XHTTP listens on /dev/shm/xray.sock inside the container;
    // the health server on :8080 is the container's defaultPort.
    // Requests to /split/* (XHTTP) will be handled by xray via the
    // internal routing in the container.
    return container.fetch(request);
  },
};
