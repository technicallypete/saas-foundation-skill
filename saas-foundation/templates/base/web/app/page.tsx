import { runtimeConfig } from "@/lib/config";

export const dynamic = "force-dynamic";

export default function Home() {
  const { productName, platformBaseDomain } = runtimeConfig();
  return (
    <main className="mx-auto flex max-w-2xl flex-col gap-4 p-8">
      <h1 className="text-3xl font-semibold text-primary" data-testid="product-name">
        {productName}
      </h1>
      <p className="text-muted-foreground">
        Foundation skeleton served for <span data-testid="platform-domain">{platformBaseDomain}</span>.
      </p>
    </main>
  );
}
