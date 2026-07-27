/**
 * Comment create/update/delete over the collaboration WS — persisted
 * through the document service and broadcast live.
 *
 * Regression context: comment_create used to broadcast WITHOUT
 * persisting (comments vanished on refetch) and update/delete had no
 * handlers at all. These tests pin: two clients see live create +
 * update + delete; the store reflects every mutation; a late joiner
 * (rejoin) gets the persisted state via comments_snapshot; and an
 * unauthorized session can neither mutate nor see anything.
 */

import { test, onTestFinished } from 'vitest';
import assert from 'node:assert/strict';

import { startHarness, TestClient } from './harness.js';

const DOC = 'doc-aaaa';

test('create/update/delete round-trip: two clients + persistence', async () => {
  const { api, server } = await startHarness();
  api.addUser('tok-alice', { name: 'Alice' });
  api.addUser('tok-bob', { name: 'Bob' });

  const alice = await new TestClient(server.url).open();
  const bob = await new TestClient(server.url).open();
  onTestFinished(() => { alice.close(); bob.close(); });
  await alice.auth('tok-alice');
  await bob.auth('tok-bob');
  await alice.subscribe(DOC);
  await bob.subscribe(DOC);

  // CREATE — both clients see it; the broadcast carries the PERSISTED id.
  alice.send({ type: 'comment_create', doc_id: DOC, body: 'first!' });
  const addedAtBob = await bob.waitFor('comment_added');
  const addedAtAlice = await alice.waitFor('comment_added');
  assert.equal(addedAtBob.comment.body, 'first!');
  assert.ok(addedAtBob.comment.id, 'broadcast must carry the persisted comment id');
  assert.equal(addedAtAlice.comment.id, addedAtBob.comment.id);
  const commentId = addedAtBob.comment.id;
  assert.ok(api.comments.get(DOC).has(commentId), 'comment must be persisted in the store');

  // UPDATE — live at the second client AND persisted.
  alice.send({ type: 'comment_update', doc_id: DOC, comment_id: commentId, body: 'first! (edited)' });
  const updated = await bob.waitFor('comment_updated');
  assert.equal(updated.comment_id, commentId);
  assert.equal(updated.body, 'first! (edited)');
  assert.equal(api.comments.get(DOC).get(commentId).body, 'first! (edited)',
    'update must persist, not just broadcast');

  // DELETE — live at the second client AND gone from the store.
  alice.send({ type: 'comment_delete', doc_id: DOC, comment_id: commentId });
  const deleted = await bob.waitFor('comment_deleted');
  assert.equal(deleted.comment_id, commentId);
  assert.equal(api.comments.get(DOC).has(commentId), false,
    'delete must persist, not just broadcast');
});

test('rejoin sees persisted state (room-load reconcile)', async () => {
  const { api, server } = await startHarness();
  api.addUser('tok-alice', { name: 'Alice' });
  api.addUser('tok-carol', { name: 'Carol' });

  const alice = await new TestClient(server.url).open();
  onTestFinished(() => alice.close());
  await alice.auth('tok-alice');
  await alice.subscribe(DOC);

  // Mutate while nobody else is in the room.
  alice.send({ type: 'comment_create', doc_id: DOC, body: 'v1' });
  const added = await alice.waitFor('comment_added');
  alice.send({ type: 'comment_update', doc_id: DOC, comment_id: added.comment.id, body: 'v2 (edited)' });
  await alice.waitFor('comment_updated');
  alice.send({ type: 'comment_create', doc_id: DOC, body: 'to be deleted' });
  const doomed = await alice.waitFor((m) => m.type === 'comment_added' && m.comment.body === 'to be deleted');
  alice.send({ type: 'comment_delete', doc_id: DOC, comment_id: doomed.comment.id });
  await alice.waitFor('comment_deleted');

  // Late joiner: the subscribe snapshot must reflect update + delete.
  const carol = await new TestClient(server.url).open();
  onTestFinished(() => carol.close());
  await carol.auth('tok-carol');
  const snapshot = await carol.subscribe(DOC);
  assert.equal(snapshot.comments.length, 1, 'deleted comment must not reappear');
  assert.equal(snapshot.comments[0].body, 'v2 (edited)', 'late joiner sees the EDITED body');
});

test('unauthorized sessions are rejected', async () => {
  const { api, server } = await startHarness();
  api.addUser('tok-alice', { name: 'Alice' });

  // Bad token → connection closed 4001, nothing persisted.
  const badTok = await new TestClient(server.url).open();
  badTok.send({ type: 'auth', token: 'not-a-real-token' });
  const closed = await badTok.waitForClose();
  assert.equal(closed.code, 4001);

  // Unauthenticated socket sending mutations → silently ignored:
  // nothing persisted, nothing broadcast to the room.
  const alice = await new TestClient(server.url).open();
  const rogue = await new TestClient(server.url).open();
  onTestFinished(() => { alice.close(); rogue.close(); });
  await alice.auth('tok-alice');
  await alice.subscribe(DOC);

  rogue.send({ type: 'comment_create', doc_id: DOC, body: 'injected' });
  rogue.send({ type: 'comment_delete', doc_id: DOC, comment_id: 'any' });
  await new Promise((r) => setTimeout(r, 300));
  assert.equal((api.comments.get(DOC) || new Map()).size, 0, 'no unauthorized persistence');
  assert.equal(alice.inbox.filter((m) => m.type.startsWith('comment_')).length, 0,
    'no comment mutation events may reach the room from an unauthorized socket');
  assert.equal(alice.inbox.filter((m) => m.type === 'comments_snapshot').length, 1,
    'sanity: alice did get her own room-load snapshot');
});

test('subscribe is denied when the caller lacks view on the document', async () => {
  const { api, server } = await startHarness();
  api.addUser('tok-mallory', { name: 'Mallory' });
  api.denyView('tok-mallory', DOC); // policy says no `view`

  const mallory = await new TestClient(server.url).open();
  const alice = await new TestClient(server.url).open();
  onTestFinished(() => { mallory.close(); alice.close(); });
  await mallory.auth('tok-mallory');

  // Subscribe must be rejected with an explicit frame and NO snapshot.
  mallory.send({ type: 'subscribe', doc_id: DOC });
  const denied = await mallory.waitFor('subscribe_denied');
  assert.equal(denied.doc_id, DOC);
  assert.equal(mallory.inbox.filter((m) => m.type === 'comments_snapshot').length, 0,
    'a denied subscriber must not receive the comments snapshot');

  // And a subsequently-created comment by an authorized user must never
  // reach Mallory (she never joined the room).
  api.addUser('tok-alice', { name: 'Alice' });
  await alice.auth('tok-alice');
  await alice.subscribe(DOC);
  alice.send({ type: 'comment_create', doc_id: DOC, body: 'secret' });
  await alice.waitFor('comment_added');
  await new Promise((r) => setTimeout(r, 200));
  assert.equal(mallory.inbox.filter((m) => m.type.startsWith('comment_')).length, 0,
    'a denied subscriber must not receive live comment events');
});

test('persistence failure sends comment_error and broadcasts nothing', async () => {
  const { api, server } = await startHarness();
  api.addUser('tok-alice', { name: 'Alice' });
  api.addUser('tok-bob', { name: 'Bob' });

  const alice = await new TestClient(server.url).open();
  const bob = await new TestClient(server.url).open();
  onTestFinished(() => { alice.close(); bob.close(); });
  await alice.auth('tok-alice');
  await bob.auth('tok-bob');
  await alice.subscribe(DOC);
  await bob.subscribe(DOC);

  // Updating a nonexistent comment → the store 404s → the caller gets
  // an error frame and the room sees NO comment_updated (no desync).
  alice.send({ type: 'comment_update', doc_id: DOC, comment_id: 'ghost', body: 'x' });
  const err = await alice.waitFor('comment_error');
  assert.equal(err.op, 'update');
  await new Promise((r) => setTimeout(r, 200));
  assert.equal(bob.inbox.filter((m) => m.type === 'comment_updated').length, 0,
    'failed persistence must not broadcast');
});
