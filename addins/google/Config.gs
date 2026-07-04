/**
 * SeDoc Google Workspace Add-on — configuration (ADR 0116).
 *
 * The SeDoc gateway base URL is read from a Script Property so the same
 * code deploys against dev / staging / prod without edits:
 *
 *   Project Settings → Script Properties → SEDOC_API_BASE = https://app.<tenant>.sedoc.example
 *
 * There is no build step for Apps Script, so this is the equivalent of
 * the Office add-ins' config.ts (window.__SEDOC_API_BASE__ / DefinePlugin).
 */
var CONFIG = {
  get API_BASE() {
    var v = PropertiesService.getScriptProperties().getProperty('SEDOC_API_BASE');
    return (v && v.replace(/\/+$/, '')) || 'https://app.vaultdms.example.com';
  },

  // .docx — what we export a Google Doc to before saving it as a SeDoc
  // version, matching the Word add-in's upload MIME.
  DOCX_MIME: 'application/vnd.openxmlformats-officedocument.wordprocessingml.document',

  // How many search hits to render as pickable rows in a card.
  SEARCH_LIMIT: 20
};
