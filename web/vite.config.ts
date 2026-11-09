import { dirname, resolve } from "node:path";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import react from "@vitejs/plugin-react";
import { defineConfig } from "vite";

const webDir = dirname(fileURLToPath(import.meta.url));
const appVersion = process.env.CANVAS_BUILD_VERSION?.trim() || readFileSync(resolve(webDir, "../VERSION"), "utf8").trim();
const buildCommit = process.env.CANVAS_BUILD_COMMIT?.trim() || process.env.VITE_BUILD_COMMIT?.trim() || "unknown";
const buildTime = process.env.CANVAS_BUILD_TIME?.trim() || process.env.VITE_BUILD_TIME?.trim() || "unknown";
const appChangelog = readFileSync(resolve(webDir, "../CHANGELOG.md"), "utf8");
const apiProxyTarget = process.env.VITE_API_PROXY_TARGET?.trim() || "http://127.0.0.1:8080";

// Vite 8 的 oxc 在 serve 模式下会向 .tsx 注入 Fast Refresh 钩子（$RefreshReg$ / $RefreshSig$）。
// @vitejs/plugin-react 6 在本项目配置（optimizeDeps + 非 bundledDev）下没有把对应的
// React Refresh 运行时前置脚本注入 index.html，导致首个 .tsx 模块抛
// "ReferenceError: $RefreshReg$ is not defined"，React 无法挂载，页面空白。
// 这里显式补上官方前置脚本；生产构建（command === "build"）不注入，行为不变。
const reactRefreshPreamble = () => ({
    name: "canvas:react-refresh-preamble",
    apply: "serve" as const,
    transformIndexHtml(html: string) {
        if (html.includes("vite_plugin_react_preamble_installed__")) return html;
        const preamble = `    <script type="module">
import RefreshRuntime from "/@react-refresh";
RefreshRuntime.injectIntoGlobalHook(window);
window.$RefreshReg$ = () => {};
window.$RefreshSig$ = () => (type) => type;
window.__vite_plugin_react_preamble_installed__ = true;
</script>
`;
        return html.replace("</head>", `${preamble}  </head>`);
    },
});

export default defineConfig({
    plugins: [react(), reactRefreshPreamble()],
    define: {
        __APP_VERSION__: JSON.stringify(appVersion),
        __APP_CHANGELOG__: JSON.stringify(appChangelog),
        "import.meta.env.VITE_APP_VERSION": JSON.stringify(appVersion),
        "import.meta.env.VITE_BUILD_COMMIT": JSON.stringify(buildCommit),
        "import.meta.env.VITE_BUILD_TIME": JSON.stringify(buildTime),
    },
    server: {
        proxy: {
            "/api": {
                target: apiProxyTarget,
                changeOrigin: true,
                xfwd: true,
                ws: true,
            },
            "/oauth/linuxdo/callback": {
                target: apiProxyTarget,
                changeOrigin: true,
                xfwd: true,
            },
        },
    },
    resolve: {
        alias: {
            "@": resolve(webDir, "src"),
        },
    },
    build: {
        rolldownOptions: {
            output: {
                strictExecutionOrder: true,
                codeSplitting: {
                    // Keep route-level lazy imports isolated. Recursively merging dependencies
                    // pulls unrelated pages into the initial modulepreload graph.
                    includeDependenciesRecursively: false,
                    minSize: 40 * 1024,
                    groups: [
                        {
                            // Shared interop helpers must not be emitted into a route entry:
                            // AntD would import that entry back and execute its bootstrap early.
                            name: "vendor-babel-runtime",
                            minSize: 0,
                            test: /node_modules[\\/]@babel[\\/]runtime[\\/]/,
                            priority: 40,
                        },
                        {
                            name: "vendor-react",
                            test: /node_modules[\\/](?:react(?:-dom|-router|-router-dom)?|scheduler|zustand|use-sync-external-store|@tanstack[\\/](?:query-core|react-query))[\\/]/,
                            priority: 30,
                        },
                        {
                            name: "vendor-icons",
                            test: /node_modules[\\/](?:lucide-react|@ant-design[\\/]icons)[\\/]/,
                            priority: 20,
                            entriesAware: true,
                            entriesAwareMergeThreshold: 48 * 1024,
                        },
                        {
                            name: "vendor-antd",
                            test: /node_modules[\\/](?:antd|@ant-design|@rc-component|rc-[^\\/]+|dayjs)[\\/]/,
                            priority: 10,
                            entriesAware: true,
                            entriesAwareMergeThreshold: 80 * 1024,
                        },
                    ],
                },
            },
        },
    },
});
