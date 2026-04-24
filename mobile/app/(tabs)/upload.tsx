import { useState } from 'react'
import { View, Text, TouchableOpacity, StyleSheet, ActivityIndicator, Alert } from 'react-native'
import * as ImagePicker from 'expo-image-picker'
import { initiateUpload } from '../../api/upload'
import { captureDocument } from '../../lib/scanner'
import { enqueue, MAX_BYTES } from '../../lib/uploadQueue'
import { useAuthStore } from '../../store/authStore'

export default function UploadScreen() {
  const [uploading, setUploading] = useState(false)

  const pickAndUpload = async () => {
    const result = await ImagePicker.launchImageLibraryAsync({ mediaTypes: ImagePicker.MediaTypeOptions.All, quality: 0.8 })
    if (result.canceled) return
    const asset = result.assets[0]
    setUploading(true)
    try {
      const filename = asset.uri.split('/').pop() || 'file'
      const session = await initiateUpload({ filename, mime_type: asset.mimeType || 'application/octet-stream', size_bytes: asset.fileSize || 0 })
      const resp = await fetch(session.presigned_put_url, {
        method: 'PUT',
        headers: { 'Content-Type': asset.mimeType || 'application/octet-stream' },
        body: { uri: asset.uri } as any,
      })
      if (resp.ok) Alert.alert('Success', `${filename} uploaded`)
      else Alert.alert('Error', 'Upload failed')
    } catch (e) {
      Alert.alert('Error', String(e))
    } finally {
      setUploading(false)
    }
  }

  // Edge-detecting capture + queue. The queue handles offline,
  // reconnect resume, and 25 MB cap — upload happens here
  // synchronously when online, and next-tick when not.
  const takePhoto = async () => {
    const tenantId = useAuthStore.getState().tenantId
    if (!tenantId) return Alert.alert('Not signed in', 'Sign in before capturing')
    setUploading(true)
    try {
      const capture = await captureDocument()
      if (!capture) return
      const filename = `scan_${Date.now()}.jpg`
      try {
        await enqueue({
          tenantId,
          localPath: capture.uri,
          filename,
          mimeType: 'image/jpeg',
        })
        Alert.alert('Queued', 'Scan queued; will upload when online')
      } catch (e) {
        const msg = String(e).includes('capture too large')
          ? `Capture exceeds ${Math.round(MAX_BYTES / 1024 / 1024)} MB limit`
          : String(e)
        Alert.alert('Error', msg)
      }
    } finally {
      setUploading(false)
    }
  }

  return (
    <View style={styles.container}>
      {uploading ? (
        <ActivityIndicator size="large" color="#1E40AF" />
      ) : (
        <>
          <TouchableOpacity style={styles.button} onPress={pickAndUpload}>
            <Text style={styles.buttonText}>Choose from Gallery</Text>
          </TouchableOpacity>
          <TouchableOpacity style={[styles.button, styles.secondary]} onPress={takePhoto}>
            <Text style={[styles.buttonText, { color: '#1E40AF' }]}>Scan with Camera</Text>
          </TouchableOpacity>
        </>
      )}
    </View>
  )
}

const styles = StyleSheet.create({
  container: { flex: 1, justifyContent: 'center', padding: 24, backgroundColor: '#f8fafc' },
  button: { height: 50, backgroundColor: '#1E40AF', borderRadius: 10, justifyContent: 'center', alignItems: 'center', marginBottom: 12 },
  secondary: { backgroundColor: '#fff', borderWidth: 1, borderColor: '#1E40AF' },
  buttonText: { color: '#fff', fontSize: 16, fontWeight: '600' },
})
