// CB-3 review: login, MFA-verify and passkey responses include
// session_token — the same secret as the HttpOnly dms_session cookie.
// The browser session rides on that cookie alone, so the SPA never needs
// the value; drop it at the API boundary so no caller can retain it.
export function stripSessionToken<T>(data: T): Omit<T, 'session_token'> {
  if (data && typeof data === 'object' && 'session_token' in (data as object)) {
    const { session_token: _drop, ...rest } = data as T & { session_token?: unknown }
    void _drop
    return rest as Omit<T, 'session_token'>
  }
  return data as Omit<T, 'session_token'>
}
