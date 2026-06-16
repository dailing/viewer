import { defineStore } from "pinia";
import { namespacedStorageKey } from "../utils/userProfile";

const DISPATCH_SELECTION_STORAGE_KEY = "viewer.superChatDispatchRoles.v1";
const CANONICAL_CHAT_ID = /^[0-9a-f]{12}$/i;

function isCanonicalChatId(chatId: string) {
  return CANONICAL_CHAT_ID.test(chatId);
}

function readSelections(): Record<string, string[]> {
  try {
    const raw = localStorage.getItem(namespacedStorageKey(DISPATCH_SELECTION_STORAGE_KEY));
    const parsed = raw ? JSON.parse(raw) : {};
    if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) return {};
    const selections: Record<string, string[]> = {};
    let hasLegacy = false;
    for (const [chatId, roleIds] of Object.entries(parsed)) {
      if (!isCanonicalChatId(chatId)) {
        hasLegacy = true;
        continue;
      }
      if (!Array.isArray(roleIds)) {
        hasLegacy = true;
        continue;
      }
      const clean = roleIds.filter((roleId): roleId is string => typeof roleId === "string" && Boolean(roleId));
      if (clean.length) selections[chatId] = [...new Set(clean)];
      else hasLegacy = true;
    }
    if (hasLegacy) writeSelections(selections);
    return selections;
  } catch {
    return {};
  }
}

function writeSelections(value: Record<string, string[]>) {
  localStorage.setItem(namespacedStorageKey(DISPATCH_SELECTION_STORAGE_KEY), JSON.stringify(value));
}

export const useSuperChatDispatchStore = defineStore("superChatDispatch", {
  state: () => ({
    selectedRolesByChatId: readSelections(),
  }),
  actions: {
    selectedRoleIds(chatId: string): string[] {
      if (!isCanonicalChatId(chatId)) return [];
      return this.selectedRolesByChatId[chatId] ?? [];
    },
    isRoleSelected(chatId: string, roleId: string): boolean {
      return this.selectedRoleIds(chatId).includes(roleId);
    },
    toggleRole(chatId: string, roleId: string) {
      if (!chatId || !roleId || !isCanonicalChatId(chatId)) return;
      const next = new Set(this.selectedRoleIds(chatId));
      if (next.has(roleId)) {
        next.delete(roleId);
      } else {
        next.add(roleId);
      }
      const selected = [...next];
      if (selected.length) this.selectedRolesByChatId[chatId] = selected;
      else delete this.selectedRolesByChatId[chatId];
      writeSelections(this.selectedRolesByChatId);
    },
    clearChat(chatId: string) {
      if (!isCanonicalChatId(chatId)) return;
      delete this.selectedRolesByChatId[chatId];
      writeSelections(this.selectedRolesByChatId);
    },
    migrateChatId(previousChatId: string, resolvedChatId: string) {
      if (!isCanonicalChatId(previousChatId) || !isCanonicalChatId(resolvedChatId) || previousChatId === resolvedChatId) return;
      const previous = this.selectedRolesByChatId[previousChatId];
      if (!previous?.length) {
        delete this.selectedRolesByChatId[previousChatId];
        writeSelections(this.selectedRolesByChatId);
        return;
      }
      const next = new Set([...(this.selectedRolesByChatId[resolvedChatId] ?? []), ...previous]);
      this.selectedRolesByChatId[resolvedChatId] = [...next];
      delete this.selectedRolesByChatId[previousChatId];
      writeSelections(this.selectedRolesByChatId);
    },
    clearRole(chatId: string, roleId: string) {
      if (!isCanonicalChatId(chatId)) return;
      const selected = this.selectedRoleIds(chatId).filter((id) => id !== roleId);
      if (selected.length) this.selectedRolesByChatId[chatId] = selected;
      else delete this.selectedRolesByChatId[chatId];
      writeSelections(this.selectedRolesByChatId);
    },
  },
});
