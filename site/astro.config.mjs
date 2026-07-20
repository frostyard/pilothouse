// @ts-check
import { defineConfig } from "astro/config";
import mdx from "@astrojs/mdx";
import { site } from "./src/site.config.ts";

/* Cold code theme matched to --surface-code and the ice/sky palette. */
const frostyardCold = {
  name: "frostyard-cold",
  type: "dark",
  colors: {
    "editor.background": "#061826",
    "editor.foreground": "#a9d8ef"
  },
  settings: [],
  tokenColors: [
    { scope: ["comment", "punctuation.definition.comment"], settings: { foreground: "#5d87a0", fontStyle: "italic" } },
    { scope: ["string", "string.quoted", "string.template"], settings: { foreground: "#8ee3ff" } },
    { scope: ["constant.numeric", "constant.language", "constant.character"], settings: { foreground: "#aee9ff" } },
    { scope: ["keyword", "keyword.control", "storage.type", "storage.modifier"], settings: { foreground: "#47b8ef" } },
    { scope: ["entity.name.function", "support.function"], settings: { foreground: "#8ddbf8" } },
    { scope: ["entity.name.type", "entity.name.tag", "support.type", "support.class"], settings: { foreground: "#c7dce8" } },
    { scope: ["variable", "variable.parameter", "variable.other"], settings: { foreground: "#c7dce8" } },
    { scope: ["punctuation", "meta.brace"], settings: { foreground: "#7799ab" } }
  ]
};

export default defineConfig({
  site: site.url,
  integrations: [mdx()],
  markdown: {
    shikiConfig: { theme: frostyardCold }
  }
});
