// Zod schemas for every client→server WebSocket message. Server
// broadcasts are described in docs only — they're produced by this
// service so the type-safety burden is on the producer.
//
// Close-code convention (per the spec 4000-range range the brief
// names):
//   4001 — unauthenticated (cookie missing / invalid / csrf mismatch)
//   4003 — forbidden (policy denied room.join)
//   4400 — message failed schema validation
//   4408 — handshake timeout (legacy; retained only in case the
//          upgrade path ever becomes async)
//
// UUIDs use RFC-4122 / UUIDv7; zod's .uuid() accepts both, matching
// Postgres validation in the doc service.

import { z } from 'zod';

export const CLOSE_UNAUTHENTICATED = 4001;
export const CLOSE_FORBIDDEN       = 4003;
export const CLOSE_INVALID_MESSAGE = 4400;

const DocumentId = z.string().uuid();
const CommentId  = z.string().uuid();

const RoomJoin = z.object({
  type: z.literal('room.join'),
  document_id: DocumentId,
});

const RoomLeave = z.object({
  type: z.literal('room.leave'),
  document_id: DocumentId,
});

const CommentCreate = z.object({
  type: z.literal('comment.create'),
  document_id: DocumentId,
  body: z.string().min(1).max(10_000),
  parent_id: CommentId.optional(),
});

const CommentUpdate = z.object({
  type: z.literal('comment.update'),
  document_id: DocumentId,
  comment_id: CommentId,
  body: z.string().min(1).max(10_000),
});

const CommentDelete = z.object({
  type: z.literal('comment.delete'),
  document_id: DocumentId,
  comment_id: CommentId,
});

const TypingStart = z.object({
  type: z.literal('typing.start'),
  document_id: DocumentId,
});

const TypingStop = z.object({
  type: z.literal('typing.stop'),
  document_id: DocumentId,
});

export const ClientMessage = z.discriminatedUnion('type', [
  RoomJoin,
  RoomLeave,
  CommentCreate,
  CommentUpdate,
  CommentDelete,
  TypingStart,
  TypingStop,
]);

// Server-side broadcast types. Listed for documentation + the
// integration test's assertion helper. Not used to parse inbound
// messages.
export const BROADCAST_TYPES = Object.freeze([
  'comment.created',
  'comment.updated',
  'comment.deleted',
  'presence.joined',
  'presence.left',
  'typing.start',
  'typing.stop',
]);
