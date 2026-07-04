/**
 * Gmail contextual card — "file this email into SeDoc" (ADR 0116).
 *
 * Equivalent of the Outlook add-in's task pane (ADR 0112): pick a
 * workspace + folder, choose what to file (message / attachments /
 * both), Save. The message body + attachments are read via GmailApp
 * using the contextual access token Gmail hands the trigger event, and
 * POSTed to the same ingest endpoint the Outlook add-in uses.
 */

/** Contextual trigger — builds the filing card for the open message. */
function onGmailMessage(e) {
  var messageId = e.gmail && e.gmail.messageId;
  if (!messageId) {
    return errorCard_('Open a message to file it into SeDoc.');
  }
  return buildGmailCard_(messageId, '', null);
}

/** Re-render when the workspace pick changes (repopulates folders). */
function onGmailWorkspaceChange(e) {
  var messageId = param_(e, 'message_id');
  var workspaceID = formValue_(e, 'workspace_id');
  return updateCard_(buildGmailCard_(messageId, workspaceID, e));
}

function buildGmailCard_(messageId, workspaceID, e) {
  var workspaces, folders = [];
  try {
    workspaces = listWorkspaces_();
    if (workspaceID) {
      folders = listFolders_(workspaceID);
    }
  } catch (err) {
    return errorCard_(String(err.message || err));
  }

  var section = CardService.newCardSection();
  section.addWidget(
    workspaceDropdown_(workspaces, workspaceID, 'onGmailWorkspaceChange', { message_id: messageId })
  );
  if (workspaceID) {
    section.addWidget(folderDropdown_(folders, e ? formValue_(e, 'folder_id') : ''));
  }
  // updateCard replaces the card wholesale, so every input must be
  // re-seeded from the triggering event or the user's typed tags and
  // file-as pick silently reset on a workspace change.
  section.addWidget(
    CardService.newTextInput()
      .setFieldName('tags')
      .setTitle('Tags (comma-separated)')
      .setHint('invoice, q1-2026')
      .setValue(e ? formValue_(e, 'tags') : '')
  );
  var fileAsSelected = (e && formValue_(e, 'file_as')) || 'both';
  var fileAs = CardService.newSelectionInput()
    .setType(CardService.SelectionInputType.DROPDOWN)
    .setFieldName('file_as')
    .setTitle('File as');
  fileAs.addItem('Message + attachments', 'both', fileAsSelected === 'both');
  fileAs.addItem('Message only', 'message', fileAsSelected === 'message');
  fileAs.addItem('Attachments only', 'attachments', fileAsSelected === 'attachments');
  section.addWidget(fileAs);

  section.addWidget(
    CardService.newTextButton()
      .setText('Save email to SeDoc')
      .setTextButtonStyle(CardService.TextButtonStyle.FILLED)
      .setOnClickAction(
        CardService.newAction()
          .setFunctionName('onGmailSave')
          .setParameters({ message_id: messageId })
      )
  );

  return CardService.newCardBuilder()
    .setHeader(CardService.newCardHeader().setTitle('Save to SeDoc'))
    .addSection(section)
    .build();
}

/** Save button — reads the message + attachments, POSTs the ingest. */
function onGmailSave(e) {
  var messageId = param_(e, 'message_id');
  var workspaceID = formValue_(e, 'workspace_id');
  var folderID = formValue_(e, 'folder_id');
  var fileAs = formValue_(e, 'file_as') || 'both';
  if (!workspaceID || !folderID) {
    return notify_('Pick a workspace and folder first.');
  }
  // The contextual access token grants short-lived read access to the
  // open message for gmail.addons.current.message.readonly.
  if (e.gmail && e.gmail.accessToken) {
    GmailApp.setCurrentMessageAccessToken(e.gmail.accessToken);
  }
  var msg;
  try {
    msg = GmailApp.getMessageById(messageId);
  } catch (err) {
    return notify_('Could not read the message: ' + String(err.message || err));
  }

  var attachments = [];
  if (fileAs !== 'message') {
    // includeInlineImages:false + includeAttachments:true mirrors the
    // Outlook add-in's "real file attachments only" v1 filter.
    msg.getAttachments({ includeInlineImages: false, includeAttachments: true }).forEach(function (a) {
      attachments.push({
        name: a.getName(),
        mime_type: a.getContentType(),
        content_b64: Utilities.base64Encode(a.getBytes())
      });
    });
  }

  var tags = formValue_(e, 'tags')
    .split(',')
    .map(function (t) { return t.trim(); })
    .filter(function (t) { return t.length > 0; });

  try {
    var res = ingestEmail_({
      subject: msg.getSubject() || '(no subject)',
      from: msg.getFrom(),
      to: (msg.getTo() || '').split(',').map(function (t) { return t.trim(); }).filter(Boolean),
      cc: (msg.getCc() || '').split(',').map(function (t) { return t.trim(); }).filter(Boolean),
      sent_at: msg.getDate().toISOString(),
      body_html: msg.getBody(),
      body_text: msg.getPlainBody(),
      attachments: attachments,
      workspace_id: workspaceID,
      folder_id: folderID,
      tags: tags,
      message_id: msg.getHeader('Message-ID') || messageId,
      include_body: fileAs !== 'attachments'
    });
    var attachCount = (res.attachment_document_ids || []).length;
    var summary = 'Filed the email' +
      (attachCount > 0 ? ' + ' + attachCount + ' attachment' + (attachCount === 1 ? '' : 's') : '') +
      '. OCR + classification run in the background.';
    return pushCard_(successCard_('Saved to SeDoc', summary, workspaceID, res.document_id));
  } catch (err) {
    return notify_('Save failed: ' + String(err.message || err));
  }
}

/** Simple full-card error surface for trigger-time failures. */
function errorCard_(message) {
  return CardService.newCardBuilder()
    .setHeader(CardService.newCardHeader().setTitle('SeDoc'))
    .addSection(
      CardService.newCardSection().addWidget(
        CardService.newTextParagraph().setText(message)
      )
    )
    .build();
}
