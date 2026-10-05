// Liveness: the web process is serving. No dependencies.
export const dynamic = "force-dynamic";

export function GET() {
  return Response.json({ status: "alive" }, { headers: { "Cache-Control": "no-store" } });
}
