const LEGACY_USER_SUFFIX = ".dailing";
const LEGACY_ACTIVE_USER_KEY = "viewer.activeUser.v1";

export function storageKey(key: string): string {
  localStorage.removeItem(LEGACY_ACTIVE_USER_KEY);
  const legacyKey = `${key}${LEGACY_USER_SUFFIX}`;
  if (localStorage.getItem(key) === null) {
    const legacyValue = localStorage.getItem(legacyKey);
    if (legacyValue !== null) localStorage.setItem(key, legacyValue);
  }
  localStorage.removeItem(legacyKey);
  return key;
}
