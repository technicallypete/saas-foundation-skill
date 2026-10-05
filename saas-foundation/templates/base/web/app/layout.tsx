import type { Metadata } from "next";
import type { ReactNode } from "react";

import { runtimeConfig } from "@/lib/config";

import "./globals.css";

// Identity is read per request, never captured at build time.
export const dynamic = "force-dynamic";

export function generateMetadata(): Metadata {
  const { productName } = runtimeConfig();
  return { title: productName };
}

export default function RootLayout({ children }: { children: ReactNode }) {
  return (
    <html lang="en">
      <body className="min-h-screen bg-background text-foreground antialiased">{children}</body>
    </html>
  );
}
