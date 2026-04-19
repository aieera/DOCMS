import { View, Text, ScrollView, StyleSheet, TouchableOpacity, ActivityIndicator } from 'react-native'
import { useLocalSearchParams } from 'expo-router'
import { useQuery } from '@tanstack/react-query'
import { api } from '../../api/client'

async function getDocument(id: string) {
  const { data } = await api.get(`/documents/${id}`)
  return data
}

export default function DocumentScreen() {
  const { id } = useLocalSearchParams<{ id: string }>()
  const { data: doc, isLoading } = useQuery({ queryKey: ['document', id], queryFn: () => getDocument(id) })

  if (isLoading) return <View style={styles.center}><ActivityIndicator size="large" color="#1E40AF" /></View>
  if (!doc) return <View style={styles.center}><Text>Document not found</Text></View>

  return (
    <ScrollView style={styles.container}>
      <Text style={styles.title}>{doc.title}</Text>
      <View style={styles.metaRow}><Text style={styles.label}>Status</Text><Text style={styles.badge}>{doc.lifecycle_state}</Text></View>
      <View style={styles.metaRow}><Text style={styles.label}>Type</Text><Text style={styles.value}>{doc.document_class || doc.mime_type}</Text></View>
      <View style={styles.metaRow}><Text style={styles.label}>Size</Text><Text style={styles.value}>{(doc.size_bytes / 1024).toFixed(0)} KB</Text></View>
      <View style={styles.metaRow}><Text style={styles.label}>Versions</Text><Text style={styles.value}>{doc.version_count}</Text></View>
      <View style={styles.metaRow}><Text style={styles.label}>Created by</Text><Text style={styles.value}>{doc.created_by_name}</Text></View>
      {doc.tags?.length > 0 && (
        <View style={styles.tags}>
          {doc.tags.map((t: string) => <Text key={t} style={styles.tag}>{t}</Text>)}
        </View>
      )}
      <View style={styles.actions}>
        <TouchableOpacity style={styles.actionBtn}><Text style={styles.actionText}>Download</Text></TouchableOpacity>
        <TouchableOpacity style={styles.actionBtn}><Text style={styles.actionText}>Share</Text></TouchableOpacity>
      </View>
      <View style={styles.viewer}>
        <Text style={styles.viewerText}>Document viewer placeholder</Text>
      </View>
    </ScrollView>
  )
}

const styles = StyleSheet.create({
  container: { flex: 1, backgroundColor: '#f8fafc', padding: 16 },
  center: { flex: 1, justifyContent: 'center', alignItems: 'center' },
  title: { fontSize: 20, fontWeight: '700', marginBottom: 16 },
  metaRow: { flexDirection: 'row', justifyContent: 'space-between', paddingVertical: 8, borderBottomWidth: 1, borderBottomColor: '#e2e8f0' },
  label: { fontSize: 14, color: '#64748b' },
  value: { fontSize: 14, fontWeight: '500' },
  badge: { fontSize: 12, color: '#1E40AF', backgroundColor: '#dbeafe', paddingHorizontal: 8, paddingVertical: 2, borderRadius: 4, overflow: 'hidden' },
  tags: { flexDirection: 'row', flexWrap: 'wrap', gap: 6, marginTop: 12 },
  tag: { fontSize: 12, color: '#475569', backgroundColor: '#f1f5f9', paddingHorizontal: 8, paddingVertical: 3, borderRadius: 12 },
  actions: { flexDirection: 'row', gap: 10, marginTop: 16 },
  actionBtn: { flex: 1, height: 40, backgroundColor: '#1E40AF', borderRadius: 8, justifyContent: 'center', alignItems: 'center' },
  actionText: { color: '#fff', fontSize: 14, fontWeight: '600' },
  viewer: { marginTop: 20, height: 300, backgroundColor: '#e2e8f0', borderRadius: 10, justifyContent: 'center', alignItems: 'center' },
  viewerText: { color: '#94a3b8' },
})
