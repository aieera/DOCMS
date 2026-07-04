// Document screen — metadata, a real viewer, comments, annotations, and
// lifecycle approve/reject (ADR 0117).
//
// Viewer: PDFs render via react-native-pdf (native module — needs a dev/
// standalone build, like the scanner); images via <Image>. Both stream
// GET /api/v1/documents/{id}/content with the session bearer — the
// backend decrypts envelope-encrypted blobs on that route, so no
// presigned-URL dance is needed. Other MIME types fall back to
// download-and-share.
import { useMemo, useState } from 'react'
import {
  View, Text, ScrollView, StyleSheet, TouchableOpacity,
  ActivityIndicator, Image, TextInput, Alert, useWindowDimensions,
} from 'react-native'
import { useLocalSearchParams } from 'expo-router'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import * as FileSystem from 'expo-file-system'
import * as Sharing from 'expo-sharing'
import Pdf from 'react-native-pdf'

import { api, API_BASE } from '../../api/client'
import { useAuthStore } from '../../store/authStore'
import { updateLifecycle } from '../../api/tasks'
import {
  listComments, createComment, replyToComment, resolveComment,
  listAnnotations, createNoteAnnotation, listVersions,
  type CommentThread, type Annotation,
} from '../../api/collab'

async function getDocument(id: string) {
  const { data } = await api.get(`/documents/${id}`)
  return data
}

type Panel = 'preview' | 'comments' | 'annotations'

export default function DocumentScreen() {
  const { id } = useLocalSearchParams<{ id: string }>()
  const qc = useQueryClient()
  const [panel, setPanel] = useState<Panel>('preview')
  const { data: doc, isLoading } = useQuery({ queryKey: ['document', id], queryFn: () => getDocument(id) })

  const lifecycle = useMutation({
    mutationFn: ({ action, reason }: { action: 'approve' | 'reject'; reason?: string }) =>
      updateLifecycle(id, action, reason),
    onSuccess: () => void qc.invalidateQueries({ queryKey: ['document', id] }),
    onError: (e: Error) => Alert.alert('Action failed', e.message),
  })

  const confirmLifecycle = (action: 'approve' | 'reject') => {
    Alert.alert(
      action === 'approve' ? 'Approve document?' : 'Reject document?',
      action === 'approve'
        ? 'The document becomes active.'
        : 'The document returns to draft.',
      [
        { text: 'Cancel', style: 'cancel' },
        {
          text: action === 'approve' ? 'Approve' : 'Reject',
          style: action === 'reject' ? 'destructive' : 'default',
          onPress: () => lifecycle.mutate({ action, reason: `${action} from mobile` }),
        },
      ],
    )
  }

  if (isLoading) return <View style={styles.center}><ActivityIndicator size="large" color="#1E40AF" /></View>
  if (!doc) return <View style={styles.center}><Text>Document not found</Text></View>

  // grpc-gateway serializes the enum as its proto name
  // (LIFECYCLE_STATE_IN_REVIEW) — normalize before comparing/displaying.
  const lifecycleState = String(doc.lifecycle_state ?? '')
    .replace(/^LIFECYCLE_STATE_/, '')
    .toLowerCase()

  return (
    <ScrollView style={styles.container}>
      <Text style={styles.title}>{doc.title}</Text>
      <View style={styles.metaRow}><Text style={styles.label}>Status</Text><Text style={styles.badge}>{lifecycleState.replace(/_/g, ' ')}</Text></View>
      <View style={styles.metaRow}><Text style={styles.label}>Type</Text><Text style={styles.value}>{doc.document_class || doc.mime_type}</Text></View>
      <View style={styles.metaRow}><Text style={styles.label}>Size</Text><Text style={styles.value}>{(doc.size_bytes / 1024).toFixed(0)} KB</Text></View>
      <View style={styles.metaRow}><Text style={styles.label}>Versions</Text><Text style={styles.value}>{doc.version_count}</Text></View>
      <View style={styles.metaRow}><Text style={styles.label}>Created by</Text><Text style={styles.value}>{doc.created_by_name}</Text></View>
      {doc.tags?.length > 0 && (
        <View style={styles.tags}>
          {doc.tags.map((t: string) => <Text key={t} style={styles.tag}>{t}</Text>)}
        </View>
      )}

      {lifecycleState === 'in_review' && (
        <View style={styles.actions}>
          <TouchableOpacity
            style={[styles.actionBtn, { backgroundColor: '#16a34a' }]}
            disabled={lifecycle.isPending}
            onPress={() => confirmLifecycle('approve')}
          >
            <Text style={styles.actionText}>Approve</Text>
          </TouchableOpacity>
          <TouchableOpacity
            style={[styles.actionBtn, { backgroundColor: '#dc2626' }]}
            disabled={lifecycle.isPending}
            onPress={() => confirmLifecycle('reject')}
          >
            <Text style={styles.actionText}>Reject</Text>
          </TouchableOpacity>
        </View>
      )}

      <View style={styles.actions}>
        <DownloadShareButton documentID={id} title={doc.title} mime={doc.mime_type} />
      </View>

      <View style={styles.panelTabs}>
        {(['preview', 'comments', 'annotations'] as Panel[]).map((p) => (
          <TouchableOpacity key={p} style={[styles.panelTab, panel === p && styles.panelTabActive]} onPress={() => setPanel(p)}>
            <Text style={[styles.panelTabText, panel === p && styles.panelTabTextActive]}>
              {p.charAt(0).toUpperCase() + p.slice(1)}
            </Text>
          </TouchableOpacity>
        ))}
      </View>

      {panel === 'preview' && <Viewer documentID={id} mime={doc.mime_type} />}
      {panel === 'comments' && <CommentsPanel documentID={id} />}
      {panel === 'annotations' && <AnnotationsPanel documentID={id} />}
    </ScrollView>
  )
}

// ---------------------------------------------------------------------
// Viewer
// ---------------------------------------------------------------------

function contentURL(documentID: string): string {
  return `${API_BASE}/documents/${encodeURIComponent(documentID)}/content`
}

function Viewer({ documentID, mime }: { documentID: string; mime?: string }) {
  const token = useAuthStore((s) => s.token)
  const { width } = useWindowDimensions()
  const [pdfError, setPdfError] = useState<string | null>(null)
  const headers = useMemo(() => ({ Authorization: `Bearer ${token}` }), [token])

  if (!token) return null
  const isPDF = mime === 'application/pdf'
  const isImage = !!mime && mime.startsWith('image/')

  if (isPDF) {
    if (pdfError) {
      return <Text style={styles.viewerNote}>Preview unavailable ({pdfError}) — use Download.</Text>
    }
    return (
      <Pdf
        trustAllCerts={false}
        source={{ uri: contentURL(documentID), headers, cache: true }}
        style={{ width: width - 32, height: 480, alignSelf: 'center', marginTop: 12, borderRadius: 10 }}
        onError={(e) => setPdfError(String((e as Error)?.message ?? e))}
      />
    )
  }
  if (isImage) {
    return (
      <Image
        source={{ uri: contentURL(documentID), headers }}
        style={{ width: width - 32, height: 420, alignSelf: 'center', marginTop: 12, borderRadius: 10 }}
        resizeMode="contain"
      />
    )
  }
  return (
    <Text style={styles.viewerNote}>
      No in-app preview for {mime || 'this type'} — use Download to open it in another app.
    </Text>
  )
}

function DownloadShareButton({ documentID, title, mime }: { documentID: string; title?: string; mime?: string }) {
  const token = useAuthStore((s) => s.token)
  const [busy, setBusy] = useState(false)

  const downloadAndShare = async () => {
    if (!token) return
    setBusy(true)
    try {
      const safe = (title || 'document').replace(/[^\w.-]+/g, '_').slice(0, 60)
      const ext = mime === 'application/pdf' ? '.pdf' : ''
      const dest = `${FileSystem.cacheDirectory}${safe}${ext}`
      const res = await FileSystem.downloadAsync(contentURL(documentID), dest, {
        headers: { Authorization: `Bearer ${token}` },
      })
      if (res.status !== 200) throw new Error(`download failed (${res.status})`)
      if (await Sharing.isAvailableAsync()) {
        await Sharing.shareAsync(res.uri, { mimeType: mime })
      } else {
        Alert.alert('Downloaded', `Saved to ${res.uri}`)
      }
    } catch (e) {
      Alert.alert('Download failed', String((e as Error).message ?? e))
    } finally {
      setBusy(false)
    }
  }

  return (
    <TouchableOpacity style={styles.actionBtn} disabled={busy} onPress={() => void downloadAndShare()}>
      {busy ? <ActivityIndicator color="#fff" /> : <Text style={styles.actionText}>Download / Share</Text>}
    </TouchableOpacity>
  )
}

// ---------------------------------------------------------------------
// Comments
// ---------------------------------------------------------------------

function CommentsPanel({ documentID }: { documentID: string }) {
  const qc = useQueryClient()
  const [body, setBody] = useState('')
  const [replyTo, setReplyTo] = useState<CommentThread | null>(null)
  const { data: comments, isLoading } = useQuery({
    queryKey: ['comments', documentID],
    queryFn: () => listComments(documentID),
  })
  const refresh = () => void qc.invalidateQueries({ queryKey: ['comments', documentID] })

  const post = useMutation({
    mutationFn: async () => {
      const text = body.trim()
      if (!text) return
      if (replyTo) await replyToComment(replyTo.id, text)
      else await createComment(documentID, text)
    },
    onSuccess: () => { setBody(''); setReplyTo(null); refresh() },
    onError: (e: Error) => Alert.alert('Comment failed', e.message),
  })

  const resolve = useMutation({
    mutationFn: (id: string) => resolveComment(id),
    onSuccess: refresh,
  })

  if (isLoading) return <ActivityIndicator style={{ marginTop: 16 }} color="#1E40AF" />

  return (
    <View style={{ marginTop: 12 }}>
      {(comments ?? []).length === 0 && <Text style={styles.viewerNote}>No comments yet.</Text>}
      {(comments ?? []).map((c) => (
        <View key={c.id} style={styles.comment}>
          <Text style={styles.commentAuthor}>{shortUser(c.author_id)}</Text>
          <Text style={styles.commentBody}>{c.body}</Text>
          <View style={styles.commentActions}>
            <TouchableOpacity onPress={() => setReplyTo(c)}>
              <Text style={styles.commentLink}>Reply</Text>
            </TouchableOpacity>
            {!c.is_resolved && (
              <TouchableOpacity onPress={() => resolve.mutate(c.id)}>
                <Text style={styles.commentLink}>Resolve</Text>
              </TouchableOpacity>
            )}
          </View>
          {c.replies.map((r) => (
            <View key={r.id} style={styles.reply}>
              <Text style={styles.commentAuthor}>{shortUser(r.author_id)}</Text>
              <Text style={styles.commentBody}>{r.body}</Text>
            </View>
          ))}
        </View>
      ))}
      {replyTo && (
        <View style={styles.replyBanner}>
          <Text style={styles.replyBannerText} numberOfLines={1}>Replying to: {replyTo.body}</Text>
          <TouchableOpacity onPress={() => setReplyTo(null)}><Text style={styles.commentLink}>✕</Text></TouchableOpacity>
        </View>
      )}
      <View style={styles.composer}>
        <TextInput
          style={styles.composerInput}
          placeholder={replyTo ? 'Write a reply…' : 'Write a comment…'}
          value={body}
          onChangeText={setBody}
          multiline
        />
        <TouchableOpacity
          style={[styles.actionBtn, { flex: 0, paddingHorizontal: 16 }]}
          disabled={post.isPending || !body.trim()}
          onPress={() => post.mutate()}
        >
          <Text style={styles.actionText}>Send</Text>
        </TouchableOpacity>
      </View>
    </View>
  )
}

// shortUser renders an author when only the id is available (the
// comments endpoint doesn't join display names).
function shortUser(id?: string): string {
  return id ? `User ${id.slice(0, 8)}` : 'Someone'
}

// ---------------------------------------------------------------------
// Annotations — list + add a page-pinned note (the mobile-authorable
// subset; rect/draw markup stays a web-viewer feature).
// ---------------------------------------------------------------------

function AnnotationsPanel({ documentID }: { documentID: string }) {
  const qc = useQueryClient()
  const [page, setPage] = useState('1')
  const [note, setNote] = useState('')

  const { data: versions } = useQuery({
    queryKey: ['versions', documentID],
    queryFn: () => listVersions(documentID),
  })
  const currentVersion = versions?.[0]?.id

  const { data: annotations, isLoading } = useQuery({
    queryKey: ['annotations', documentID, currentVersion],
    queryFn: () => listAnnotations(documentID, currentVersion as string),
    enabled: !!currentVersion,
  })

  const add = useMutation({
    mutationFn: async () => {
      const p = Math.max(1, parseInt(page, 10) || 1)
      await createNoteAnnotation(documentID, currentVersion as string, p, note.trim())
    },
    onSuccess: () => {
      setNote('')
      void qc.invalidateQueries({ queryKey: ['annotations', documentID, currentVersion] })
    },
    onError: (e: Error) => Alert.alert('Annotation failed', e.message),
  })

  if (!currentVersion) return <Text style={styles.viewerNote}>No versions yet — nothing to annotate.</Text>
  if (isLoading) return <ActivityIndicator style={{ marginTop: 16 }} color="#1E40AF" />

  return (
    <View style={{ marginTop: 12 }}>
      {(annotations ?? []).length === 0 && <Text style={styles.viewerNote}>No annotations on the current version.</Text>}
      {(annotations ?? []).map((a: Annotation) => (
        <View key={a.id} style={styles.comment}>
          <Text style={styles.commentAuthor}>p.{a.page} · {a.created_by_name || 'Someone'}</Text>
          <Text style={styles.commentBody}>{String((a.data as { body?: string })?.body ?? a.type)}</Text>
        </View>
      ))}
      <View style={styles.composer}>
        <TextInput
          style={[styles.composerInput, { flex: 0, width: 56 }]}
          placeholder="p."
          value={page}
          onChangeText={setPage}
          keyboardType="number-pad"
        />
        <TextInput
          style={styles.composerInput}
          placeholder="Add a note to this page…"
          value={note}
          onChangeText={setNote}
          multiline
        />
        <TouchableOpacity
          style={[styles.actionBtn, { flex: 0, paddingHorizontal: 16 }]}
          disabled={add.isPending || !note.trim()}
          onPress={() => add.mutate()}
        >
          <Text style={styles.actionText}>Add</Text>
        </TouchableOpacity>
      </View>
    </View>
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
  panelTabs: { flexDirection: 'row', gap: 8, marginTop: 20 },
  panelTab: { paddingVertical: 6, paddingHorizontal: 14, borderRadius: 16, backgroundColor: '#e2e8f0' },
  panelTabActive: { backgroundColor: '#1E40AF' },
  panelTabText: { fontSize: 13, color: '#475569', fontWeight: '500' },
  panelTabTextActive: { color: '#fff' },
  viewerNote: { color: '#94a3b8', marginTop: 16, textAlign: 'center' },
  comment: { backgroundColor: '#fff', borderRadius: 10, padding: 12, marginTop: 10 },
  commentAuthor: { fontSize: 12, fontWeight: '700', color: '#334155', marginBottom: 3 },
  commentBody: { fontSize: 14, color: '#0f172a' },
  commentActions: { flexDirection: 'row', gap: 16, marginTop: 6 },
  commentLink: { fontSize: 13, color: '#1E40AF', fontWeight: '600' },
  reply: { marginTop: 8, marginLeft: 12, paddingLeft: 10, borderLeftWidth: 2, borderLeftColor: '#e2e8f0' },
  replyBanner: { flexDirection: 'row', alignItems: 'center', gap: 8, backgroundColor: '#eff6ff', borderRadius: 8, padding: 8, marginTop: 10 },
  replyBannerText: { flex: 1, fontSize: 12, color: '#334155' },
  composer: { flexDirection: 'row', gap: 8, marginTop: 12, alignItems: 'flex-end' },
  composerInput: { flex: 1, minHeight: 40, maxHeight: 100, borderWidth: 1, borderColor: '#e2e8f0', borderRadius: 8, paddingHorizontal: 10, paddingVertical: 8, backgroundColor: '#fff', fontSize: 14 },
})
