import { defineStore } from "pinia";
import { listUsers } from "../api/client";
import type { UserProfile } from "../types/files";
import { currentUserId, setCurrentUserId } from "../utils/userProfile";

export const useUsersStore = defineStore("users", {
  state: () => ({
    profiles: [] as UserProfile[],
    activeUserId: currentUserId(),
    loaded: false,
  }),
  getters: {
    activeProfile(state): UserProfile | null {
      return state.profiles.find((profile) => profile.id === state.activeUserId) ?? null;
    },
    needsSelection(state): boolean {
      return !state.activeUserId || !state.profiles.some((profile) => profile.id === state.activeUserId);
    },
  },
  actions: {
    async load() {
      this.profiles = await listUsers();
      if (this.activeUserId && !this.profiles.some((profile) => profile.id === this.activeUserId)) {
        this.activeUserId = "";
      }
      this.loaded = true;
    },
    select(userId: string) {
      this.activeUserId = userId;
      setCurrentUserId(userId);
    },
  },
});
