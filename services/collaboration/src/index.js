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

createCollabServer({ port: config.port, redisPub, redisSub, redisStore });

console.log(`collaboration ws server listening on :${config.port}/ws`);
