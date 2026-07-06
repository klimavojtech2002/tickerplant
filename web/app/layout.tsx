import type { Metadata } from "next";
import "./globals.css";

// System fonts only: a Google Fonts fetch would make the build depend on the
// network, and the console aesthetic wants the platform's monospace anyway.

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
    <html lang="en">
      <body>{children}</body>
    </html>
  );
}
