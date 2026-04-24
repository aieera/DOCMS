// Per-tenant AES-GCM for the offline cache.
//
// Key lifecycle:
//   - One 256-bit key per tenant, generated on first use.
//   - Stored in expo-secure-store under `vaultdms.ek.<tenantId>` —
//     Keychain (iOS) / Keystore (Android), never persisted anywhere
//     else on the device.
//   - Nonce is 12 bytes, generated per-ciphertext with expo-crypto's
//     getRandomBytes — AES-GCM nonce uniqueness is the single
//     non-negotiable invariant; a repeat breaks confidentiality.
//
// Ciphertext layout on disk: [nonce (12 B) | ciphertext+tag (n B)].
// The tag is appended by @noble/ciphers/aes#gcm automatically.

import * as SecureStore from 'expo-secure-store'
import * as Crypto from 'expo-crypto'
import { gcm } from '@noble/ciphers/aes'

const KEY_BYTES   = 32
const NONCE_BYTES = 12

function secretKeyName(tenantId: string): string {
  return `vaultdms.ek.${tenantId}`
}

function toHex(bytes: Uint8Array): string {
  return Array.from(bytes).map((b) => b.toString(16).padStart(2, '0')).join('')
}
function fromHex(hex: string): Uint8Array {
  const out = new Uint8Array(hex.length / 2)
  for (let i = 0; i < out.length; i++) out[i] = parseInt(hex.slice(i * 2, i * 2 + 2), 16)
  return out
}

export async function getOrCreateTenantKey(tenantId: string): Promise<Uint8Array> {
  const existing = await SecureStore.getItemAsync(secretKeyName(tenantId))
  if (existing) return fromHex(existing)
  const fresh = Crypto.getRandomBytes(KEY_BYTES)
  await SecureStore.setItemAsync(secretKeyName(tenantId), toHex(fresh))
  return fresh
}

export async function forgetTenantKey(tenantId: string): Promise<void> {
  await SecureStore.deleteItemAsync(secretKeyName(tenantId))
}

export async function encryptBytes(tenantId: string, plaintext: Uint8Array): Promise<Uint8Array> {
  const key = await getOrCreateTenantKey(tenantId)
  const nonce = Crypto.getRandomBytes(NONCE_BYTES)
  const ciphertext = gcm(key, nonce).encrypt(plaintext)
  const out = new Uint8Array(nonce.length + ciphertext.length)
  out.set(nonce, 0)
  out.set(ciphertext, nonce.length)
  return out
}

export async function decryptBytes(tenantId: string, blob: Uint8Array): Promise<Uint8Array> {
  const key = await getOrCreateTenantKey(tenantId)
  const nonce = blob.slice(0, NONCE_BYTES)
  const ciphertext = blob.slice(NONCE_BYTES)
  return gcm(key, nonce).decrypt(ciphertext)
}
