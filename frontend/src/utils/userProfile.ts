export const USER_STORAGE_KEY = "viewer.activeUser.v1";

export function currentUserId(): string {
  return localStorage.getItem(USER_STORAGE_KEY)?.trim() || "";
}

export function setCurrentUserId(userId: string) {
  localStorage.setItem(USER_STORAGE_KEY, userId);
}

export function namespacedStorageKey(key: string): string {
  const userId = currentUserId();
  return userId ? `${key}.${userId}` : key;
}
