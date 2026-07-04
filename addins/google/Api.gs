/**
 * SeDoc REST client for the Google Workspace Add-on (ADR 0116).
 *
 * Same endpoints the Office add-ins use (they are the reference
 * clients — see addins/word/src/api.ts and addins/outlook/src/api.ts):
 *
 *   GET  /api/v1/workspaces                         — location picker
 *   GET  /api/v1/workspaces/{id}/folders            — location picker
 *   GET  /api/v1/search?q=                          — find documents
 *   GET  /api/v1/documents/{id}/versions            — newest first
 *   GET  /api/v1/storage/downloads/{doc}/{ver}      — presigned download URL
 *   POST /api/v1/storage/uploads/initiate → PUT → /complete
 *   POST /api/v1/documents/{id}/versions            — save (base_version_id 409s on stale base)
 *   POST /api/v1/integrations/m365/ingest-email     — file an email (provider-agnostic body)
 *   POST /api/v1/documents/{id}/share-links         — shareable reference
 *
 * A 401 clears the cached session and retries once with a fresh
 * exchange, mirroring the Office clients' behavior.
 */

/** Sentinel error message prefix for a 409 CreateVersion conflict. */
var CONFLICT_PREFIX = 'SEDOC_CONFLICT: ';

/**
 * Perform an authenticated JSON request against the SeDoc gateway.
 * Retries exactly once on 401 after re-exchanging the session.
 */
function sedocFetch_(method, path, body, retried) {
  var token = getSeDocSession_();
  var params = {
    method: method,
    headers: { Authorization: 'Bearer ' + token },
    muteHttpExceptions: true
  };
  if (body !== undefined && body !== null) {
    params.contentType = 'application/json';
    params.payload = JSON.stringify(body);
  }
  var resp = UrlFetchApp.fetch(CONFIG.API_BASE + path, params);
  var code = resp.getResponseCode();
  if (code === 401 && !retried) {
    clearSeDocSession_();
    return sedocFetch_(method, path, body, true);
  }
  if (code === 409) {
    throw new Error(CONFLICT_PREFIX + resp.getContentText());
  }
  if (code < 200 || code >= 300) {
    throw new Error(method + ' ' + path + ' failed (' + code + '): ' + resp.getContentText());
  }
  var text = resp.getContentText();
  return text ? JSON.parse(text) : {};
}

function isConflictError_(e) {
  return e && typeof e.message === 'string' && e.message.indexOf(CONFLICT_PREFIX) === 0;
}

// ---- location picker ------------------------------------------------

function listWorkspaces_() {
  var body = sedocFetch_('get', '/api/v1/workspaces');
  return body.workspaces || body.items || [];
}

function listFolders_(workspaceID) {
  var body = sedocFetch_('get', '/api/v1/workspaces/' + encodeURIComponent(workspaceID) + '/folders');
  if (Array.isArray(body)) return body;
  return body.folders || body.items || [];
}

// ---- documents -------------------------------------------------------

function searchDocuments_(q) {
  var body = sedocFetch_('get', '/api/v1/search?q=' + encodeURIComponent(q || '') + '&limit=' + CONFIG.SEARCH_LIMIT);
  return body.results || body.hits || [];
}

function listVersions_(documentID) {
  var body = sedocFetch_('get', '/api/v1/documents/' + encodeURIComponent(documentID) + '/versions');
  return body.versions || body.items || [];
}

function getDownloadURL_(documentID, versionID) {
  var body = sedocFetch_(
    'get',
    '/api/v1/storage/downloads/' + encodeURIComponent(documentID) + '/' + encodeURIComponent(versionID)
  );
  return body.url;
}

/**
 * Canonical in-app URL for a document (needs a SeDoc session to open).
 * The web app's document route is workspace-scoped, so both ids are
 * required — search hits and the filing flow both carry workspace_id.
 */
function documentURL_(workspaceID, documentID) {
  return CONFIG.API_BASE + '/workspaces/' + encodeURIComponent(workspaceID) +
    '/documents/' + encodeURIComponent(documentID);
}

function createShareLink_(documentID) {
  return sedocFetch_('post', '/api/v1/documents/' + encodeURIComponent(documentID) + '/share-links', {});
}

// ---- save (initiate → PUT → complete → version) ----------------------

/**
 * Upload raw bytes and return the content_blob_id. `bytes` is a byte[]
 * from Blob.getBytes().
 */
function uploadBlob_(bytes, filename, mimeType) {
  var init = sedocFetch_('post', '/api/v1/storage/uploads/initiate', {
    filename: filename,
    mime_type: mimeType,
    size_bytes: bytes.length
  });
  if (init.deduplicated && (init.content_blob_id || init.existing_blob_id)) {
    return init.content_blob_id || init.existing_blob_id;
  }
  // PUT straight to object storage — the presigned URL carries its own auth.
  var put = UrlFetchApp.fetch(init.presigned_put_url, {
    method: 'put',
    contentType: mimeType,
    payload: bytes,
    muteHttpExceptions: true
  });
  if (put.getResponseCode() < 200 || put.getResponseCode() >= 300) {
    throw new Error('presigned PUT failed (' + put.getResponseCode() + ')');
  }
  var done = sedocFetch_('post', '/api/v1/storage/uploads/' + encodeURIComponent(init.upload_id) + '/complete');
  var blobID = done.content_blob_id || init.content_blob_id;
  if (!blobID) {
    throw new Error('storage did not return a blob id');
  }
  return blobID;
}

/**
 * Create a new version. `baseVersionID` (optional) enables the server's
 * optimistic-concurrency check — a stale base raises a CONFLICT_PREFIX
 * error via the 409 branch in sedocFetch_.
 */
function createVersion_(documentID, contentBlobID, changeSummary, baseVersionID) {
  var body = {
    content_blob_id: contentBlobID,
    change_summary: changeSummary
  };
  if (baseVersionID) {
    body.base_version_id = baseVersionID;
  }
  return sedocFetch_('post', '/api/v1/documents/' + encodeURIComponent(documentID) + '/versions', body);
}

// ---- file an email ---------------------------------------------------

/**
 * File an email (+ attachments) into a workspace/folder. The endpoint is
 * named m365 for historical reasons (ADR 0112) but its request body is
 * provider-agnostic — subject/from/to/body/attachments — so the Gmail
 * add-on reuses it rather than growing a parallel Google-only route.
 */
function ingestEmail_(req) {
  return sedocFetch_('post', '/api/v1/integrations/m365/ingest-email', req);
}
