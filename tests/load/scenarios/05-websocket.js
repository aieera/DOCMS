// Scenario 5: WebSocket — 5000 connections, 100 broadcasts/sec, measure
// delivery latency.
import ws from 'k6/ws';
import { check, sleep } from 'k6';
import { Trend, Counter, Rate } from 'k6/metrics';
import { WS_URL, randomTenant, randomString } from '../lib/config.js';

const wsConnectDuration = new Trend('ws_connect_duration', true);
const wsMessageLatency = new Trend('ws_message_latency', true);
const wsConnections = new Counter('ws_connections');
const wsMessagesSent = new Counter('ws_messages_sent');
const wsMessagesReceived = new Counter('ws_messages_received');

export const options = {
  scenarios: {
    connections: {
      executor: 'ramping-vus',
      startVUs: 0,
      stages: [
        { duration: '2m', target: 1000 },
        { duration: '2m', target: 3000 },
        { duration: '2m', target: 5000 },
        { duration: '10m', target: 5000 },
        { duration: '2m', target: 0 },
      ],
    },
  },
  thresholds: {
    'ws_connect_duration': ['p(99)<2000'],
    'ws_message_latency': ['p(99)<500'],
    'ws_connections': ['count>4000'],
  },
};

export default function () {
  const tenantId = randomTenant();
  const userId = `user-${randomString(8)}`;
  const docId = `doc-${randomString(12)}`;

  const start = Date.now();
  const res = ws.connect(WS_URL, {}, function (socket) {
    wsConnectDuration.add(Date.now() - start);
    wsConnections.add(1);

    // Authenticate.
    socket.send(JSON.stringify({ type: 'auth', token: `test-${userId}` }));

    socket.on('message', (data) => {
      wsMessagesReceived.add(1);
      try {
        const msg = JSON.parse(data);
        if (msg._ts) {
          wsMessageLatency.add(Date.now() - msg._ts);
        }
      } catch {}
    });

    // Subscribe to a document channel.
    socket.send(JSON.stringify({ type: 'subscribe', doc_id: docId }));

    // Periodically send presence updates and comments.
    let iteration = 0;
    const interval = setInterval(() => {
      iteration++;
      if (iteration % 5 === 0) {
        socket.send(JSON.stringify({
          type: 'presence',
          doc_id: docId,
          page: Math.floor(Math.random() * 10) + 1,
          cursor: { x: Math.random() * 800, y: Math.random() * 600 },
        }));
        wsMessagesSent.add(1);
      }
      if (iteration % 20 === 0) {
        socket.send(JSON.stringify({
          type: 'comment_create',
          doc_id: docId,
          body: `Load test comment ${randomString(16)}`,
          _ts: Date.now(),
        }));
        wsMessagesSent.add(1);
      }
    }, 1000);

    // Stay connected for the scenario duration.
    socket.setTimeout(() => {
      clearInterval(interval);
      socket.send(JSON.stringify({ type: 'unsubscribe', doc_id: docId }));
      socket.close();
    }, 60000 + Math.random() * 60000); // 1-2 min connection
  });

  check(res, { 'ws connected': (r) => r && r.status === 101 });
  sleep(1);
}
