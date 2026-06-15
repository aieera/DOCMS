import { useState } from "react";
import {
  BrowserRouter,
  Link,
  Navigate,
  Route,
  Routes,
  useNavigate,
  useParams,
} from "react-router-dom";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { FileExplorer } from "./pages/FileExplorer";
import { ReviewQueue } from "./pages/ReviewQueue";
import { SyncDashboard } from "./pages/SyncDashboard";

const qc = new QueryClient({ defaultOptions: { queries: { retry: 1, refetchOnWindowFocus: false } } });

export default function App() {
  return (
    <QueryClientProvider client={qc}>
      <BrowserRouter>
        <div className="mx-auto max-w-6xl p-4">
          <TopBar />
          <main className="mt-4">
            <Routes>
              <Route path="/" element={<CustomerPicker />} />
              <Route path="/customers/:ref" element={<ExplorerHeader />} />
              <Route path="/review" element={<ReviewQueue />} />
              <Route path="/sync" element={<SyncDashboard />} />
              <Route path="*" element={<Navigate to="/" replace />} />
            </Routes>
          </main>
        </div>
      </BrowserRouter>
    </QueryClientProvider>
  );
}

function TopBar() {
  return (
    <header className="flex items-center justify-between border-b pb-3">
      <nav className="flex items-center gap-4 text-sm" aria-label="Primary">
        <span className="font-semibold text-brand">Customer Files</span>
        <Link to="/" className="text-gray-600 hover:text-brand">Explorer</Link>
        <Link to="/review" className="text-gray-600 hover:text-brand">Review queue</Link>
        <Link to="/sync" className="text-gray-600 hover:text-brand">Sync</Link>
      </nav>
      <DevIdentity />
    </header>
  );
}

// DevIdentity is a stand-in for the ERP's SSO: it sets the X-ERP-User /
// X-ERP-Admin headers the BFF expects. In production the ERP auth proxy injects
// these and this control disappears.
function DevIdentity() {
  const [user, setUser] = useState(localStorage.getItem("erpUser") || "");
  const [admin, setAdmin] = useState(localStorage.getItem("erpAdmin") === "true");
  return (
    <div className="flex items-center gap-2 text-xs text-gray-500">
      <span title="Dev only — the ERP injects this in production">dev user</span>
      <input
        aria-label="Dev ERP user id"
        value={user}
        placeholder="alice"
        onChange={(e) => { setUser(e.target.value); localStorage.setItem("erpUser", e.target.value); }}
        className="w-24 rounded border px-1.5 py-0.5"
      />
      <label className="flex items-center gap-1">
        <input
          type="checkbox"
          checked={admin}
          onChange={(e) => { setAdmin(e.target.checked); localStorage.setItem("erpAdmin", String(e.target.checked)); }}
        />
        admin
      </label>
    </div>
  );
}

function CustomerPicker() {
  const [ref, setRef] = useState("");
  const nav = useNavigate();
  return (
    <form
      onSubmit={(e) => { e.preventDefault(); if (ref) nav(`/customers/${encodeURIComponent(ref)}`); }}
      className="mx-auto mt-12 max-w-sm space-y-3 text-center"
    >
      <h1 className="text-lg font-semibold">Open a customer's files</h1>
      <p className="text-sm text-gray-500">Embedded in the ERP this opens with the customer in context.</p>
      <input
        aria-label="Customer reference"
        value={ref}
        onChange={(e) => setRef(e.target.value)}
        placeholder="CUST-1"
        className="w-full rounded border px-3 py-2"
      />
      <button className="w-full rounded bg-brand px-3 py-2 text-white">Open</button>
    </form>
  );
}

// ExplorerHeader shows the customer breadcrumb above the explorer.
function ExplorerHeader() {
  const { ref } = useParams();
  return (
    <div>
      <nav className="mb-3 text-sm text-gray-500" aria-label="Breadcrumb">
        <Link to="/" className="hover:text-brand">Customers</Link>
        <span className="mx-1">/</span>
        <span className="font-medium text-gray-700">{ref}</span>
      </nav>
      <FileExplorer />
    </div>
  );
}
