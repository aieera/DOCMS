// Task inbox — GET /api/v1/tasks/mine (ADR 0068). Complete/reopen in
// place; tasks linked to a document deep-link into it (where in_review
// documents also expose Approve/Reject).
import { useState } from 'react'
import {
  View, Text, FlatList, TouchableOpacity, StyleSheet,
  ActivityIndicator, RefreshControl, Alert, Switch,
} from 'react-native'
import { useRouter } from 'expo-router'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { myTasks, completeTask, reopenTask, type Task } from '../../api/tasks'

export default function TasksScreen() {
  const router = useRouter()
  const qc = useQueryClient()
  const [showCompleted, setShowCompleted] = useState(false)

  const { data: tasks, isLoading, refetch, isRefetching } = useQuery({
    queryKey: ['tasks', 'mine', showCompleted],
    queryFn: () => myTasks(showCompleted),
  })

  const refresh = () => void qc.invalidateQueries({ queryKey: ['tasks', 'mine'] })

  const complete = useMutation({
    mutationFn: (id: string) => completeTask(id),
    onSuccess: refresh,
    onError: (e: Error) => Alert.alert('Could not complete task', e.message),
  })
  const reopen = useMutation({
    mutationFn: (id: string) => reopenTask(id),
    onSuccess: refresh,
    onError: (e: Error) => Alert.alert('Could not reopen task', e.message),
  })

  if (isLoading) return <View style={styles.center}><ActivityIndicator size="large" color="#1E40AF" /></View>

  const rows = tasks ?? []

  return (
    <View style={styles.container}>
      <View style={styles.toggleRow}>
        <Text style={styles.toggleLabel}>Show completed</Text>
        <Switch value={showCompleted} onValueChange={setShowCompleted} trackColor={{ true: '#1E40AF' }} />
      </View>
      <FlatList
        data={rows}
        keyExtractor={(t) => t.id}
        refreshControl={<RefreshControl refreshing={isRefetching} onRefresh={() => void refetch()} />}
        ListEmptyComponent={<Text style={styles.empty}>No tasks assigned to you. 🎉</Text>}
        renderItem={({ item }) => <TaskRow task={item} onComplete={complete.mutate} onReopen={reopen.mutate} onOpenDoc={(d) => router.push(`/document/${d}`)} />}
        contentContainerStyle={{ paddingBottom: 24 }}
      />
    </View>
  )
}

function TaskRow({
  task, onComplete, onReopen, onOpenDoc,
}: {
  task: Task
  onComplete: (id: string) => void
  onReopen: (id: string) => void
  onOpenDoc: (documentID: string) => void
}) {
  const isDone = task.status === 'done' || task.status === 'cancelled'
  return (
    <View style={styles.card}>
      <View style={{ flex: 1 }}>
        <Text style={[styles.taskTitle, isDone && styles.taskDone]}>{task.title}</Text>
        {task.description ? <Text style={styles.taskDesc} numberOfLines={2}>{task.description}</Text> : null}
        <View style={styles.metaLine}>
          {task.priority ? <Text style={styles.priority}>{task.priority}</Text> : null}
          {task.due_at ? <Text style={styles.due}>due {new Date(task.due_at).toLocaleDateString()}</Text> : null}
        </View>
        {task.linked_document_id ? (
          <TouchableOpacity onPress={() => onOpenDoc(task.linked_document_id as string)}>
            <Text style={styles.docLink}>Open linked document →</Text>
          </TouchableOpacity>
        ) : null}
      </View>
      <TouchableOpacity
        style={[styles.doneBtn, isDone && styles.reopenBtn]}
        onPress={() => (isDone ? onReopen(task.id) : onComplete(task.id))}
      >
        <Text style={styles.doneBtnText}>{isDone ? 'Reopen' : 'Done'}</Text>
      </TouchableOpacity>
    </View>
  )
}

const styles = StyleSheet.create({
  container: { flex: 1, backgroundColor: '#f8fafc', padding: 16 },
  center: { flex: 1, justifyContent: 'center', alignItems: 'center' },
  toggleRow: { flexDirection: 'row', justifyContent: 'flex-end', alignItems: 'center', gap: 8, marginBottom: 8 },
  toggleLabel: { fontSize: 13, color: '#64748b' },
  empty: { textAlign: 'center', color: '#94a3b8', marginTop: 48 },
  card: { flexDirection: 'row', gap: 12, backgroundColor: '#fff', borderRadius: 10, padding: 14, marginBottom: 10, alignItems: 'center' },
  taskTitle: { fontSize: 15, fontWeight: '600', color: '#0f172a' },
  taskDone: { textDecorationLine: 'line-through', color: '#94a3b8' },
  taskDesc: { fontSize: 13, color: '#64748b', marginTop: 2 },
  metaLine: { flexDirection: 'row', gap: 10, marginTop: 4 },
  priority: { fontSize: 11, color: '#b45309', backgroundColor: '#fef3c7', paddingHorizontal: 6, paddingVertical: 1, borderRadius: 4, overflow: 'hidden' },
  due: { fontSize: 11, color: '#64748b' },
  docLink: { fontSize: 13, color: '#1E40AF', fontWeight: '600', marginTop: 6 },
  doneBtn: { paddingHorizontal: 14, height: 34, backgroundColor: '#16a34a', borderRadius: 8, justifyContent: 'center' },
  reopenBtn: { backgroundColor: '#64748b' },
  doneBtnText: { color: '#fff', fontSize: 13, fontWeight: '600' },
})
