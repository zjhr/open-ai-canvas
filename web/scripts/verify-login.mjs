/**
 * 登录态渲染验证：用已注册的 admin 账号在真实 Chrome 中登录，确认进入工作台。
 * 复用 scripts/verify-render.mjs 的 CDP 方案。
 */
import { spawn } from "node:child_process";
import { existsSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { createServer } from "node:net";

const CHROME = [
    process.env.CHROME_BIN,
    "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
].filter(Boolean).find((c) => existsSync(c));

const BASE = process.argv[2] || "http://localhost:3000";
const USER = process.argv[3] || "admin";
// 密码从环境变量读取，避免出现在命令行/脚本字面量里
const PASS = process.env.CANVAS_VERIFY_PASSWORD;
if (!CHROME) throw new Error("Chrome not found; set CHROME_BIN");
if (!PASS) throw new Error("set CANVAS_VERIFY_PASSWORD");

const freePort = () =>
    new Promise((res, rej) => {
        const s = createServer();
        s.once("error", rej);
        s.listen(0, "127.0.0.1", () => {
            const p = s.address().port;
            s.close(() => res(p));
        });
    });

const cdpPort = await freePort();
const profile = mkdtempSync(join(tmpdir(), "yingce-login-"));
const chrome = spawn(
    CHROME,
    [
        "--headless=new",
        `--remote-debugging-port=${cdpPort}`,
        `--user-data-dir=${profile}`,
        "--no-first-run",
        "--no-default-browser-check",
        "--window-size=1440,1000",
        "--enable-unsafe-swiftshader",
        "--use-angle=swiftshader",
        "--no-sandbox",
        "--disable-dev-shm-usage",
        "--disable-crash-reporter",
        "--disable-breakpad",
        "about:blank",
    ],
    { stdio: ["ignore", "pipe", "pipe"] },
);

async function waitCdp(ms = 30000) {
    const t0 = Date.now();
    while (Date.now() - t0 < ms) {
        try {
            const r = await fetch(`http://127.0.0.1:${cdpPort}/json/version`);
            if (r.ok) return;
        } catch {}
        await new Promise((r) => setTimeout(r, 300));
    }
    throw new Error("CDP timeout");
}

let code = 0;
try {
    await waitCdp();
    const list = await (await fetch(`http://127.0.0.1:${cdpPort}/json/list`)).json();
    const page = list.find((t) => t.type === "page");
    const ws = new WebSocket(page.webSocketDebuggerUrl);
    await new Promise((r) => ws.addEventListener("open", r, { once: true }));

    let id = 0;
    const pending = new Map();
    const events = [];
    ws.addEventListener("message", (e) => {
        const m = JSON.parse(e.data);
        if (m.id && pending.has(m.id)) {
            const { res, rej } = pending.get(m.id);
            pending.delete(m.id);
            m.error ? rej(new Error(JSON.stringify(m.error))) : res(m.result);
        } else if (m.method) events.push(m);
    });
    const send = (method, params = {}) => {
        const i = ++id;
        ws.send(JSON.stringify({ id: i, method, params }));
        return new Promise((res, rej) => {
            pending.set(i, { res, rej });
            setTimeout(() => pending.has(i) && (pending.delete(i), rej(new Error("timeout " + method))), 60000);
        });
    };
    const evalJs = async (expr) => {
        const r = await send("Runtime.evaluate", { expression: expr, returnByValue: true, awaitPromise: true });
        if (r.exceptionDetails) throw new Error(r.exceptionDetails.text);
        return r.result?.value;
    };

    await send("Runtime.enable");
    await send("Page.enable");
    await send("Network.enable");
    await send("Console.enable");

    // 先导航到站内页面建立 origin / 页面上下文，再调用登录接口
    await send("Page.navigate", { url: `${BASE}/` });
    await new Promise((r) => setTimeout(r, 3000));

    // 调用登录接口，拿到会话 Cookie
    const login = await evalJs(`
      (async () => {
        const r = await fetch("/api/auth/login", {
          method: "POST", credentials: "include",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ username: ${JSON.stringify(USER)}, password: ${JSON.stringify(PASS)} })
        });
        const j = await r.json();
        return { status: r.status, code: j.code, role: j?.data?.user?.role, name: j?.data?.user?.username };
      })()
    `);
    console.log("login      :", JSON.stringify(login));

    await send("Page.navigate", { url: `${BASE}/` });
    await new Promise((r) => setTimeout(r, 1500));

    for (const path of ["/projects", "/settings"]) {
        await send("Page.navigate", { url: `${BASE}${path}` });
        const deadline = Date.now() + 25000;
        let info = null;
        while (Date.now() < deadline) {
            info = await evalJs(
                `(() => ({ url: location.pathname, text: (document.body.innerText||"").trim(), root: (document.getElementById("root")?.innerHTML||"").length }))()`,
            );
            if (info && info.root > 1000 && info.text.length > 20) break;
            await new Promise((r) => setTimeout(r, 600));
        }
        console.log(`\n--- ${path} ---`);
        console.log("  final path :", info?.url);
        console.log("  rootLen    :", info?.root);
        console.log("  sample     :", JSON.stringify((info?.text || "").replace(/\s+/g, " ").slice(0, 180)));
        try {
            const shot = await send("Page.captureScreenshot", { format: "png" });
            writeFileSync(`/tmp/yingce-login-${path.replace(/\//g, "_")}.png`, Buffer.from(shot.data, "base64"));
        } catch {}
    }

    const exc = events.filter((e) => e.method === "Runtime.exceptionThrown");
    console.log("\nexceptions :", exc.length);
    exc.slice(0, 5).forEach((e) => console.log("  " + (e.params?.exceptionDetails?.exception?.description || "")));
    const failed = events.filter((e) => e.method === "Network.loadingFailed");
    console.log("failedReq  :", failed.length);
    ws.close();
} catch (err) {
    console.error("FAIL:", err.message);
    code = 1;
} finally {
    chrome.kill("SIGKILL");
    try {
        rmSync(profile, { recursive: true, force: true });
    } catch {}
}
process.exit(code);
