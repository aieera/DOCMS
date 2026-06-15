import { mountFileExplorer } from "./widget";

// Iframe-embeddable fallback for ERPs that can't import the JS widget directly.
// The host embeds:
//   <iframe src=".../embed.html?customerRef=CUST-1&apiBase=https://erp.example.com"></iframe>
// and supplies the auth token over postMessage (NOT in the URL — query strings
// can leak into logs). The frame announces readiness; the host replies with:
//   iframe.contentWindow.postMessage({ type: "sedoc-files-token", token }, origin)
// Posting an empty token means "no bearer — rely on same-origin proxy/cookies".
const params = new URLSearchParams(location.search);
const customerRef = params.get("customerRef") ?? "";
const apiBase = params.get("apiBase") ?? "";

let latestToken = "";
let resolveToken: ((t: string) => void) | null = null;
const firstToken = new Promise<string>((res) => {
  resolveToken = res;
});
// Don't hang forever if the host never posts a token — after 10s assume
// proxy/cookie auth (empty bearer).
const timeout = setTimeout(() => resolveToken?.(""), 10_000);

window.addEventListener("message", (e) => {
  if (e.data && e.data.type === "sedoc-files-token" && typeof e.data.token === "string") {
    latestToken = e.data.token;
    clearTimeout(timeout);
    resolveToken?.(latestToken);
    resolveToken = null;
  }
});

// getAuthToken awaits the first host-supplied token, then returns the latest.
const getAuthToken = async (): Promise<string> => {
  if (latestToken) return latestToken;
  return firstToken;
};

const el = document.getElementById("root");
if (!el) {
  throw new Error("embed: #root not found");
}
if (!customerRef) {
  el.textContent = "Missing customerRef query parameter.";
} else {
  mountFileExplorer(el, { customerRef, apiBase, getAuthToken });
  // Tell the host we're ready to receive the token.
  window.parent?.postMessage({ type: "sedoc-files-ready" }, "*");
}
