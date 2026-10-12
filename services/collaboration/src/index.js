// Process bootstrap for the collaboration service. Validates configuration
// before creating any client, then wires Redis + NATS into the server.
import { randomUUID } from 'crypto';
import { loadConfig } from './config.js';

let config;
try {
  config = loadConfig(process.env);
} catch (err) {
  console.error(`collaboration: refusing to start: ${err.message}`);
  process.exit(1);
}

const { createClient } = await import('./redis.js');
const { startAnnotationBridge } = await import('./nats-bridge.js');
const { createCollabServer } = await import('./server.js');

const redisSub = createClient();
const redisPub = createClient();
const redisStore = createClient();

// §17.3 / D10 WS fan-out — bridge NATS annotation events into the
// Redis doc-room broadcast path. Non-fatal if it fails; WS service
// still serves connections even without the bridge.
startAnnotationBridge(redisPub, config.natsUrl, randomUUID())
  .then(() => console.log(`annotation bridge subscribed to NATS ${config.natsUrl}`))
  .catch((err) => console.error('annotation bridge failed to start:', err));

const server = createCollabServer({ port: config.port, redisPub, redisSub, redisStore });
server.listening.then(
  () => console.log(`collaboration ws server listening on :${server.port}`),
  (err) => {
    console.error('collaboration: listen failed:', err);
    process.exit(1);
  },
);

// CB-5: on SIGTERM/SIGINT flush every dirty Yjs room (bounded at 10 s)
// before exiting, so a rolling deploy doesn't drop up to FLUSH_EVERY edits
// per room.
let stopping = false;
async function stop(signal) {
  if (stopping) return;
  stopping = true;
  console.log(`collaboration: ${signal} received, flushing rooms`);
  try {
    const res = await server.shutdown();
    console.log(`collaboration: flushed=${res.flushed} failed=${res.failed} timedOut=${res.timedOut}`);
  } catch (err) {
    console.error('collaboration: shutdown error', err);
  }
  for (const c of [redisSub, redisPub, redisStore]) {
    try { c.disconnect(); } catch { /* ignore */ }
  }
  console.log('collaboration: shutdown complete');
  process.exit(0);
}
process.on('SIGTERM', () => { stop('SIGTERM'); });
process.on('SIGINT', () => { stop('SIGINT'); });
