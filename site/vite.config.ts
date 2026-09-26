import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// Served from https://0x0079.github.io/tingly-shell/, hence the base path.
export default defineConfig({
  base: "/tingly-shell/",
  plugins: [react()],
});
