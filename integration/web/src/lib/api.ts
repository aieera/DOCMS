import { ApiError } from "./types";

// The BFF base. In dev, vite proxies /files → the BFF (see vite.config.ts), so
// the default empty base (same-origin) works both in dev and when embedded in
// the ERP behind the same origin.
const BASE = import.meta.env.VITE_BFF_BASE ?? "";

// Dev identity. In production the ERP's auth proxy stamps X-ERP-User /
// X-ERP-Admin upstream and the browser sends neither; here we let a dev pick an
// identity (see DevUser in App.tsx). Stored in localStorage.
export function devHeaders(): Record<string, string> {
  const user = localStorage.getItem("erpUser") || "";
  const admin = localStorage.getItem("erpAdmin") === "true";
  const h: Record<string, string> = {};
  if (user) h["X-ERP-User"] = user;
  if (admin) h["X-ERP-Admin"] = "true";
  return h;
}

async function parse<T>(resp: Response): Promise<T> {
  const text = await resp.text();
  if (!resp.ok) {
    let msg = resp.statusText;
    let corr = "";
    try {
      const j = JSON.parse(text);
      msg = j.error || j.message || msg;
      corr = j.correlation_id || "";
    } catch {
      /* non-JSON error body */
    }
    throw new ApiError(resp.status, msg, corr);
  }
  return (text ? JSON.parse(text) : {}) as T;
}

export async function apiGet<T>(path: string): Promise<T> {
  const resp = await fetch(BASE + path, { headers: { ...devHeaders() } });
  return parse<T>(resp);
}

export async function apiPostJSON<T>(path: string, body: unknown): Promise<T> {
  const resp = await fetch(BASE + path, {
    method: "POST",
    headers: { "Content-Type": "application/json", ...devHeaders() },
    body: JSON.stringify(body),
  });
  return parse<T>(resp);
}

export async function apiPostEmpty(path: string): Promise<void> {
  const resp = await fetch(BASE + path, { method: "POST", headers: { ...devHeaders() } });
  await parse<unknown>(resp);
}

// uploadFile posts multipart form-data to the BFF upload route, reporting
// progress via XHR (fetch can't stream upload progress).
export function uploadFile(
  customerRef: string,
  file: File,
  onProgress?: (pct: number) => void,
): Promise<{ ingestion_item_id: string; status: string }> {
  return new Promise((resolve, reject) => {
    const form = new FormData();
    form.append("file", file);
    const xhr = new XMLHttpRequest();
    xhr.open("POST", `${BASE}/files/customers/${encodeURIComponent(customerRef)}/upload`);
    for (const [k, v] of Object.entries(devHeaders())) xhr.setRequestHeader(k, v);
    xhr.upload.onprogress = (e) => {
      if (e.lengthComputable && onProgress) onProgress(Math.round((e.loaded / e.total) * 100));
    };
    xhr.onload = () => {
      if (xhr.status >= 200 && xhr.status < 300) {
        resolve(JSON.parse(xhr.responseText));
      } else {
        let msg = xhr.statusText;
        let corr = "";
        try {
          const j = JSON.parse(xhr.responseText);
          msg = j.error || msg;
          corr = j.correlation_id || "";
        } catch {
          /* ignore */
        }
        reject(new ApiError(xhr.status, msg, corr));
      }
    };
    xhr.onerror = () => reject(new ApiError(0, "network error", ""));
    xhr.send(form);
  });
}

// downloadDoc fetches the document bytes through the BFF (sending the dev
// identity headers a plain <a> can't) and triggers a browser download. In a
// production embed where the ERP proxy injects identity, a same-origin <a
// href={downloadURL()}> works too.
export async function downloadDoc(documentId: string, filename: string): Promise<void> {
  const resp = await fetch(`${BASE}/files/documents/${encodeURIComponent(documentId)}/download`, {
    headers: { ...devHeaders() },
  });
  if (!resp.ok) throw new ApiError(resp.status, "download failed", "");
  const blob = await resp.blob();
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url;
  a.download = filename || documentId;
  a.click();
  URL.revokeObjectURL(url);
}
