// Background drain — wakes periodically to push the upload queue.
//
// iOS: BGTaskScheduler under the hood. The task identifier must be
//      declared in app.json under ios.infoPlist.BGTaskSchedulerPermittedIdentifiers;
//      iOS runs the task at its discretion (typically ≥15 min intervals,
//      network-available, not on cellular under low-power).
// Android: WorkManager under the hood with the same 15-min floor.
//
// Expo exposes both through expo-task-manager + expo-background-fetch.
// Real-time "within seconds of reconnect" pushes are handled by the
// foreground NetInfo listener in uploadQueue.ts; this task is the
// belt-and-braces path for when the app was backgrounded during the
// outage.

import * as BackgroundFetch from 'expo-background-fetch'
import * as TaskManager from 'expo-task-manager'
import { drain } from './uploadQueue'

export const UPLOAD_DRAIN_TASK = 'com.vaultdms.app.upload-drain'

TaskManager.defineTask(UPLOAD_DRAIN_TASK, async () => {
  try {
    await drain()
    return BackgroundFetch.BackgroundFetchResult.NewData
  } catch {
    return BackgroundFetch.BackgroundFetchResult.Failed
  }
})

export async function registerBackgroundSync(): Promise<void> {
  const status = await BackgroundFetch.getStatusAsync()
  if (status === BackgroundFetch.BackgroundFetchStatus.Restricted
      || status === BackgroundFetch.BackgroundFetchStatus.Denied) {
    return
  }
  const registered = await TaskManager.isTaskRegisteredAsync(UPLOAD_DRAIN_TASK)
  if (registered) return
  await BackgroundFetch.registerTaskAsync(UPLOAD_DRAIN_TASK, {
    minimumInterval: 15 * 60,      // OS-enforced floor on both platforms
    stopOnTerminate: false,        // Android
    startOnBoot: true,             // Android
  })
}
