import { useAuthStore } from '@/store/authStore'

const permissionCache = new Map<string, boolean>()

export function hasPermission(action: string, _resourceType: string, _resourceId: string): boolean {
  const user = useAuthStore.getState().user
  if (!user) return false
  if (user.role === 'admin') return true
  const key = `${action}:${_resourceType}:${_resourceId}`
  return permissionCache.get(key) ?? false
}

export function cachePermission(action: string, resourceType: string, resourceId: string, allowed: boolean) {
  permissionCache.set(`${action}:${resourceType}:${resourceId}`, allowed)
}

export function clearPermissionCache() {
  permissionCache.clear()
}
