"use client";

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { ThemeProvider } from "next-themes";
import { useState } from "react";
import { TelemetryProvider } from "@/lib/telemetry-context";

export function Providers({ children }: { children: React.ReactNode }) {
  const [queryClient] = useState(
    () =>
      new QueryClient({
        defaultOptions: {
          queries: { staleTime: 0, gcTime: 5 * 60 * 1000 },
        },
      })
  );

  return (
    <QueryClientProvider client={queryClient}>
      <ThemeProvider attribute="class" defaultTheme="dark" disableTransitionOnChange>
        <TelemetryProvider>
          {children}
        </TelemetryProvider>
      </ThemeProvider>
    </QueryClientProvider>
  );
}
