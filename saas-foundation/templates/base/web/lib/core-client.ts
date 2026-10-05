import "server-only";

import { runtimeConfig } from "./config";

// Server-only bridge to core. The browser never calls core or intelligence
// directly; this thin BFF forwards to core over the internal edge network.
// Authority, policy and domain rules live in core, never here.
export async function coreReady(timeoutMs = 2000): Promise<boolean> {
  const { coreInternalUrl } = runtimeConfig();
  try {
    const response = await fetch(new URL("/readyz", coreInternalUrl), {
      cache: "no-store",
      signal: AbortSignal.timeout(timeoutMs),
    });
    return response.ok;
  } catch {
    return false;
  }
}
