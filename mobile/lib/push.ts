// Push registration + tap handling (ADR 0117).
//
// After login the app registers its ExponentPushToken with the
// notification service (POST /api/v1/notifications/devices); the
// backend fans notifications out through Expo's push service, which
// fronts FCM + APNs. Tapping a notification deep-links into the
// document it references.
//
// Expo Go can't receive remote pushes on SDK 51+ — a dev/standalone
// build is required (same constraint as the native document scanner).
// All failures here are soft: the app works fully without push.
import * as Notifications from 'expo-notifications'
import Constants from 'expo-constants'
import { router } from 'expo-router'
import { api } from '../api/client'

// Foreground presentation: show banners even while the app is open.
Notifications.setNotificationHandler({
  handleNotification: async () => ({
    shouldShowAlert: true,
    shouldPlaySound: false,
    shouldSetBadge: true,
  }),
})

/**
 * Ask permission, fetch the Expo push token, and register it with the
 * backend. Returns true when a device registration was created.
 */
export async function registerForPush(): Promise<boolean> {
  try {
    const perms = await Notifications.getPermissionsAsync()
    let status = perms.status
    if (status !== 'granted') {
      status = (await Notifications.requestPermissionsAsync()).status
    }
    if (status !== 'granted') return false

    // EAS project id is required for getExpoPushTokenAsync in dev
    // builds; absent (e.g. Expo Go) → skip quietly.
    const projectId: string | undefined =
      Constants.expoConfig?.extra?.eas?.projectId ?? Constants.easConfig?.projectId
    if (!projectId) return false

    const token = (await Notifications.getExpoPushTokenAsync({ projectId })).data
    if (!token) return false
    await api.post('/notifications/devices', {
      token,
      platform: 'expo',
      label: Constants.deviceName ?? 'mobile',
    })
    return true
  } catch {
    return false
  }
}

/**
 * Route a notification tap to the resource it references. The backend
 * puts {resource_type, resource_id} into the push data payload.
 */
export function wirePushNavigation(): () => void {
  const sub = Notifications.addNotificationResponseReceivedListener((response) => {
    const data = response.notification.request.content.data as Record<string, string | undefined>
    if (data?.resource_type === 'document' && data.resource_id) {
      router.push(`/document/${data.resource_id}`)
      return
    }
    router.push('/(tabs)/notifications')
  })
  return () => sub.remove()
}
