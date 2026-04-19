import { useState } from 'react'
import { View, Text, TextInput, FlatList, TouchableOpacity, StyleSheet } from 'react-native'
import { useQuery } from '@tanstack/react-query'
import { useRouter } from 'expo-router'
import { search } from '../../api/search'

export default function SearchScreen() {
  const [query, setQuery] = useState('')
  const router = useRouter()
  const { data } = useQuery({
    queryKey: ['search', query],
    queryFn: () => search({ query }),
    enabled: query.length >= 2,
  })

  return (
    <View style={styles.container}>
      <TextInput style={styles.input} placeholder="Search documents..." value={query} onChangeText={setQuery} autoFocus />
      <FlatList
        data={data?.results || []}
        keyExtractor={(item) => item.document_id}
        renderItem={({ item }) => (
          <TouchableOpacity style={styles.result} onPress={() => router.push(`/document/${item.document_id}`)}>
            <Text style={styles.title}>{item.title}</Text>
            <Text style={styles.meta}>{item.mime_type} · {item.lifecycle_state}</Text>
          </TouchableOpacity>
        )}
        ListEmptyComponent={query.length >= 2 ? <Text style={styles.empty}>No results</Text> : null}
      />
    </View>
  )
}

const styles = StyleSheet.create({
  container: { flex: 1, backgroundColor: '#f8fafc', padding: 16 },
  input: { height: 44, borderWidth: 1, borderColor: '#e2e8f0', borderRadius: 10, paddingHorizontal: 12, backgroundColor: '#fff', fontSize: 15, marginBottom: 12 },
  result: { backgroundColor: '#fff', borderRadius: 8, padding: 14, marginBottom: 8, borderWidth: 1, borderColor: '#e2e8f0' },
  title: { fontSize: 15, fontWeight: '600' },
  meta: { fontSize: 12, color: '#64748b', marginTop: 2 },
  empty: { textAlign: 'center', color: '#94a3b8', marginTop: 40 },
})
