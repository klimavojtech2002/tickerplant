import type { Metadata } from "next";

import { themeScript } from "@/lib/theme";

import "./tokens.css";
import "./globals.css";
import "./legacy.css";

// System fonts only: a font download would make the build depend on the network.

export const metadata: Metadata = {
  title: "tickerplant",
  description: "Live order-book reconstruction — the pipeline's own dashboard.",
};

export default function RootLayout({
  children,
}: Readonly<{
  children: React.ReactNode;
}>) {
  return (
    // suppressHydrationWarning: the snippet below may set data-theme on this element before React hydrates it.
    <html lang="en" suppressHydrationWarning>
      <head>
        <script dangerouslySetInnerHTML={{ __html: themeScript }} />
      </head>
      <body>{children}</body>
    </html>
  );
}
