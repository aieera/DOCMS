import { useState } from 'react'
import { View, Text, TextInput, TouchableOpacity, StyleSheet, ActivityIndicator } from 'react-native'
import { useRouter } from 'expo-router'
import { useAuthStore } from '../../store/authStore'
import { login, verifyMFA, type LoginResponse } from '../../api/auth'
import { registerForPush } from '../../lib/push'

export default function LoginScreen() {
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')
  // MFA step: set when login returns mfa_required.
  const [mfaSession, setMfaSession] = useState<string | null>(null)
  const [totp, setTotp] = useState('')
  const router = useRouter()
  const authLogin = useAuthStore((s) => s.login)

  // The auth service returns { session_token, expires_at, user } —
  // tenant_id lives on the user view, not at the top level. (Reading
  // data.token/data.tenant_id here made every successful sign-in look
  // like "invalid credentials" — review finding.)
  const finishLogin = async (data: LoginResponse) => {
    if (!data.session_token || !data.user) {
      throw new Error('malformed login response')
    }
    await authLogin(data.user, data.session_token, data.user.tenant_id ?? '')
    // Best-effort: push registration must never block sign-in.
    void registerForPush()
    router.replace('/(tabs)')
  }

  const handleLogin = async () => {
    setLoading(true)
    setError('')
    try {
      const data = await login(email, password)
      if (data.mfa_required && data.mfa_session_token) {
        // Second factor required — swap to the TOTP step.
        setMfaSession(data.mfa_session_token)
        return
      }
      await finishLogin(data)
    } catch {
      setError('Invalid credentials')
    } finally {
      setLoading(false)
    }
  }

  const handleVerify = async () => {
    if (!mfaSession) return
    setLoading(true)
    setError('')
    try {
      await finishLogin(await verifyMFA(mfaSession, totp.trim()))
    } catch {
      setError('Invalid code — try again')
    } finally {
      setLoading(false)
    }
  }

  if (mfaSession) {
    return (
      <View style={styles.container}>
        <Text style={styles.logo}>SeDoc</Text>
        <Text style={styles.subtitle}>Enter your authenticator code</Text>
        <TextInput
          style={styles.input}
          placeholder="6-digit code"
          value={totp}
          onChangeText={setTotp}
          keyboardType="number-pad"
          autoFocus
          maxLength={8}
        />
        {error ? <Text style={styles.error}>{error}</Text> : null}
        <TouchableOpacity style={styles.button} onPress={() => void handleVerify()} disabled={loading || totp.trim().length < 6}>
          {loading ? <ActivityIndicator color="#fff" /> : <Text style={styles.buttonText}>Verify</Text>}
        </TouchableOpacity>
        <TouchableOpacity onPress={() => { setMfaSession(null); setTotp(''); setError('') }}>
          <Text style={styles.link}>Back to sign in</Text>
        </TouchableOpacity>
      </View>
    )
  }

  return (
    <View style={styles.container}>
      <Text style={styles.logo}>SeDoc</Text>
      <Text style={styles.subtitle}>Sign in to your account</Text>
      <TextInput style={styles.input} placeholder="Email" value={email} onChangeText={setEmail} autoCapitalize="none" keyboardType="email-address" />
      <TextInput style={styles.input} placeholder="Password" value={password} onChangeText={setPassword} secureTextEntry />
      {error ? <Text style={styles.error}>{error}</Text> : null}
      <TouchableOpacity style={styles.button} onPress={() => void handleLogin()} disabled={loading}>
        {loading ? <ActivityIndicator color="#fff" /> : <Text style={styles.buttonText}>Sign In</Text>}
      </TouchableOpacity>
    </View>
  )
}

const styles = StyleSheet.create({
  container: { flex: 1, justifyContent: 'center', padding: 24, backgroundColor: '#f8fafc' },
  logo: { fontSize: 28, fontWeight: '700', color: '#1E40AF', textAlign: 'center', marginBottom: 4 },
  subtitle: { fontSize: 14, color: '#64748b', textAlign: 'center', marginBottom: 24 },
  input: { height: 44, borderWidth: 1, borderColor: '#e2e8f0', borderRadius: 8, paddingHorizontal: 12, marginBottom: 12, backgroundColor: '#fff', fontSize: 15 },
  button: { height: 44, backgroundColor: '#1E40AF', borderRadius: 8, justifyContent: 'center', alignItems: 'center', marginTop: 8 },
  buttonText: { color: '#fff', fontSize: 15, fontWeight: '600' },
  error: { color: '#ef4444', fontSize: 13, marginBottom: 8, textAlign: 'center' },
  link: { color: '#1E40AF', fontSize: 14, textAlign: 'center', marginTop: 16 },
})
