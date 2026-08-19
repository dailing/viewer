/**
 * Import-map externals entry (framework section 8.4 stage B): external
 * plugin bundles import bare `vue`; the shell's import map points that
 * specifier at this stable URL. Because this entry and the app bundle are
 * one Rollup build, Vue lives in a shared chunk both import — external
 * plugins get the shell's exact Vue singleton (a second copy would break
 * reactivity).
 */
export * from "vue";
