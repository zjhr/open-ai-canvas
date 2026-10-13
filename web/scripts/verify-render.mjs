/**
 * 本地安装验证：用真实 Chrome（沿用项目 previs E2E 的 CDP 方案）打开影策首页，
 * 断言 SPA 真正挂载、渲染出可见内容，并截图留证。
 *
 * 用法：bun scripts/verify-render.mjs [url]
 */
import { spawn } from "node:child_process";
import { existsSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { createServer } from "node:net";

const CHROME_CANDIDATES = [
    process.env.CHROME_BIN,
    "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
    "/Applications/Chromium.app/Contents/MacOS/Chromium",
    "/usr/bin/google-chrome",
    "/usr/bin/chromium",
].filter(Boolean);

const url = process.argv[2] || "http://localhost:3000/";
const chromePath = CHROME_CANDIDATES.find((c) => existsSync(c));
if (!chromePath) throw new Error("No Chrome binary found; set CHROME_BIN");

const cdpPort = await freePort();
const profileDir = mkdtempSync(join(tmpdir(), "yingce-verify-"));
const args = [
    "--headless=new",
    `--remote-debugging-port=${cdpPort}`,
    `--user-data-dir=${profileDir}`,
    "--no-first-run",
    "--no-default-browser-check",
    "--window-size=1440,1000",
    "--enable-unsafe-swiftshader",
    "--use-angle=swiftshader",
    // 本机沙箱内 Chrome 无法初始化自带沙箱（Operation not permitted），
    // 且 Crashpad 目录不可写；仅本地验证用，不影响应用本身。
    "--no-sandbox",
    "--disable-dev-shm-usage",
    "--disable-crash-reporter",
    "--disable-breakpad",
    "--crash-dumps-dir=/tmp/yingce-chrome-crash",
    "about:blank",
];

function freePort() {
    return new Promise((resolve, reject) => {
        const srv = createServer();
        srv.once("error", reject);
        srv.listen(0, "127.0.0.1", () => {
            const { port } = srv.address();
            srv.close(() => resolve(port));
        });
    });
}

async function waitForCdp(port, deadlineMs = 30000) {
    const start = Date.now();
    while (Date.now() - start < deadlineMs) {
        try {
            const res = await fetch(`http://127.0.0.1:${port}/json/version`);
            if (res.ok) return await res.json();
        } catch {}
        await new Promise((r) => setTimeout(r, 300));
    }
    throw new Error("CDP did not become ready");
}

class Cdp {
    constructor(ws) {
        this.ws = ws;
        this.id = 0;
        this.pending = new Map();
        this.events = [];
        ws.addEventListener("message", (ev) => {
            const msg = JSON.parse(ev.data);
            if (msg.id && this.pending.has(msg.id)) {
                const { resolve, reject } = this.pending.get(msg.id);
                this.pending.delete(msg.id);
                msg.error ? reject(new Error(JSON.stringify(msg.error))) : resolve(msg.result);
            } else if (msg.method) {
                this.events.push(msg);
            }
        });
    }
    send(method, params = {}) {
        const id = ++this.id;
        this.ws.send(JSON.stringify({ id, method, params }));
        return new Promise((resolve, reject) => {
            this.pending.set(id, { resolve, reject });
            setTimeout(() => {
                if (this.pending.has(id)) {
                    this.pending.delete(id);
                    reject(new Error(`CDP timeout: ${method}`));
                }
            }, 60000);
        });
    }
    async evaluate(expression) {
        const r = await this.send("Runtime.evaluate", {
            expression,
            returnByValue: true,
            awaitPromise: true,
        });
        if (r.exceptionDetails) throw new Error(r.exceptionDetails.text || "evaluate failed");
        return r.result?.value;
    }
}

const chrome = spawn(chromePath, args, { stdio: ["ignore", "pipe", "pipe"] });
let chromeErr = "";
chrome.stderr.on("data", (d) => (chromeErr += d.toString()));

let exitCode = 0;
try {
    await waitForCdp(cdpPort);

    const targets = await (await fetch(`http://127.0.0.1:${cdpPort}/json/list`)).json();
    const page = targets.find((t) => t.type === "page");
    const ws = new WebSocket(page.webSocketDebuggerUrl);
    await new Promise((r) => ws.addEventListener("open", r, { once: true }));

    const cdp = new Cdp(ws);
    await cdp.send("Runtime.enable");
    await cdp.send("Page.enable");
    await cdp.send("Console.enable");
    await cdp.send("Log.enable");
    await cdp.send("Network.enable");

    await cdp.send("Page.navigate", { url });
    // 等待 SPA 挂载出非空内容
    const deadline = Date.now() + 30000;
    let rendered = false;
    while (Date.now() < deadline) {
        const state = await cdp.evaluate(
            `(() => { const r = document.getElementById("root"); return { len: r ? r.innerHTML.length : 0, text: (document.body.innerText || "").trim().length, title: document.title }; })()`,
        );
        if (state && state.len > 500 && state.text > 0) {
            rendered = true;
            break;
        }
        await new Promise((r) => setTimeout(r, 700));
    }

    const detail = await cdp.evaluate(
        `(() => { const r = document.getElementById("root"); return { rootLen: r ? r.innerHTML.length : 0, textLen: (document.body.innerText||"").trim().length, title: document.title, sample: (document.body.innerText||"").trim().slice(0,300) }; })()`,
    );

    const failed = cdp.events
        .filter((e) => e.method === "Network.loadingFailed")
        .map((e) => e.params?.errorText)
        .filter(Boolean);
    const consoleErrors = cdp.events
        .filter((e) => e.method === "Runtime.consoleAPICalled" && e.params?.type === "error")
        .map((e) => (e.params.args || []).map((a) => a.value ?? a.description ?? "").join(" "));

    console.log("url        :", url);
    console.log("chrome     :", chromePath);
    console.log("title      :", detail?.title);
    console.log("rootLen    :", detail?.rootLen);
    console.log("textLen    :", detail?.textLen);
    console.log("sample     :", JSON.stringify((detail?.sample || "").slice(0, 200)));
    console.log("consoleErr :", consoleErrors.length);
    console.log("failedReq  :", failed.length, failed.slice(0, 5));

    // 诊断：所有 console 消息、未捕获异常、日志条目
    const allConsole = cdp.events
        .filter((e) => e.method === "Runtime.consoleAPICalled")
        .map((e) => `[${e.params.type}] ` + (e.params.args || []).map((a) => a.value ?? a.description ?? JSON.stringify(a.preview?.properties ?? "")).join(" "));
    const exceptions = cdp.events
        .filter((e) => e.method === "Runtime.exceptionThrown")
        .map((e) => e.params?.exceptionDetails?.exception?.description || e.params?.exceptionDetails?.text || "unknown");
    const logEntries = cdp.events
        .filter((e) => e.method === "Log.entryAdded")
        .map((e) => `[${e.params.entry.level}] ${e.params.entry.text}`);
    const reqs = cdp.events.filter((e) => e.method === "Network.requestWillBeSent").map((e) => e.params.request.url);
    const responses = cdp.events.filter((e) => e.method === "Network.responseReceived").map((e) => `${e.params.response.status} ${e.params.response.url}`);

    console.log("--- console (" + allConsole.length + ") ---");
    allConsole.slice(0, 25).forEach((l) => console.log("  " + l));
    console.log("--- exceptions (" + exceptions.length + ") ---");
    exceptions.slice(0, 10).forEach((l) => console.log("  " + l));
    console.log("--- log entries (" + logEntries.length + ") ---");
    logEntries.slice(0, 20).forEach((l) => console.log("  " + l));
    console.log("--- requests (" + reqs.length + ") ---");
    reqs.slice(0, 20).forEach((l) => console.log("  " + l));
    console.log("--- non-2xx responses ---");
    responses.filter((r) => !/^2|^3/.test(r)).slice(0, 20).forEach((l) => console.log("  " + l));

    // 截图留证
    try {
        const shot = await cdp.send("Page.captureScreenshot", { format: "png" });
        writeFileSync("/tmp/yingce-render.png", Buffer.from(shot.data, "base64"));
        console.log("screenshot : /tmp/yingce-render.png");
    } catch (e) {
        console.log("screenshot : failed", e.message);
    }

    if (!rendered) {
        console.error("RESULT: FAIL — SPA did not render visible content");
        exitCode = 1;
    } else {
        console.log("RESULT: PASS — SPA mounted and rendered visible content");
    }
    ws.close();
} catch (err) {
    console.error("RESULT: FAIL —", err.message);
    console.error(chromeErr.split("\n").slice(-15).join("\n"));
    exitCode = 1;
} finally {
    chrome.kill("SIGKILL");
    try {
        rmSync(profileDir, { recursive: true, force: true });
    } catch {}
}
process.exit(exitCode);
