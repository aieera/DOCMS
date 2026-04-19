import { View, Text, TouchableOpacity, StyleSheet } from 'react-native'
import { useRouter } from 'expo-router'
import { useAuthStore } from '../../store/authStore'

export default function ProfileScreen() {
  const { user, logout } = useAuthStore()
  const router = useRouter()

  const handleLogout = () => {
    logout()
    router.replace('/(auth)/login')
  }

  return (
    <View style={styles.container}>
      <View style={styles.avatar}>
        <Text style={styles.initials}>{user?.display_name?.charAt(0)?.toUpperCase() || '?'}</Text>
      </View>
      <Text style={styles.name}>{user?.display_name || 'User'}</Text>
      <Text style={styles.email}>{user?.email || ''}</Text>
      <Text style={styles.role}>{user?.role || 'member'}</Text>
      <TouchableOpacity style={styles.logoutBtn} onPress={handleLogout}>
        <Text style={styles.logoutText}>Logout</Text>
      </TouchableOpacity>
    </View>
  )
}

const styles = StyleSheet.create({
  container: { flex: 1, alignItems: 'center', justifyContent: 'center', backgroundColor: '#f8fafc', padding: 24 },
  avatar: { width: 64, height: 64, borderRadius: 32, backgroundColor: '#1E40AF', justifyContent: 'center', alignItems: 'center', marginBottom: 12 },
  initials: { color: '#fff', fontSize: 24, fontWeight: '700' },
  name: { fontSize: 20, fontWeight: '700', marginBottom: 4 },
  email: { fontSize: 14, color: '#64748b', marginBottom: 4 },
  role: { fontSize: 13, color: '#94a3b8', marginBottom: 24 },
  logoutBtn: { height: 44, paddingHorizontal: 32, backgroundColor: '#ef4444', borderRadius: 8, justifyContent: 'center' },
  logoutText: { color: '#fff', fontSize: 15, fontWeight: '600' },
})
