import { defineComponent } from "vue";
import { beforeEach, describe, expect, it } from "vitest";

import {
  pluginCatalog,
  registerPlugin,
  settingsRegistry,
  unregisterPlugin,
} from "./registries";

const Settings = defineComponent({ template: "<div>settings</div>" });

describe("unified plugin settings registry", () => {
  beforeEach(() => {
    pluginCatalog.clear();
    settingsRegistry.clear();
  });

  it("lists built-ins as enabled catalog entries and exposes their settings", () => {
    registerPlugin("llm", "语言模型", true, Settings);
    expect(pluginCatalog.get("llm")).toEqual({ id: "llm", name: "语言模型", builtin: true });
    expect(settingsRegistry.get("llm")).toBe(Settings);
  });

  it("replaces settings on hot reload and removes both contributions on unload", () => {
    registerPlugin("viewer.browser", "Browser", false, Settings);
    expect(pluginCatalog.get("viewer.browser")?.builtin).toBe(false);
    expect(settingsRegistry.has("viewer.browser")).toBe(true);

    registerPlugin("viewer.browser", "Browser", false);
    expect(settingsRegistry.has("viewer.browser")).toBe(false);
    unregisterPlugin("viewer.browser");
    expect(pluginCatalog.has("viewer.browser")).toBe(false);
  });
});
