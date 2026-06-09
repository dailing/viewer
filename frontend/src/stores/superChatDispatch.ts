import { defineStore } from "pinia";

export const useSuperChatDispatchStore = defineStore("superChatDispatch", {
  state: () => ({
    selectedRoleByChatId: {} as Record<string, string>,
  }),
  actions: {
    selectedRoleId(chatId: string): string {
      return this.selectedRoleByChatId[chatId] ?? "";
    },
    toggleRole(chatId: string, roleId: string) {
      if (!chatId || !roleId) return;
      if (this.selectedRoleByChatId[chatId] === roleId) {
        delete this.selectedRoleByChatId[chatId];
      } else {
        this.selectedRoleByChatId[chatId] = roleId;
      }
    },
    clearChat(chatId: string) {
      delete this.selectedRoleByChatId[chatId];
    },
    clearRole(chatId: string, roleId: string) {
      if (this.selectedRoleByChatId[chatId] === roleId) delete this.selectedRoleByChatId[chatId];
    },
  },
});
