/**
 * 安全上下文兼容的摘要与互斥工具。
 *
 * 三个 API 只在「安全上下文」可用：`crypto.subtle`、`navigator.locks` 与
 * `crypto.randomUUID`。生产部署通常是 HTTPS，没问题；但局域网 HTTP
 * （`http://192.168.x.x:3000`）不是安全上下文，这些 API 会直接缺失。
 *
 * 这里的降级都保证**结果完全相同**，不会因为换了环境就让持久化 ID 变化：
 * - 摘要：优先 Web Crypto，缺失时用纯 JS SHA-256。
 * - 标签互斥：优先 BroadcastChannel 做主从选举，缺失时退化为进程内队列。
 */

const HEX = Array.from({ length: 256 }, (_, index) => index.toString(16).padStart(2, "0"));

function toHex(bytes: Uint8Array) {
    let out = "";
    for (let index = 0; index < bytes.length; index += 1) out += HEX[bytes[index]];
    return out;
}

// ── 纯 JS SHA-256（FIPS 180-4）──────────────────────────────────────────────
// 只在缺少 Web Crypto 时使用；与 Web Crypto 输出逐字节一致。
const SHA256_K = new Uint32Array([
    0x428a2f98, 0x71374491, 0xb5c0fbcf, 0xe9b5dba5, 0x3956c25b, 0x59f111f1, 0x923f82a4, 0xab1c5ed5, 0xd807aa98, 0x12835b01, 0x243185be, 0x550c7dc3,
    0x72be5d74, 0x80deb1fe, 0x9bdc06a7, 0xc19bf174, 0xe49b69c1, 0xefbe4786, 0x0fc19dc6, 0x240ca1cc, 0x2de92c6f, 0x4a7484aa, 0x5cb0a9dc, 0x76f988da,
    0x983e5152, 0xa831c66d, 0xb00327c8, 0xbf597fc7, 0xc6e00bf3, 0xd5a79147, 0x06ca6351, 0x14292967, 0x27b70a85, 0x2e1b2138, 0x4d2c6dfc, 0x53380d13,
    0x650a7354, 0x766a0abb, 0x81c2c92e, 0x92722c85, 0xa2bfe8a1, 0xa81a664b, 0xc24b8b70, 0xc76c51a3, 0xd192e819, 0xd6990624, 0xf40e3585, 0x106aa070,
    0x19a4c116, 0x1e376c08, 0x2748774c, 0x34b0bcb5, 0x391c0cb3, 0x4ed8aa4a, 0x5b9cca4f, 0x682e6ff3, 0x748f82ee, 0x78a5636f, 0x84c87814, 0x8cc70208,
    0x90befffa, 0xa4506ceb, 0xbef9a3f7, 0xc67178f2,
]);

function sha256Fallback(bytes: Uint8Array): Uint8Array {
    const h = new Uint32Array([0x6a09e667, 0xbb67ae85, 0x3c6ef372, 0xa54ff53a, 0x510e527f, 0x9b05688c, 0x1f83d9ab, 0x5be0cd19]);
    const totalLength = bytes.length;
    // 补位：0x80 + 若干 0x00，使总长 ≡ 56 (mod 64)，末尾 8 字节存原始比特长度。
    const paddedLength = ((totalLength + 8) >> 6 << 6) + 64;
    const buffer = new Uint8Array(paddedLength);
    buffer.set(bytes);
    buffer[totalLength] = 0x80;
    const view = new DataView(buffer.buffer);
    const bitLength = totalLength * 8;
    // 长度字段是大端 64 位；高 32 位对能放进内存的输入基本恒为 0，但仍按位写全。
    view.setUint32(paddedLength - 8, Math.floor(bitLength / 0x100000000));
    view.setUint32(paddedLength - 4, bitLength >>> 0);

    const w = new Uint32Array(64);
    for (let offset = 0; offset < paddedLength; offset += 64) {
        for (let index = 0; index < 16; index += 1) w[index] = view.getUint32(offset + index * 4);
        for (let index = 16; index < 64; index += 1) {
            const s0 = rotr(w[index - 15], 7) ^ rotr(w[index - 15], 18) ^ (w[index - 15] >>> 3);
            const s1 = rotr(w[index - 2], 17) ^ rotr(w[index - 2], 19) ^ (w[index - 2] >>> 10);
            w[index] = (w[index - 16] + s0 + w[index - 7] + s1) >>> 0;
        }
        let [a, b, c, d, e, f, g, hh] = h;
        for (let index = 0; index < 64; index += 1) {
            const s1 = rotr(e, 6) ^ rotr(e, 11) ^ rotr(e, 25);
            const ch = (e & f) ^ (~e & g);
            const temp1 = (hh + s1 + ch + SHA256_K[index] + w[index]) >>> 0;
            const s0 = rotr(a, 2) ^ rotr(a, 13) ^ rotr(a, 22);
            const maj = (a & b) ^ (a & c) ^ (b & c);
            const temp2 = (s0 + maj) >>> 0;
            hh = g;
            g = f;
            f = e;
            e = (d + temp1) >>> 0;
            d = c;
            c = b;
            b = a;
            a = (temp1 + temp2) >>> 0;
        }
        h[0] = (h[0] + a) >>> 0;
        h[1] = (h[1] + b) >>> 0;
        h[2] = (h[2] + c) >>> 0;
        h[3] = (h[3] + d) >>> 0;
        h[4] = (h[4] + e) >>> 0;
        h[5] = (h[5] + f) >>> 0;
        h[6] = (h[6] + g) >>> 0;
        h[7] = (h[7] + hh) >>> 0;
    }
    const digest = new Uint8Array(32);
    const out = new DataView(digest.buffer);
    for (let index = 0; index < 8; index += 1) out.setUint32(index * 4, h[index]);
    return digest;
}

function rotr(value: number, shift: number) {
    return (value >>> shift) | (value << (32 - shift));
}

/** SHA-256 是否存在真实的 Web Crypto 实现（非安全上下文里可能缺失）。 */
export function hasWebCryptoDigest() {
    return typeof globalThis.crypto?.subtle?.digest === "function";
}

/** 计算 SHA-256，返回原始字节。两套实现结果完全一致。 */
export async function sha256Bytes(bytes: Uint8Array): Promise<Uint8Array> {
    if (hasWebCryptoDigest()) {
        return new Uint8Array(await globalThis.crypto.subtle.digest("SHA-256", bytes as unknown as BufferSource));
    }
    return sha256Fallback(bytes);
}

/** 计算 SHA-256，返回小写十六进制。 */
export async function sha256HexOfBytes(bytes: Uint8Array): Promise<string> {
    return toHex(await sha256Bytes(bytes));
}

/** 对文本按 UTF-8 计算 SHA-256 小写十六进制。 */
export async function sha256Hex(text: string): Promise<string> {
    return sha256HexOfBytes(new TextEncoder().encode(text));
}

// ── 跨标签互斥 ─────────────────────────────────────────────────────────────
/**
 * 在浏览器提供 `navigator.locks` 时用它做跨标签互斥；
 * 非安全上下文（局域网 HTTP）里改用 `localStorage` 租约锁。
 *
 * 为什么不用「干脆不加锁」：素材目录是「读整份 → 改 → 写整份」的模式，
 * 两个标签页同时做这件事会丢掉其中一方刚写入的素材。所以即使失去 Web Locks，
 * 也要换一个还能用的互斥手段，而不是把保护直接删掉。
 *
 * localStorage 在本项目的目标环境（局域网 HTTP）里是可用的，而 Web Locks 不是。
 * 租约锁是「写入 → 读回自证 → 静置后复查」的乐观方案：它没有原子 CAS，
 * 靠复查把竞争窗口压到毫秒级，并靠过期时间避免崩溃的标签页永久占锁。
 */
export type NamedLockManager = {
    request<T>(name: string, callback: () => Promise<T>): Promise<T>;
    /** 该实现能否跨标签页互斥。false 表示只剩同标签内串行。 */
    crossRealm: boolean;
};

const LEASE_STORAGE_PREFIX = "yingce:secure-context-lock:";
// 租约长于任何一次目录写入，但短到崩溃后能自动恢复。
const LEASE_TTL_MS = 15_000;
// 写入后等一小段再复查，用来暴露「刚写完就被别人覆盖」的竞争。
const LEASE_SETTLE_MS = 12;
const LEASE_WAIT_TIMEOUT_MS = 10_000;

function leaseStorage(): Storage | null {
    try {
        const storage = typeof window !== "undefined" ? window.localStorage : undefined;
        if (!storage) return null;
        // Safari 隐私模式会抛异常而不是返回 null。
        const probe = `${LEASE_STORAGE_PREFIX}probe`;
        storage.setItem(probe, "1");
        storage.removeItem(probe);
        return storage;
    } catch {
        return null;
    }
}

function leaseToken() {
    const random = globalThis.crypto?.randomUUID?.() ?? `${Date.now()}-${Math.random().toString(36).slice(2)}`;
    return `${random}:${Math.random().toString(36).slice(2)}`;
}

function sleep(ms: number) {
    return new Promise<void>((resolve) => setTimeout(resolve, ms));
}

async function requestWithLease<T>(storage: Storage, name: string, callback: () => Promise<T>): Promise<T> {
    const key = `${LEASE_STORAGE_PREFIX}${name}`;
    const token = leaseToken();
    const deadline = Date.now() + LEASE_WAIT_TIMEOUT_MS;

    while (Date.now() < deadline) {
        const now = Date.now();
        let holder: { token?: string; expiresAt?: number } | null = null;
        try {
            const raw = storage.getItem(key);
            holder = raw ? (JSON.parse(raw) as { token?: string; expiresAt?: number }) : null;
        } catch {
            holder = null;
        }
        // 只有「没人持有」或「租约已过期」才能尝试接管。
        if (!holder || typeof holder.expiresAt !== "number" || holder.expiresAt <= now) {
            try {
                storage.setItem(key, JSON.stringify({ token, expiresAt: now + LEASE_TTL_MS }));
            } catch {
                // 配额或权限异常：退回直接执行，让上层看到真实错误。
                return callback();
            }
            // 静置后复查：如果这段时间里被别的标签页覆盖，就说明我们没抢到。
            await sleep(LEASE_SETTLE_MS);
            let confirmed: string | undefined;
            try {
                const raw = storage.getItem(key);
                confirmed = raw ? (JSON.parse(raw) as { token?: string }).token : undefined;
            } catch {
                confirmed = undefined;
            }
            if (confirmed === token) {
                try {
                    return await callback();
                } finally {
                    // 只释放自己持有的租约，绝不误删别人刚抢到的锁。
                    try {
                        const raw = storage.getItem(key);
                        if (raw && (JSON.parse(raw) as { token?: string }).token === token) storage.removeItem(key);
                    } catch {
                        // 释放失败由 TTL 兜底。
                    }
                }
            }
        }
        await sleep(8 + Math.floor(Math.random() * 17));
    }
    throw new Error("等待其他标签页释放存储锁超时，请稍后重试（关闭多余的本站标签页通常可以解决）");
}

export function namedLockManager(): NamedLockManager {
    const locks = typeof window !== "undefined" && typeof navigator !== "undefined" ? navigator.locks : undefined;
    if (locks && typeof locks.request === "function") {
        return { request: (name, callback) => locks.request(name, callback), crossRealm: true };
    }
    const storage = leaseStorage();
    if (storage) {
        return { request: (name, callback) => requestWithLease(storage, name, callback), crossRealm: true };
    }
    // 连 localStorage 都不可用（极端隐私模式）：只剩同标签内串行。
    return { request: (_name, callback) => callback(), crossRealm: false };
}
