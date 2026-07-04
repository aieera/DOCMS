/**
 * Shared CardService UI helpers — the Google equivalent of the Office
 * add-ins' consistent task-pane: a workspace picker, a dependent folder
 * picker, and uniform error / success rendering.
 *
 * State that must survive between card interactions travels in action
 * parameters (setParameters) — never in any client-side storage.
 */

/** A one-line "something went wrong" notification response. */
function notify_(message) {
  return CardService.newActionResponseBuilder()
    .setNotification(CardService.newNotification().setText(message))
    .build();
}

/** Push a new card onto the stack. */
function pushCard_(card) {
  return CardService.newActionResponseBuilder()
    .setNavigation(CardService.newNavigation().pushCard(card))
    .build();
}

/** Replace the current card. */
function updateCard_(card) {
  return CardService.newActionResponseBuilder()
    .setNavigation(CardService.newNavigation().updateCard(card))
    .build();
}

/**
 * Workspace dropdown. `selectedID` keeps the current pick across
 * re-renders; `onChangeFunction` re-renders the card so the folder
 * dropdown can repopulate for the newly-picked workspace.
 */
function workspaceDropdown_(workspaces, selectedID, onChangeFunction, extraParams) {
  var input = CardService.newSelectionInput()
    .setType(CardService.SelectionInputType.DROPDOWN)
    .setFieldName('workspace_id')
    .setTitle('Workspace');
  input.addItem('— pick a workspace —', '', !selectedID);
  workspaces.forEach(function (w) {
    input.addItem(w.name || w.id, w.id, w.id === selectedID);
  });
  if (onChangeFunction) {
    var action = CardService.newAction().setFunctionName(onChangeFunction);
    if (extraParams) action.setParameters(extraParams);
    input.setOnChangeAction(action);
  }
  return input;
}

/** Folder dropdown for the selected workspace (empty state handled). */
function folderDropdown_(folders, selectedID) {
  var input = CardService.newSelectionInput()
    .setType(CardService.SelectionInputType.DROPDOWN)
    .setFieldName('folder_id')
    .setTitle('Folder');
  if (!folders || folders.length === 0) {
    input.addItem('— no folders in this workspace —', '', true);
    return input;
  }
  input.addItem('— pick a folder —', '', !selectedID);
  folders.forEach(function (f) {
    input.addItem(f.name || f.id, f.id, f.id === selectedID);
  });
  return input;
}

/** Read a form input's first value ('' when absent). */
function formValue_(e, name) {
  var inputs = e && e.commonEventObject && e.commonEventObject.formInputs;
  var entry = inputs && inputs[name];
  var vals = entry && entry.stringInputs && entry.stringInputs.value;
  return (vals && vals[0]) || '';
}

/** Read an action parameter ('' when absent). */
function param_(e, name) {
  return (e && e.parameters && e.parameters[name]) || '';
}

/**
 * Success card with an "Open in SeDoc" link button and optional extras.
 * `title` and `message` describe what happened; `workspaceID` + `docID`
 * power the deep link (both required — the web route is workspace-scoped).
 */
function successCard_(title, message, workspaceID, docID) {
  var section = CardService.newCardSection()
    .addWidget(CardService.newTextParagraph().setText(message));
  if (workspaceID && docID) {
    section.addWidget(
      CardService.newTextButton()
        .setText('Open in SeDoc')
        .setOpenLink(CardService.newOpenLink().setUrl(documentURL_(workspaceID, docID)))
    );
  }
  return CardService.newCardBuilder()
    .setHeader(CardService.newCardHeader().setTitle(title))
    .addSection(section)
    .build();
}
