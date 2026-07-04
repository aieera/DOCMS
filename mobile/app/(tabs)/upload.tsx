import { useEffect, useState } from 'react'
import {
  View, Text, TouchableOpacity, StyleSheet, ActivityIndicator, Alert,
  ScrollView, Image,
} from 'react-native'
import * as ImagePicker from 'expo-image-picker'

import { listWorkspaces, listFolders, type Workspace, type Folder } from '../../api/folders'
import { imagesToPdf } from '../../lib/pdf'
import { scanDocument, applyFilter, type PageFilter } from '../../lib/scan'
import { enqueue, drain, isOnline, startAutoDrain, active } from '../../store/queue'

// Camera → multi-page PDF → ingest. Capture pages (native scanner with edge
// detection, or the plain camera as fallback), retake/reorder/filter, pick a
// destination folder, then build a PDF and upload through the standard ingest
// flow — or queue it offline and sync when connectivity returns.
export default function UploadScreen() {
  const [pages, setPages] = useState<string[]>([])
  const [filter, setFilter] = useState<PageFilter>('none')
  const [workspaces, setWorkspaces] = useState<Workspace[]>([])
  const [wsId, setWsId] = useState('')
  const [folders, setFolders] = useState<Folder[]>([])
  const [folderId, setFolderId] = useState('')
  const [online, setOnline] = useState(true)
  const [queued, setQueued] = useState(0)
  const [progress, setProgress] = useState<number | null>(null)
  const [busy, setBusy] = useState(false)

  const refreshQueue = async () => setQueued((await active()).length)

  useEffect(() => {
    void listWorkspaces().then(setWorkspaces).catch(() => {})
    void isOnline().then(setOnline)
    void refreshQueue()
    // Auto-drain the offline queue when connectivity returns.
    const unsub = startAutoDrain(() => {
      void isOnline().then(setOnline)
      void refreshQueue()
    })
    return unsub
  }, [])

  useEffect(() => {
    if (!wsId) { setFolders([]); setFolderId(''); return }
    void listFolders(wsId).then(setFolders).catch(() => setFolders([]))
  }, [wsId])

  const addByScanner = async () => {
    try {
      const imgs = await scanDocument()
      if (imgs.length) setPages((p) => [...p, ...imgs])
    } catch {
      // Scanner not available (e.g. Expo Go) → fall back to the camera.
      await addByCamera()
    }
  }

  const addByCamera = async () => {
    const perm = await ImagePicker.requestCameraPermissionsAsync()
    if (!perm.granted) return Alert.alert('Permission needed', 'Camera access is required')
    const res = await ImagePicker.launchCameraAsync({ quality: 0.85 })
    if (res.canceled) return
    setPages((p) => [...p, res.assets[0].uri])
  }

  const move = (i: number, dir: -1 | 1) => {
    setPages((p) => {
      const j = i + dir
      if (j < 0 || j >= p.length) return p
      const n = [...p]
      ;[n[i], n[j]] = [n[j], n[i]]
      return n
    })
  }

  const retake = async (i: number) => {
    const perm = await ImagePicker.requestCameraPermissionsAsync()
    if (!perm.granted) return
    const res = await ImagePicker.launchCameraAsync({ quality: 0.85 })
    if (res.canceled) return
    setPages((p) => p.map((u, k) => (k === i ? res.assets[0].uri : u)))
  }

  const removePage = (i: number) => setPages((p) => p.filter((_, k) => k !== i))

  const onUpload = async () => {
    if (pages.length === 0) return Alert.alert('No pages', 'Capture at least one page')
    if (!wsId || !folderId) return Alert.alert('Pick a destination', 'Choose a workspace and folder')
    setBusy(true)
    setProgress(0)
    try {
      // Apply the chosen filter to every page, then render the PDF.
      const filtered = await Promise.all(pages.map((u) => applyFilter(u, filter)))
      const { uri, size } = await imagesToPdf(filtered)
      const filename = `scan_${Date.now()}.pdf`

      // Always enqueue (durable across restarts); drain immediately if online.
      await enqueue({ file_uri: uri, filename, workspace_id: wsId, folder_id: folderId, size_bytes: size })
      await refreshQueue()

      if (await isOnline()) {
        setProgress(0.5)
        const n = await drain(refreshQueue)
        setProgress(1)
        Alert.alert(n > 0 ? 'Uploaded' : 'Queued', n > 0
          ? `Uploaded ${n} document${n === 1 ? '' : 's'}. It’ll appear in the web app shortly.`
          : 'Saved to the offline queue — it will sync when you’re back online.')
      } else {
        setOnline(false)
        Alert.alert('Offline', 'Saved to the offline queue — it will sync automatically when you’re back online.')
      }
      setPages([])
    } catch (e) {
      Alert.alert('Error', String(e))
    } finally {
      setBusy(false)
      setProgress(null)
    }
  }

  return (
    <ScrollView style={styles.container} contentContainerStyle={{ padding: 16, gap: 12 }}>
      <View style={styles.statusRow}>
        <View style={[styles.dot, { backgroundColor: online ? '#10b981' : '#ef4444' }]} />
        <Text style={styles.statusText}>{online ? 'Online' : 'Offline'}</Text>
        {queued > 0 && <Text style={styles.queueBadge}>{queued} queued</Text>}
      </View>

      <View style={styles.row}>
        <TouchableOpacity style={styles.button} onPress={addByScanner} disabled={busy}>
          <Text style={styles.buttonText}>Scan page</Text>
        </TouchableOpacity>
        <TouchableOpacity style={[styles.button, styles.secondary]} onPress={addByCamera} disabled={busy}>
          <Text style={[styles.buttonText, { color: '#1E40AF' }]}>Camera</Text>
        </TouchableOpacity>
      </View>

      {/* filter */}
      <View style={styles.row}>
        {(['none', 'grayscale', 'enhance'] as PageFilter[]).map((f) => (
          <TouchableOpacity key={f} style={[styles.chip, filter === f && styles.chipActive]} onPress={() => setFilter(f)}>
            <Text style={[styles.chipText, filter === f && styles.chipTextActive]}>{f}</Text>
          </TouchableOpacity>
        ))}
      </View>

      {/* pages */}
      {pages.length === 0 ? (
        <Text style={styles.hint}>No pages yet — tap “Scan page” to capture (auto edge-detection on a dev build).</Text>
      ) : (
        pages.map((uri, i) => (
          <View key={`${uri}-${i}`} style={styles.pageRow}>
            <Image source={{ uri }} style={styles.thumb} />
            <Text style={styles.pageNo}>Page {i + 1}</Text>
            <View style={styles.pageActions}>
              <TouchableOpacity onPress={() => move(i, -1)}><Text style={styles.act}>↑</Text></TouchableOpacity>
              <TouchableOpacity onPress={() => move(i, 1)}><Text style={styles.act}>↓</Text></TouchableOpacity>
              <TouchableOpacity onPress={() => retake(i)}><Text style={styles.act}>retake</Text></TouchableOpacity>
              <TouchableOpacity onPress={() => removePage(i)}><Text style={[styles.act, { color: '#ef4444' }]}>✕</Text></TouchableOpacity>
            </View>
          </View>
        ))
      )}

      {/* destination */}
      <Text style={styles.label}>Workspace</Text>
      <View style={styles.pickerWrap}>
        {workspaces.map((w) => (
          <TouchableOpacity key={w.id} style={[styles.chip, wsId === w.id && styles.chipActive]} onPress={() => setWsId(w.id)}>
            <Text style={[styles.chipText, wsId === w.id && styles.chipTextActive]}>{w.name}</Text>
          </TouchableOpacity>
        ))}
      </View>
      {wsId !== '' && (
        <>
          <Text style={styles.label}>Folder</Text>
          <View style={styles.pickerWrap}>
            {folders.map((f) => (
              <TouchableOpacity key={f.id} style={[styles.chip, folderId === f.id && styles.chipActive]} onPress={() => setFolderId(f.id)}>
                <Text style={[styles.chipText, folderId === f.id && styles.chipTextActive]}>{f.name}</Text>
              </TouchableOpacity>
            ))}
          </View>
        </>
      )}

      {/* upload */}
      <TouchableOpacity style={[styles.button, styles.upload]} onPress={onUpload} disabled={busy || pages.length === 0}>
        {busy ? <ActivityIndicator color="#fff" /> : <Text style={styles.buttonText}>Upload {pages.length || ''} page PDF</Text>}
      </TouchableOpacity>
      {progress != null && (
        <View style={styles.progressTrack}><View style={[styles.progressFill, { width: `${Math.round(progress * 100)}%` }]} /></View>
      )}
    </ScrollView>
  )
}

const styles = StyleSheet.create({
  container: { flex: 1, backgroundColor: '#f8fafc' },
  statusRow: { flexDirection: 'row', alignItems: 'center', gap: 8 },
  dot: { width: 10, height: 10, borderRadius: 5 },
  statusText: { fontSize: 13, color: '#334155', fontWeight: '600' },
  queueBadge: { marginLeft: 8, fontSize: 12, color: '#92400e', backgroundColor: '#fef3c7', paddingHorizontal: 8, paddingVertical: 2, borderRadius: 8 },
  row: { flexDirection: 'row', gap: 8, flexWrap: 'wrap' },
  button: { flex: 1, height: 48, backgroundColor: '#1E40AF', borderRadius: 10, justifyContent: 'center', alignItems: 'center' },
  secondary: { backgroundColor: '#fff', borderWidth: 1, borderColor: '#1E40AF' },
  upload: { backgroundColor: '#0f766e', marginTop: 8 },
  buttonText: { color: '#fff', fontSize: 15, fontWeight: '600' },
  chip: { paddingHorizontal: 12, paddingVertical: 6, borderRadius: 16, borderWidth: 1, borderColor: '#cbd5e1', backgroundColor: '#fff' },
  chipActive: { backgroundColor: '#1E40AF', borderColor: '#1E40AF' },
  chipText: { color: '#334155', fontSize: 13 },
  chipTextActive: { color: '#fff' },
  hint: { color: '#64748b', fontSize: 13 },
  pageRow: { flexDirection: 'row', alignItems: 'center', gap: 10, backgroundColor: '#fff', borderRadius: 10, padding: 8, borderWidth: 1, borderColor: '#e2e8f0' },
  thumb: { width: 44, height: 56, borderRadius: 6, backgroundColor: '#e2e8f0' },
  pageNo: { fontSize: 13, fontWeight: '600', color: '#334155' },
  pageActions: { flexDirection: 'row', gap: 12, marginLeft: 'auto' },
  act: { fontSize: 13, color: '#1E40AF', fontWeight: '600' },
  label: { fontSize: 12, fontWeight: '700', color: '#64748b', textTransform: 'uppercase', marginTop: 4 },
  pickerWrap: { flexDirection: 'row', gap: 8, flexWrap: 'wrap' },
  progressTrack: { height: 6, backgroundColor: '#e2e8f0', borderRadius: 3, overflow: 'hidden' },
  progressFill: { height: 6, backgroundColor: '#0f766e' },
})
