import { useState } from 'react'
import { View, Text, TouchableOpacity, StyleSheet, ActivityIndicator, Alert } from 'react-native'
import * as ImagePicker from 'expo-image-picker'
import { initiateUpload } from '../../api/upload'

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

  const takePhoto = async () => {
    const perm = await ImagePicker.requestCameraPermissionsAsync()
    if (!perm.granted) return Alert.alert('Permission needed', 'Camera access required')
    const result = await ImagePicker.launchCameraAsync({ quality: 0.8 })
    if (result.canceled) return
    setUploading(true)
    try {
      const asset = result.assets[0]
      const filename = `scan_${Date.now()}.jpg`
      const session = await initiateUpload({ filename, mime_type: 'image/jpeg', size_bytes: asset.fileSize || 0 })
      await fetch(session.presigned_put_url, { method: 'PUT', headers: { 'Content-Type': 'image/jpeg' }, body: { uri: asset.uri } as any })
      Alert.alert('Success', 'Photo uploaded')
    } catch (e) {
      Alert.alert('Error', String(e))
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
