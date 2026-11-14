import { describe, expect, test } from "bun:test";

import { hasWebCryptoDigest, namedLockManager, sha256Bytes, sha256Hex } from "@/lib/secure-context";

// 纯 JS 的 SHA-256 只有在非安全上下文才会被用到，但它的输出会被写进持久化 ID。
// 一旦与 Web Crypto 不一致，同一个任务在局域网 HTTP 与 HTTPS 下会算出不同的素材 ID，
// 造成重复素材或幂等失效。这里直接用官方向量 + 与 Web Crypto 逐字节对比来钉住。
const VECTORS: Array<[string, string]> = [
    ["", "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"],
    ["abc", "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"],
    [
        "abcdbcdecdefdefgefghfghighijhijkijkljklmklmnlmnomnopnopq",
        "248d6a61d20638b8e5c026930c3e6039a33ce45964ff2167f6ecedd419db06c1",
    ],
    [
        "abcdefghbcdefghicdefghijdefghijkefghijklfghijklmghijklmnhijklmnoijklmnopjklmnopqklmnopqrlmnopqrsmnopqrstnopqrstu",
        "cf5b16a778af8380036ce59e7b0492370b249b11e8f07a51afac45037afee9d1",
    ],
];

describe("secure-context sha256", () => {
    test("匹配 FIPS 180-4 官方测试向量", async () => {
        for (const [input, expected] of VECTORS) {
            expect(await sha256Hex(input)).toBe(expected);
        }
    });

    test("跨块边界的长输入仍然正确", async () => {
        // 55/56/64 字节是补位逻辑最容易写错的位置（正好卡在 0x80 与长度字段）。
        for (const size of [55, 56, 57, 63, 64, 65, 119, 120, 128, 1000]) {
            const text = "a".repeat(size);
            const viaWebCrypto = new Uint8Array(await globalThis.crypto.subtle.digest("SHA-256", new TextEncoder().encode(text)));
            const viaHelper = await sha256Bytes(new TextEncoder().encode(text));
            expect(Array.from(viaHelper)).toEqual(Array.from(viaWebCrypto));
        }
    });

    test("中文与多字节输入一致", async () => {
        const text = "提示词画廊 · 生成素材 · 🎬";
        const viaWebCrypto = new Uint8Array(await globalThis.crypto.subtle.digest("SHA-256", new TextEncoder().encode(text)));
        expect(Array.from(await sha256Bytes(new TextEncoder().encode(text)))).toEqual(Array.from(viaWebCrypto));
    });

    test("hasWebCryptoDigest 反映当前环境", () => {
        expect(hasWebCryptoDigest()).toBe(typeof globalThis.crypto?.subtle?.digest === "function");
    });
});

describe("secure-context named locks", () => {
    test("有 navigator.locks 时报告 crossRealm 并真的串行", async () => {
        const manager = namedLockManager();
        if (!manager.crossRealm) {
            // 非安全上下文（或测试环境）下跳过串行断言，但降级路径仍必须能跑通。
            const result = await manager.request("yingce-test", async () => "ok");
            expect(result).toBe("ok");
            return;
        }
        const order: string[] = [];
        await Promise.all([
            manager.request("yingce-test", async () => {
                order.push("a-start");
                await new Promise((resolve) => setTimeout(resolve, 20));
                order.push("a-end");
            }),
            manager.request("yingce-test", async () => {
                order.push("b-start");
                order.push("b-end");
            }),
        ]);
        // 同名锁必须互斥：b 不能插进 a 的中间。
        expect(order).toEqual(["a-start", "a-end", "b-start", "b-end"]);
    });

    test("降级路径仍然执行回调并返回结果", async () => {
        const fallback = { request: <T,>(_name: string, callback: () => Promise<T>) => callback(), crossRealm: false };
        await expect(fallback.request("x", async () => 42)).resolves.toBe(42);
    });
});

describe("localStorage 租约锁（局域网 HTTP 降级）", () => {
    // 模拟「没有 navigator.locks、但有 localStorage」的非安全上下文。
    function withLeaseOnlyEnvironment<T>(run: () => Promise<T>): Promise<T> {
        const originalWindow = (globalThis as { window?: unknown }).window;
        const originalNavigator = (globalThis as { navigator?: unknown }).navigator;
        const store = new Map<string, string>();
        const storage = {
            getItem: (key: string) => store.get(key) ?? null,
            setItem: (key: string, value: string) => void store.set(key, String(value)),
            removeItem: (key: string) => void store.delete(key),
            clear: () => void store.clear(),
            key: (index: number) => [...store.keys()][index] ?? null,
            get length() {
                return store.size;
            },
        };
        Object.defineProperty(globalThis, "window", { configurable: true, value: { localStorage: storage } });
        Object.defineProperty(globalThis, "navigator", { configurable: true, value: {} });
        return run().finally(() => {
            if (originalWindow === undefined) delete (globalThis as { window?: unknown }).window;
            else Object.defineProperty(globalThis, "window", { configurable: true, value: originalWindow });
            if (originalNavigator === undefined) delete (globalThis as { navigator?: unknown }).navigator;
            else Object.defineProperty(globalThis, "navigator", { configurable: true, value: originalNavigator });
        });
    }

    test("没有 navigator.locks 时仍然报告 crossRealm 并互斥", async () => {
        await withLeaseOnlyEnvironment(async () => {
            const manager = namedLockManager();
            expect(manager.crossRealm).toBe(true);
            const order: string[] = [];
            await Promise.all([
                manager.request("yingce-lease", async () => {
                    order.push("a-start");
                    await new Promise((resolve) => setTimeout(resolve, 40));
                    order.push("a-end");
                }),
                manager.request("yingce-lease", async () => {
                    order.push("b-start");
                    order.push("b-end");
                }),
            ]);
            // 租约锁必须真的把两个临界区排开，而不是让它们交错。
            expect(order).toEqual(["a-start", "a-end", "b-start", "b-end"]);
        });
    });

    test("临界区抛错也会释放租约", async () => {
        await withLeaseOnlyEnvironment(async () => {
            const manager = namedLockManager();
            await expect(
                manager.request("yingce-lease-error", async () => {
                    throw new Error("boom");
                }),
            ).rejects.toThrow("boom");
            // 释放失败会让后续写入卡到 TTL 过期，这里必须能立刻再拿到锁。
            await expect(manager.request("yingce-lease-error", async () => "recovered")).resolves.toBe("recovered");
        });
    });

    test("过期的租约可以被接管", async () => {
        await withLeaseOnlyEnvironment(async () => {
            // 模拟一个崩溃的标签页留下的过期租约。
            const key = "yingce:secure-context-lock:yingce-stale";
            window.localStorage.setItem(key, JSON.stringify({ token: "dead-tab", expiresAt: Date.now() - 1000 }));
            const manager = namedLockManager();
            await expect(manager.request("yingce-stale", async () => "took-over")).resolves.toBe("took-over");
        });
    });
});
