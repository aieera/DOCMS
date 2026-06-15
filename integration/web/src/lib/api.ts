import { ApiError } from "./types";

// The explorer talks ONLY to the Files BFF (which holds the SeDoc key). All
// config — the BFF base URL and how the ERP user's auth is obtained — is injected
// via createApiClient so the widget can be embedded in a host (the ERP) with no
// hardcoded routing or auth assumptions. The standalone dev app builds a client
// the same way (empty base → same-origin via the Vite proxy; dev identity from
// localStorage).

export interface ApiConfig {
  // BFF base URL. "" = same origin (standalone dev proxies /files → BFF; in an
  // ERP embed behind the same origin this also works).
  apiBase: string;
  // Returns the ERP user's auth token. When provided, requests carry
  // `Authorization: Bearer <token>` (the host/ERP proxy maps it to the identity
  // the BFF trusts). When omitted, the dev identity headers (X-ERP-User /
  // X-ERP-Admin from localStorage) are sent — standalone dev only.
  getAuthToken?: () => string | Promise<string>;
}

export interface UploadResult {
  ingestion_item_id: string;
  status: string;
}

export interface ApiClient {
  get<T>(path: string): Promise<T>;
  postJSON<T>(path: string, body: unknown): Promise<T>;
  postEmpty(path: string): Promise<void>;
  upload(customerRef: string, file: File, onProgress?: (pct: number) => void): Promise<UploadResult>;
  download(documentId: string, filename: string): Promise<void>;
}

// devHeaders is the standalone-dev identity stand-in: the ERP's auth proxy stamps
// X-ERP-User / X-ERP-Admin upstream in production, so the browser sends neither;
// here a dev picks an identity (see DevIdentity in App.tsx), stored in localStorage.
export function devHeaders(): Record<string, string> {
  const user = localStorage.getItem("erpUser") || "";
  const admin = localStorage.getItem("erpAdmin") === "true";
  const h: Record<string, string> = {};
  if (user) h["X-ERP-User"] = user;
  if (admin) h["X-ERP-Admin"] = "true";
  return h;
}

// sha256Hex computes a file's SHA-256 with Web Crypto. Requires a secure context
// (https or localhost); returns "" if unavailable so the upload falls back to the
// BFF's server-side hashing (temp-file spool).
async function sha256Hex(file: File): Promise<string> {
  if (!globalThis.crypto?.subtle) return "";
  try {
    const digest = await crypto.subtle.digest("SHA-256", await file.arrayBuffer());
    return Array.from(new Uint8Array(digest))
      .map((b) => b.toString(16).padStart(2, "0"))
      .join("");
  } catch {
    return "";
  }
}

// createApiClient builds an ApiClient bound to a config. No module-level globals,
// so multiple clients (e.g. several embeds on one page) coexist.
export function createApiClient(config: ApiConfig): ApiClient {
  const base = config.apiBase.replace(/\/$/, "");

  async function authHeaders(): Promise<Record<string, string>> {
    if (config.getAuthToken) {
      const token = await config.getAuthToken();
      return token ? { Authorization: `Bearer ${token}` } : {};
    }
    return devHeaders();
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

  return {
    async get<T>(path: string): Promise<T> {
      const resp = await fetch(base + path, { headers: { ...(await authHeaders()) } });
      return parse<T>(resp);
    },

    async postJSON<T>(path: string, body: unknown): Promise<T> {
      const resp = await fetch(base + path, {
        method: "POST",
        headers: { "Content-Type": "application/json", ...(await authHeaders()) },
        body: JSON.stringify(body),
      });
      return parse<T>(resp);
    },

    async postEmpty(path: string): Promise<void> {
      const resp = await fetch(base + path, { method: "POST", headers: { ...(await authHeaders()) } });
      await parse<unknown>(resp);
    },

    // upload posts multipart form-data, reporting progress via XHR (fetch can't
    // stream upload progress). Sends sha256 + size FIRST so the BFF streams the
    // file straight to the presigned PUT (flat memory, no size cap).
    async upload(customerRef, file, onProgress) {
      const sha = await sha256Hex(file);
      const headers = await authHeaders();
      return new Promise((resolve, reject) => {
        const form = new FormData();
        if (sha) form.append("sha256", sha); // order matters: sha + size precede the file part
        form.append("size", String(file.size));
        form.append("file", file);
        const xhr = new XMLHttpRequest();
        xhr.open("POST", `${base}/files/customers/${encodeURIComponent(customerRef)}/upload`);
        for (const [k, v] of Object.entries(headers)) xhr.setRequestHeader(k, v);
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
    },

    // download streams document bytes through the BFF (sending the configured
    // identity a plain <a> can't) and triggers a browser download.
    async download(documentId, filename) {
      const resp = await fetch(`${base}/files/documents/${encodeURIComponent(documentId)}/download`, {
        headers: { ...(await authHeaders()) },
      });
      if (!resp.ok) throw new ApiError(resp.status, "download failed", "");
      const blob = await resp.blob();
      const url = URL.createObjectURL(blob);
      const a = document.createElement("a");
      a.href = url;
      a.download = filename || documentId;
      a.click();
      URL.revokeObjectURL(url);
    },
  };
}
