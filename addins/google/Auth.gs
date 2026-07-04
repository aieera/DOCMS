/**
 * SeDoc session exchange for the Google Workspace Add-on (ADR 0116).
 *
 * Mirrors the Office add-ins' auth.ts: obtain a host identity token,
 * exchange it server-side for a SeDoc session, then send that session
 * as a Bearer on every /api/v1 call.
 *
 *   1. ScriptApp.getIdentityToken() → a Google OpenID Connect ID token
 *      for the signed-in user (requires the `openid` + userinfo.email
 *      scopes declared in appsscript.json).
 *   2. POST it to /api/v1/auth/google/exchange. The auth service
 *      verifies it against Google's JWKS, checks the hosted-domain
 *      allow-list, and returns a `vdms_session_token`.
 *
 * C2 / §8.1 — the SeDoc session token MUST NOT live in browser storage.
 * Apps Script has no browser storage anyway; CacheService.getUserCache()
 * is Google-server-side, per-user, and short-lived, which is the
 * server-side analogue of the Office add-ins holding the token in an
 * in-memory module variable. It is never returned to the card client.
 */

var SESSION_CACHE_KEY = 'sedoc_session_token';
var SESSION_CACHE_TTL_SECONDS = 1500; // 25 min — under the server session TTL.

/**
 * Returns a valid SeDoc session token, exchanging a fresh Google
 * identity token when the cache is cold. Throws on any failure so the
 * calling action surfaces it as a card notification.
 */
function getSeDocSession_() {
  var cache = CacheService.getUserCache();
  var cached = cache.get(SESSION_CACHE_KEY);
  if (cached) {
    return cached;
  }
  var idToken = ScriptApp.getIdentityToken();
  if (!idToken) {
    throw new Error('Could not obtain a Google identity token — check that the add-on has the "openid" scope.');
  }
  var resp = UrlFetchApp.fetch(CONFIG.API_BASE + '/api/v1/auth/google/exchange', {
    method: 'post',
    contentType: 'application/json',
    payload: JSON.stringify({ google_id_token: idToken }),
    muteHttpExceptions: true
  });
  var code = resp.getResponseCode();
  if (code === 404) {
    throw new Error('No SeDoc user matches your Google account. Ask your SeDoc admin to invite you.');
  }
  if (code === 409) {
    // Multi-tenant email collision. The card UI here does not (yet)
    // offer a tenant chooser, so surface a clear message; a deployer
    // scoping the add-on to a single tenant will never hit this.
    throw new Error('Your email exists in multiple SeDoc tenants; a single-tenant deployment is required for the add-on.');
  }
  if (code !== 200) {
    throw new Error('SeDoc sign-in failed (' + code + '): ' + resp.getContentText());
  }
  var body = JSON.parse(resp.getContentText());
  var token = body.vdms_session_token;
  if (!token) {
    throw new Error('SeDoc sign-in returned no session token.');
  }
  cache.put(SESSION_CACHE_KEY, token, SESSION_CACHE_TTL_SECONDS);
  return token;
}

/** Drop the cached session so the next call re-exchanges (used on 401). */
function clearSeDocSession_() {
  CacheService.getUserCache().remove(SESSION_CACHE_KEY);
}
