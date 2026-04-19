import { View, Text, FlatList, StyleSheet, RefreshControl, TouchableOpacity } from 'react-native'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { getNotifications, markAsRead } from '../../api/notifications'

export default function NotificationsScreen() {
  const qc = useQueryClient()
  const { data, isLoading, refetch } = useQuery({ queryKey: ['notifications'], queryFn: () => getNotifications() })
  const mark = useMutation({ mutationFn: markAsRead, onSuccess: () => qc.invalidateQueries({ queryKey: ['notifications'] }) })

  return (
    <View style={styles.container}>
      <FlatList
        data={data?.items || []}
        keyExtractor={(item) => item.id}
        refreshControl={<RefreshControl refreshing={isLoading} onRefresh={refetch} />}
        renderItem={({ item }) => (
          <TouchableOpacity style={[styles.card, !item.read && styles.unread]} onPress={() => mark.mutate(item.id)}>
            <Text style={styles.title}>{item.title}</Text>
            <Text style={styles.body}>{item.body}</Text>
            <Text style={styles.time}>{item.created_at}</Text>
          </TouchableOpacity>
        )}
        ListEmptyComponent={!isLoading ? <Text style={styles.empty}>No notifications</Text> : null}
      />
    </View>
  )
}

const styles = StyleSheet.create({
  container: { flex: 1, backgroundColor: '#f8fafc', padding: 16 },
  card: { backgroundColor: '#fff', borderRadius: 8, padding: 14, marginBottom: 8, borderWidth: 1, borderColor: '#e2e8f0' },
  unread: { borderLeftWidth: 3, borderLeftColor: '#1E40AF' },
  title: { fontSize: 14, fontWeight: '600' },
  body: { fontSize: 13, color: '#475569', marginTop: 2 },
  time: { fontSize: 11, color: '#94a3b8', marginTop: 4 },
  empty: { textAlign: 'center', color: '#94a3b8', marginTop: 40 },
})
