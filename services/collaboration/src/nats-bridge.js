// §17.3 / D10 WS fan-out — bridges NATS JetStream `dms.annotation.>`
// events into the existing Redis doc-room pub/sub so every WS client
// subscribed to (tenant, doc) sees annotation mutations in real time.
//
// Input:  CloudEvents envelopes from the document service's outbox
// Output: pushes to `dms:{tenant_id}:doc:{document_id}` on Redis;
//         connections.js already has a subscriber that broadcasts
//         every message on that channel to matching WS clients.
//
// Why a bridge (not a direct NATS → WS path): Redis is the shared
// state across collaboration replicas. A pod that didn't handle the
// upstream NATS message still needs to forward to its locally-
// connected WS clients. Bridging through Redis keeps a single
// broadcast path regardless of which replica ingested the event.

import { connect, JSONCodec } from 'nats';

const STREAM = 'ANNOTATION_EVENTS';
const SUBJECT_FILTER = 'dms.annotation.>';

/**
 * Start the bridge. Returns the nats connection so the caller can
 * close it on shutdown.
 * @param {import('ioredis').Redis} redisPub — ioredis publisher
 *   connected to the same Redis the WS connection manager reads.
 * @param {string} natsUrl — e.g. "nats://nats:4222"
 * @param {string} serviceId — unique per-pod id; used as the durable
 *   consumer name so re-starts resume where they left off.
 */
export async function startAnnotationBridge(redisPub, natsUrl, serviceId) {
  const nc = await connect({ servers: natsUrl, name: `collab-${serviceId}` });
  const jsm = await nc.jetstreamManager();
  const js = nc.jetstream();
  const jc = JSONCodec();

  // Ensure a durable, queue-group consumer so multiple collab pods
  // share the workload; each annotation event lands on exactly one
  // pod and gets re-broadcast via Redis to all pods with matching
  // WS clients.
  const consumerName = 'collab-annotations';
  try {
    await jsm.consumers.info(STREAM, consumerName);
  } catch {
    await jsm.consumers.add(STREAM, {
      durable_name: consumerName,
      filter_subject: SUBJECT_FILTER,
      deliver_policy: 'new',  // don't replay history on a new deploy
      ack_policy: 'explicit',
      max_deliver: 5,
    });
  }

  const consumer = await js.consumers.get(STREAM, consumerName);
  const iter = await consumer.consume();

  (async () => {
    for await (const msg of iter) {
      try {
        const envelope = jc.decode(msg.data);
        // CloudEvents: { data: <payload>, tenantid, type, ... }
        const payload = envelope.data || {};
        const tenantId = envelope.tenantid || payload.tenant_id;
        const docId = payload.document_id;
        if (!tenantId || !docId) {
          msg.ack();  // malformed but not this bridge's job to fix
          continue;
        }

        const channel = `dms:${tenantId}:doc:${docId}`;
        const wireMsg = {
          kind: 'annotation',
          action: actionFromType(envelope.type),  // created|updated|deleted
          annotation_id: payload.annotation_id,
          version_id: payload.version_id,
          page_number: payload.page_number,
          type: payload.type,
          actor: payload.created_by || payload.updated_by || payload.deleted_by,
        };
        await redisPub.publish(channel, JSON.stringify(wireMsg));
        msg.ack();
      } catch (err) {
        console.error('annotation bridge: failed to process', err);
        msg.nak();  // retry per max_deliver policy
      }
    }
  })().catch((err) => console.error('annotation bridge iterator:', err));

  return nc;
}

function actionFromType(evType) {
  if (!evType) return 'unknown';
  const last = evType.split('.').slice(-2, -1)[0];  // "dms.annotation.created.v1" → "created"
  return last || 'unknown';
}
