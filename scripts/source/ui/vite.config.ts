import { defineConfig } from "vite";
import { viteSingleFile } from "vite-plugin-singlefile";

export default defineConfig({
  plugins: [viteSingleFile()],
  build: {
    target: "es2022",
    cssMinify: true,
    minify: "oxc",
    outDir: "dist",
    emptyOutDir: true,
    rollupOptions: {
      input: "panel.html",
    },
  },
});
