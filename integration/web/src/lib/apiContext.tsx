import { createContext, useContext, type ReactNode } from "react";
import type { ApiClient } from "./api";

// The configured ApiClient is provided per-mount via context, so hooks never
// reach for a module global. Both the standalone app and the embeddable widget
// build a client and wrap their tree in <ApiProvider>.
const ApiContext = createContext<ApiClient | null>(null);

export function ApiProvider({ client, children }: { client: ApiClient; children: ReactNode }) {
  return <ApiContext.Provider value={client}>{children}</ApiContext.Provider>;
}

export function useApiClient(): ApiClient {
  const c = useContext(ApiContext);
  if (!c) throw new Error("useApiClient must be used within <ApiProvider>");
  return c;
}
