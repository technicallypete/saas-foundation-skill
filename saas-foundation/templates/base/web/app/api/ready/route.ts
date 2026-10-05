import { coreReady } from "@/lib/core-client";

// Readiness: web can serve only when core is ready. Reports state by name,
// never error detail.
export const dynamic = "force-dynamic";

export async function GET() {
  const ready = await coreReady();
  return Response.json(
    { status: ready ? "ready" : "unready", checks: { core: ready ? "ok" : "unavailable" } },
    { status: ready ? 200 : 503, headers: { "Cache-Control": "no-store" } },
  );
}
