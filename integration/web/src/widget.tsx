import { StrictMode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createApiClient } from "./lib/api";
import { ApiProvider } from "./lib/apiContext";
import { FileExplorer } from "./pages/FileExplorer";
import { Toaster } from "./components/toast";
import "./index.css";

// The embeddable entry. The ERP drops the file explorer into its customer record
// by calling mountFileExplorer(el, opts) — no routing, no localStorage identity,
// no hardcoded BFF URL. All config is injected:
//   - customerRef : the customer whose files to show (the ERP has it in context)
//   - apiBase     : the Files BFF base URL ("" = same origin)
//   - getAuthToken: returns the ERP user's token → sent as Authorization: Bearer
//
// Returns a handle so the host can tear the widget down (e.g. when navigating
// away from the customer record).
export interface MountOptions {
  customerRef: string;
  apiBase?: string;
  getAuthToken?: () => string | Promise<string>;
}

export interface ExplorerHandle {
  unmount(): void;
}

export function mountFileExplorer(el: HTMLElement, opts: MountOptions): ExplorerHandle {
  const client = createApiClient({ apiBase: opts.apiBase ?? "", getAuthToken: opts.getAuthToken });
  // Each mount gets its own QueryClient so multiple embeds on one page (or a
  // remount) never share or leak cache.
  const qc = new QueryClient({ defaultOptions: { queries: { retry: 1, refetchOnWindowFocus: false } } });
  const root: Root = createRoot(el);
  root.render(
    <StrictMode>
      <QueryClientProvider client={qc}>
        <ApiProvider client={client}>
          <Toaster />
          <div className="p-2">
            <FileExplorer customerRef={opts.customerRef} />
          </div>
        </ApiProvider>
      </QueryClientProvider>
    </StrictMode>,
  );
  return {
    unmount() {
      root.unmount();
      qc.clear();
    },
  };
}
