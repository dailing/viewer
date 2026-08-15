import { defineStore } from "pinia";

export interface PaneChromeAction {
  id: string;
  title: string;
  icon?: string;
  label?: string;
  active?: boolean;
  variant?: "default" | "danger";
  run: () => void | Promise<void>;
}

export type PaneChromeControl =
  | {
      kind: "select";
      id: string;
      title: string;
      value: string;
      options: string[];
      size?: "compact";
      onChange: (value: string) => void | Promise<void>;
    }
  | {
      kind: "chips";
      id: string;
      title?: string;
      items: string[];
    };

export interface PaneChrome {
  title?: string;
  status?: string;
  statusClass?: string;
  actions?: PaneChromeAction[];
  controls?: PaneChromeControl[];
}

/** Instance-owned title-bar contributions, keyed by `paneType:instanceId`. */
export const usePaneChromeStore = defineStore("paneChrome", {
  state: () => ({
    chromeByUid: {} as Record<string, PaneChrome>,
  }),
  getters: {
    forUid: (state) => (uid: string): PaneChrome | undefined => state.chromeByUid[uid],
  },
  actions: {
    setChrome(uid: string, chrome: PaneChrome): void {
      this.chromeByUid[uid] = chrome;
    },
    clearChrome(uid: string): void {
      delete this.chromeByUid[uid];
    },
  },
});
