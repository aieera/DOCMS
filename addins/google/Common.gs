/**
 * Common (non-contextual) homepage — shown when the add-on is opened
 * from the side panel outside a specific message/document context.
 */
function onHomepage() {
  var section = CardService.newCardSection()
    .addWidget(CardService.newTextParagraph().setText(
      'SeDoc for Google Workspace.<br><br>' +
      '<b>Gmail</b>: open a message to file it (with attachments) into a SeDoc workspace/folder.<br>' +
      '<b>Docs</b>: open a SeDoc document as a Google Doc, save it back as a new (conflict-checked) version, or insert a link.'
    ))
    .addWidget(
      CardService.newTextButton()
        .setText('Open SeDoc')
        .setOpenLink(CardService.newOpenLink().setUrl(CONFIG.API_BASE + '/'))
    );
  return CardService.newCardBuilder()
    .setHeader(CardService.newCardHeader().setTitle('SeDoc'))
    .addSection(section)
    .build();
}
