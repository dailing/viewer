import { defineStore } from "pinia";

export type PaneToolbarAction = {
  id: string;
  title: string;
  icon?: string;
  label?: string;
  active?: boolean;
  variant?: "default" | "danger";
  run: () => void | Promise<void>;
};

export type PaneToolbarControl =
  | {
      kind: "select";
      id: string;
      title: string;
      value: string;
      options: string[];
      onChange: (value: string) => void | Promise<void>;
    }
  | {
      kind: "chips";
      id: string;
      title?: string;
      items: string[];
    };

export type PaneToolbar = {
  title?: string;
  status?: string;
  statusClass?: string;
  actions?: PaneToolbarAction[];
  controls?: PaneToolbarControl[];
};

export const usePaneToolbarStore = defineStore("paneToolbar", {
  state: () => ({
    toolbars: {} as Record<string, PaneToolbar>,
  }),
  getters: {
    forPane: (state) => (paneId: string) => state.toolbars[paneId],
  },
  actions: {
    setPaneToolbar(paneId: string, toolbar: PaneToolbar) {
      this.toolbars[paneId] = toolbar;
    },
    clearPaneToolbar(paneId: string) {
      delete this.toolbars[paneId];
    },
  },
});
