/**
 * Google Docs card — Open / Save / Insert-link against SeDoc (ADR 0116).
 * The Google equivalent of the Word add-in (ADR 0113):
 *
 *   OPEN   — search SeDoc → download the newest version's .docx →
 *            import it into Drive as a Google Doc (converted) → link
 *            the user to it. The SeDoc (document_id, version_id) pair
 *            is remembered per created Doc so a later Save is
 *            version-aware.
 *   SAVE   — export the current Google Doc as .docx via the Drive API →
 *            initiate/PUT/complete upload → create a new SeDoc version,
 *            passing the remembered version as base_version_id. A 409
 *            (someone saved a newer SeDoc version meanwhile) surfaces
 *            as a clear conflict message instead of clobbering.
 *   INSERT — search SeDoc → insert a hyperlink to the picked document
 *            at the cursor (internal canonical URL or a tokenised
 *            share link).
 *
 * The SeDoc↔Doc mapping lives in PropertiesService.getUserProperties()
 * (Google-server-side, per-user) — the analogue of the Word add-in
 * stamping CustomProperties into the .docx. Nothing SeDoc-related is
 * ever stored client-side (C2/§8.1).
 */

var DOC_MAP_PREFIX = 'sedoc_map_'; // + googleDocId → JSON {document_id, version_id, workspace_id}

// ---- homepage / file-scope plumbing ----------------------------------

/** Docs homepage trigger — main menu, or a grant-access prompt. */
function onDocsHomepage(e) {
  var docId = e && e.docs && e.docs.id;
  if (!docId) {
    // The add-on needs per-file authorization before it can export the
    // open document. CardService's file-scope flow grants drive.file
    // access to just this doc.
    var section = CardService.newCardSection()
      .addWidget(CardService.newTextParagraph().setText(
        'Grant SeDoc access to this document to save it into SeDoc, or use Open to pull a SeDoc document into Google Docs.'
      ))
      .addWidget(
        CardService.newTextButton()
          .setText('Grant access to this document')
          .setOnClickAction(CardService.newAction().setFunctionName('onDocsRequestFileScope'))
      )
      .addWidget(openSearchButton_())
      .addWidget(insertSearchButton_(''));
    return CardService.newCardBuilder()
      .setHeader(CardService.newCardHeader().setTitle('SeDoc'))
      .addSection(section)
      .build();
  }
  return buildDocsMenuCard_(docId, e.docs.title || 'Untitled document');
}

function onDocsRequestFileScope() {
  return CardService.newEditorFileScopeActionResponseBuilder()
    .requestFileScopeForActiveDocument()
    .build();
}

function onDocsFileScopeGranted(e) {
  return onDocsHomepage(e);
}

function buildDocsMenuCard_(docId, docTitle) {
  var mapping = readDocMapping_(docId);
  var saveSection = CardService.newCardSection().setHeader('Save "' + docTitle + '"');
  if (mapping) {
    saveSection.addWidget(CardService.newTextParagraph().setText(
      'This document is linked to a SeDoc document. Saving creates a new version (conflict-checked).'
    ));
  } else {
    saveSection.addWidget(CardService.newTextParagraph().setText(
      'Pick the SeDoc target on save. Documents opened via this add-on are linked automatically.'
    ));
  }
  saveSection.addWidget(
    CardService.newTextInput()
      .setFieldName('change_summary')
      .setTitle('Change summary (optional)')
  );
  saveSection.addWidget(
    CardService.newTextButton()
      .setText(mapping ? 'Save as new version' : 'Save to SeDoc…')
      .setTextButtonStyle(CardService.TextButtonStyle.FILLED)
      .setOnClickAction(
        CardService.newAction()
          .setFunctionName(mapping ? 'onDocsSave' : 'onDocsSearchTarget')
          .setParameters({ doc_id: docId, doc_title: docTitle })
      )
  );

  var openSection = CardService.newCardSection()
    .setHeader('Open from SeDoc')
    .addWidget(openSearchButton_());

  var insertSection = CardService.newCardSection()
    .setHeader('Insert link')
    .addWidget(insertSearchButton_(docId));

  return CardService.newCardBuilder()
    .setHeader(CardService.newCardHeader().setTitle('SeDoc'))
    .addSection(saveSection)
    .addSection(openSection)
    .addSection(insertSection)
    .build();
}

function openSearchButton_() {
  return CardService.newTextButton()
    .setText('Search SeDoc to open…')
    .setOnClickAction(
      CardService.newAction()
        .setFunctionName('onDocsSearchCard')
        .setParameters({ purpose: 'open' })
    );
}

function insertSearchButton_(docId) {
  return CardService.newTextButton()
    .setText('Search SeDoc to link…')
    .setOnClickAction(
      CardService.newAction()
        .setFunctionName('onDocsSearchCard')
        .setParameters({ purpose: 'insert', doc_id: docId })
    );
}

// ---- shared search card ----------------------------------------------

/** A query box + results list; each result routes by `purpose`. */
function onDocsSearchCard(e) {
  return pushCard_(buildSearchCard_(param_(e, 'purpose'), param_(e, 'doc_id'), param_(e, 'doc_title'), '', '', null));
}

function onDocsRunSearch(e) {
  var q = formValue_(e, 'q');
  var hits;
  try {
    hits = searchDocuments_(q);
  } catch (err) {
    return notify_(String(err.message || err));
  }
  return updateCard_(buildSearchCard_(param_(e, 'purpose'), param_(e, 'doc_id'), param_(e, 'doc_title'),
    param_(e, 'change_summary'), q, hits));
}

function buildSearchCard_(purpose, docId, docTitle, changeSummary, q, hits) {
  var titleByPurpose = {
    open: 'Open from SeDoc',
    insert: 'Insert a link',
    target: 'Pick the save target'
  };
  var section = CardService.newCardSection();
  section.addWidget(
    CardService.newTextInput().setFieldName('q').setTitle('Search SeDoc').setValue(q || '')
  );
  section.addWidget(
    CardService.newTextButton()
      .setText('Search')
      .setOnClickAction(
        CardService.newAction()
          .setFunctionName('onDocsRunSearch')
          .setParameters({
            purpose: purpose,
            doc_id: docId || '',
            doc_title: docTitle || '',
            change_summary: changeSummary || ''
          })
      )
  );
  if (hits) {
    if (hits.length === 0) {
      section.addWidget(CardService.newTextParagraph().setText('No documents match.'));
    }
    var pickFunction = { open: 'onDocsOpenPick', insert: 'onDocsInsertPick', target: 'onDocsTargetPick' }[purpose];
    hits.forEach(function (h) {
      section.addWidget(
        CardService.newDecoratedText()
          .setText(h.title || '(untitled)')
          .setBottomLabel(h.workspace_name || '')
          .setOnClickAction(
            CardService.newAction()
              .setFunctionName(pickFunction)
              .setParameters({
                purpose: purpose,
                doc_id: docId || '',
                doc_title: docTitle || '',
                change_summary: changeSummary || '',
                sedoc_document_id: h.document_id,
                sedoc_workspace_id: h.workspace_id || '',
                sedoc_title: h.title || 'SeDoc document'
              })
          )
      );
    });
  }
  return CardService.newCardBuilder()
    .setHeader(CardService.newCardHeader().setTitle(titleByPurpose[purpose] || 'SeDoc'))
    .addSection(section)
    .build();
}

// ---- OPEN --------------------------------------------------------------

/** Pull the picked SeDoc document into Drive as a converted Google Doc. */
function onDocsOpenPick(e) {
  var sedocID = param_(e, 'sedoc_document_id');
  var title = param_(e, 'sedoc_title');
  try {
    var versions = listVersions_(sedocID);
    if (!versions.length) {
      return notify_('That document has no versions yet.');
    }
    var url = getDownloadURL_(sedocID, versions[0].id);
    var dl = UrlFetchApp.fetch(url, { muteHttpExceptions: true });
    if (dl.getResponseCode() !== 200) {
      throw new Error('download failed (' + dl.getResponseCode() + ')');
    }
    var newDocId = driveImportAsGoogleDoc_(dl.getContent(), title);
    // Remember which SeDoc version this Doc came from → version-aware save.
    writeDocMapping_(newDocId, sedocID, versions[0].id, param_(e, 'sedoc_workspace_id'));
    var editUrl = 'https://docs.google.com/document/d/' + newDocId + '/edit';
    var card = CardService.newCardBuilder()
      .setHeader(CardService.newCardHeader().setTitle('Opened from SeDoc'))
      .addSection(
        CardService.newCardSection()
          .addWidget(CardService.newTextParagraph().setText(
            '"' + title + '" was imported as a Google Doc, linked for version-aware save-back.'
          ))
          .addWidget(
            CardService.newTextButton()
              .setText('Open the Google Doc')
              .setOpenLink(CardService.newOpenLink().setUrl(editUrl))
          )
      )
      .build();
    return pushCard_(card);
  } catch (err) {
    return notify_('Open failed: ' + String(err.message || err));
  }
}

/**
 * Import raw .docx bytes into Drive, converting to a Google Doc, via a
 * RESUMABLE upload session. Multipart upload is hard-capped at 5 MB
 * total request size, which routine DMS documents (embedded scans /
 * images) exceed; a resumable session takes the metadata first and the
 * bytes in a second request. drive.file scope suffices for both.
 */
function driveImportAsGoogleDoc_(docxBytes, title) {
  var start = UrlFetchApp.fetch(
    'https://www.googleapis.com/upload/drive/v3/files?uploadType=resumable&fields=id',
    {
      method: 'post',
      contentType: 'application/json; charset=UTF-8',
      headers: {
        Authorization: 'Bearer ' + ScriptApp.getOAuthToken(),
        'X-Upload-Content-Type': CONFIG.DOCX_MIME
      },
      payload: JSON.stringify({ name: title, mimeType: 'application/vnd.google-apps.document' }),
      muteHttpExceptions: true
    }
  );
  if (start.getResponseCode() < 200 || start.getResponseCode() >= 300) {
    throw new Error('Drive import failed (' + start.getResponseCode() + '): ' + start.getContentText());
  }
  var sessionURI = start.getHeaders().Location || start.getHeaders().location;
  if (!sessionURI) {
    throw new Error('Drive import failed: no resumable session URI');
  }
  var put = UrlFetchApp.fetch(sessionURI, {
    method: 'put',
    contentType: CONFIG.DOCX_MIME,
    payload: docxBytes,
    muteHttpExceptions: true
  });
  if (put.getResponseCode() < 200 || put.getResponseCode() >= 300) {
    throw new Error('Drive import failed (' + put.getResponseCode() + '): ' + put.getContentText());
  }
  return JSON.parse(put.getContentText()).id;
}

// ---- SAVE --------------------------------------------------------------

/** No mapping yet: run the target-picking search, then save.
 * The menu card's change_summary input is captured HERE (this event
 * still carries the menu card's form inputs) and threaded through the
 * search card as an action parameter — the later pick event only has
 * the search card's inputs. */
function onDocsSearchTarget(e) {
  return pushCard_(buildSearchCard_('target', param_(e, 'doc_id'), param_(e, 'doc_title'),
    formValue_(e, 'change_summary'), '', null));
}

function onDocsTargetPick(e) {
  // Adopt the picked target with no base version — the save flow then
  // uses the target's current head as the base (same policy as the
  // Word add-in when the user picks a doc they didn't open).
  writeDocMapping_(param_(e, 'doc_id'), param_(e, 'sedoc_document_id'), '', param_(e, 'sedoc_workspace_id'));
  return onDocsSave(e);
}

function onDocsSave(e) {
  var docId = param_(e, 'doc_id');
  var mapping = readDocMapping_(docId);
  if (!mapping) {
    return notify_('Pick a SeDoc target first.');
  }
  try {
    var docxBytes = driveExportAsDocx_(docId);
    var filename = (param_(e, 'doc_title') || 'google-doc') + '.docx';
    var blobID = uploadBlob_(docxBytes, filename, CONFIG.DOCX_MIME);
    var base = mapping.version_id;
    if (!base) {
      var versions = listVersions_(mapping.document_id);
      base = versions.length ? versions[0].id : '';
    }
    var summary = formValue_(e, 'change_summary') || param_(e, 'change_summary') || 'Saved from Google Docs add-on';
    var created = createVersion_(mapping.document_id, blobID, summary, base || undefined);
    // Advance the base so the next save from this Doc doesn't
    // false-conflict against the version we just wrote.
    writeDocMapping_(docId, mapping.document_id, (created && created.id) || '', mapping.workspace_id);
    return pushCard_(successCard_(
      'New version saved',
      'SeDoc will OCR + classify the new bytes in the background.',
      mapping.workspace_id,
      mapping.document_id
    ));
  } catch (err) {
    if (isConflictError_(err)) {
      return notify_('A newer version was saved to SeDoc since this Doc was opened. Open the latest version, reapply your edits, then save again.');
    }
    return notify_('Save failed: ' + String(err.message || err));
  }
}

/** Export the Google Doc as .docx bytes via the Drive API. */
function driveExportAsDocx_(docId) {
  var resp = UrlFetchApp.fetch(
    'https://www.googleapis.com/drive/v3/files/' + encodeURIComponent(docId) +
      '/export?mimeType=' + encodeURIComponent(CONFIG.DOCX_MIME),
    {
      headers: { Authorization: 'Bearer ' + ScriptApp.getOAuthToken() },
      muteHttpExceptions: true
    }
  );
  if (resp.getResponseCode() !== 200) {
    throw new Error('Drive export failed (' + resp.getResponseCode() + '): ' + resp.getContentText());
  }
  return resp.getContent();
}

// ---- INSERT LINK ---------------------------------------------------------

function onDocsInsertPick(e) {
  var sedocID = param_(e, 'sedoc_document_id');
  var workspaceID = param_(e, 'sedoc_workspace_id');
  var title = param_(e, 'sedoc_title');
  var section = CardService.newCardSection()
    .addWidget(CardService.newTextParagraph().setText('Insert a link to "' + title + '" at your cursor.'))
    .addWidget(
      CardService.newTextButton()
        .setText('Insert internal link')
        .setOnClickAction(
          CardService.newAction()
            .setFunctionName('onDocsInsertLink')
            .setParameters({ sedoc_document_id: sedocID, sedoc_workspace_id: workspaceID, sedoc_title: title, shareable: 'false' })
        )
    )
    .addWidget(
      CardService.newTextButton()
        .setText('Insert shareable link (anyone with the link can view)')
        .setOnClickAction(
          CardService.newAction()
            .setFunctionName('onDocsInsertLink')
            .setParameters({ sedoc_document_id: sedocID, sedoc_workspace_id: workspaceID, sedoc_title: title, shareable: 'true' })
        )
    );
  return pushCard_(
    CardService.newCardBuilder()
      .setHeader(CardService.newCardHeader().setTitle('Insert link'))
      .addSection(section)
      .build()
  );
}

function onDocsInsertLink(e) {
  var sedocID = param_(e, 'sedoc_document_id');
  var title = param_(e, 'sedoc_title');
  var shareable = param_(e, 'shareable') === 'true';
  try {
    var url;
    if (shareable) {
      url = createShareLink_(sedocID).url;
    } else {
      var workspaceID = param_(e, 'sedoc_workspace_id');
      if (!workspaceID) {
        return notify_('This search result has no workspace — use the shareable link instead.');
      }
      url = documentURL_(workspaceID, sedocID);
    }
    var doc = DocumentApp.getActiveDocument();
    if (!doc) {
      return notify_('Open a Google Doc to insert a link.');
    }
    var cursor = doc.getCursor();
    if (cursor) {
      var inserted = cursor.insertText(title);
      if (inserted) {
        inserted.setLinkUrl(url);
      } else {
        return notify_('Could not insert at the cursor — click into the document body first.');
      }
    } else {
      // No cursor (e.g. a selection is active): append at the end.
      doc.getBody().appendParagraph(title).editAsText().setLinkUrl(url);
    }
    return notify_('Link to "' + title + '" inserted.');
  } catch (err) {
    return notify_('Insert failed: ' + String(err.message || err));
  }
}

// ---- SeDoc↔Doc mapping (user-scoped, server-side) ----------------------

function readDocMapping_(docId) {
  if (!docId) return null;
  var raw = PropertiesService.getUserProperties().getProperty(DOC_MAP_PREFIX + docId);
  if (!raw) return null;
  try {
    var m = JSON.parse(raw);
    return m && m.document_id ? m : null;
  } catch (err) {
    return null;
  }
}

function writeDocMapping_(docId, sedocDocumentID, sedocVersionID, sedocWorkspaceID) {
  if (!docId) return;
  PropertiesService.getUserProperties().setProperty(
    DOC_MAP_PREFIX + docId,
    JSON.stringify({
      document_id: sedocDocumentID,
      version_id: sedocVersionID || '',
      workspace_id: sedocWorkspaceID || ''
    })
  );
}
