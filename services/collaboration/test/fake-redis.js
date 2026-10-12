/**
 * In-process stand-in for the ioredis clients the collaboration service
 * uses: pub/sub (publish / subscribe / unsubscribe + 'message' and
 * 'messageBuffer' events, delivered asynchronously like the real server)
 * and the online-user SET commands. Every client created from one bus
 * shares the same channel space, so two "instances" wired to the same bus
 * behave like two pods on one Redis.
 */

import { EventEmitter } from 'node:events'

export function createFakeRedisBus() {
  const clients = new Set()
  const sets = new Map()
  const published = []

  function client() {
    const c = new EventEmitter()
    c.channels = new Set()
    c.subscribe = async (...chs) => {
      for (const ch of chs.flat()) c.channels.add(ch)
      return c.channels.size
    }
    c.unsubscribe = async (...chs) => {
      for (const ch of chs.flat()) c.channels.delete(ch)
      return c.channels.size
    }
    c.publish = async (channel, message) => {
      const buf = Buffer.isBuffer(message) ? message : Buffer.from(String(message))
      published.push({ channel, message: buf })
      let n = 0
      for (const s of clients) {
        if (!s.channels.has(channel) || s.down) continue
        n++
        setImmediate(() => {
          if (!s.channels.has(channel) || s.down) return
          s.emit('messageBuffer', Buffer.from(channel), Buffer.from(buf))
          s.emit('message', channel, buf.toString())
        })
      }
      return n
    }
    c.sadd = async (key, member) => {
      if (!sets.has(key)) sets.set(key, new Set())
      sets.get(key).add(member)
      return 1
    }
    c.srem = async (key, member) => (sets.get(key)?.delete(member) ? 1 : 0)
    c.smembers = async (key) => [...(sets.get(key) || [])]
    // Connection loss: while down, nothing published reaches this client.
    // ioredis re-subscribes on reconnect and then emits 'ready'; `silent`
    // models a loss nobody noticed (no events at all).
    c.down = false
    c.simulateDrop = ({ silent = false } = {}) => {
      c.down = true
      if (!silent) { c.emit('close'); c.emit('reconnecting', 50) }
    }
    c.simulateReconnect = ({ silent = false } = {}) => {
      c.down = false
      if (!silent) { c.emit('connect'); c.emit('ready') }
    }
    c.quit = async () => { clients.delete(c) }
    c.disconnect = () => { clients.delete(c) }
    clients.add(c)
    return c
  }

  return { client, published }
}
