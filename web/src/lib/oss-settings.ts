export type OSSProvider = "aliyun" | "tencent" | "qiniu" | "volcengine" | "s3" | "gdrive";

export type S3Preset = "aws" | "r2" | "b2" | "rustfs" | "custom";

export const DEFAULT_OSS_PATH_PREFIX = "open-ai-canvas";

export type OSSConnectionTestInput = {
    provider: OSSProvider;
    s3Preset?: S3Preset;
    region: string;
    endpoint: string;
    cdnBaseUrl: string;
    bucket: string;
    accessKeyId: string;
    accessKeySecret?: string;
    sessionToken?: string;
    pathPrefix: string;
    pathStyle?: boolean;
    gdriveClientId?: string;
    gdriveClientSecret?: string;
    gdriveRefreshToken?: string;
    gdriveRootFolderId?: string;
};

export type OSSConnectionTestDraft = Omit<Partial<OSSConnectionTestInput>, "provider"> & Pick<OSSConnectionTestInput, "provider">;

export type OSSConnectionTestResult = {
    ok: boolean;
    message?: string;
    testedAt?: string;
    testedDigest?: string;
};

export const S3_PRESET_OPTIONS: Array<{ label: string; value: S3Preset }> = [
    { label: "AWS S3", value: "aws" },
    { label: "Cloudflare R2", value: "r2" },
    { label: "Backblaze B2", value: "b2" },
    { label: "RustFS", value: "rustfs" },
    { label: "自定义", value: "custom" },
];

const S3_PRESET_HINTS: Record<S3Preset, { region: string; endpoint: string; help: string }> = {
    aws: {
        region: "us-east-1",
        endpoint: "https://s3.us-east-1.amazonaws.com",
        help: "填写 AWS 区域对应的 S3 服务根 URL。",
    },
    r2: {
        region: "auto",
        endpoint: "https://<account-id>.r2.cloudflarestorage.com",
        help: "Endpoint 使用 R2 控制台提供的账户级 S3 API 根 URL。",
    },
    b2: {
        region: "us-west-004",
        endpoint: "https://s3.us-west-004.backblazeb2.com",
        help: "Region 和 Endpoint 应与 Backblaze B2 Bucket 所在区域一致。",
    },
    rustfs: {
        region: "us-east-1",
        endpoint: "http://127.0.0.1:9000",
        help: "填写后端可访问的 RustFS S3 服务根 URL。",
    },
    custom: {
        region: "us-east-1",
        endpoint: "https://s3.example.com",
        help: "填写兼容 S3 API 的服务根 URL，不要包含 Bucket 或对象路径。",
    },
};

const CONNECTION_FIELDS = new Set([
    "provider",
    "s3Preset",
    "region",
    "endpoint",
    "cdnBaseUrl",
    "bucket",
    "accessKeyId",
    "accessKeySecret",
    "sessionToken",
    "pathPrefix",
    "pathStyle",
    "gdriveClientId",
    "gdriveClientSecret",
    "gdriveRefreshToken",
    "gdriveRootFolderId",
]);

// Google Drive 的四个字段提示：Drive 只有 OAuth 凭据与一个落点，没有 Bucket/Endpoint/Region。
export const GDRIVE_ROOT_FOLDER_PLACEHOLDER = "留空 = 我的云端硬盘根目录";

export const GDRIVE_FIELD_HINTS = {
    clientId: "在 Google Cloud Console 创建 OAuth 客户端（类型选桌面应用）后取得；它与客户端密钥共同代表本次授权。",
    clientSecret: "与客户端 ID 配套；后端只保存密文，留空表示不修改。",
    refreshToken: "用同一客户端完成一次离线授权取得（access_type=offline 且 prompt=consent）；它等同于该账号 Drive 的长期读写权限，请在 Google 账号的第三方授权页面撤销泄露的令牌。",
    rootFolderId: "可留空：留空表示写入「我的云端硬盘」根目录；填写时取文件夹链接 /folders/<ID> 的最后一段。",
} as const;

// 火山引擎 TOS 官方地域，见 https://docs.volcengine.com/docs/6349/107356 。
export const VOLCENGINE_TOS_REGION_HINT = "cn-beijing、cn-shanghai、cn-guangzhou、cn-hongkong、ap-southeast-1、ap-southeast-3";

const VOLCENGINE_TOS_HOST_PATTERN = /^(?:([a-z0-9][a-z0-9-]*)\.)?tos-(?:s3-)?([a-z0-9-]+)\.(volces\.com|ivolces\.com)$/;
const VOLCENGINE_TOS_REGION_PATTERN = /^[a-z0-9]+(?:-[a-z0-9]+)+$/;

/** 与后端 ValidateVolcengineTOSLocation 一致：只接受官方 tos-s3 域名和形如 cn-beijing 的地域。 */
export function volcengineTOSLocationError(region: string, endpoint: string) {
    if (region && !VOLCENGINE_TOS_REGION_PATTERN.test(region)) return "火山引擎 TOS Region 格式不正确，例如 cn-beijing";
    let hostname = "";
    try {
        hostname = new URL(endpoint).hostname;
    } catch {
        return "火山引擎 TOS Endpoint 格式不正确";
    }
    if (!VOLCENGINE_TOS_HOST_PATTERN.test(hostname)) return "火山引擎 TOS 仅支持官方 Endpoint（tos-s3-<地域>.volces.com），其他服务请选择 S3 兼容存储";
    return "";
}

/**
 * 与后端 storage.normalizeVolcengineTOSLocation 保持一致：Region 生成 S3 兼容 Endpoint，
 * 控制台的原生域名或 Bucket 域名换算为同地域 tos-s3 服务根地址；无法确定的输入保持原值，由后端拒绝。
 */
export function normalizeVolcengineTOSLocation(input: { region: string; endpoint: string; bucket: string }) {
    let region = input.region.toLowerCase();
    let endpoint = input.endpoint;
    if (!endpoint) return { region, endpoint: VOLCENGINE_TOS_REGION_PATTERN.test(region) ? `https://tos-s3-${region}.volces.com` : "" };
    if (!endpoint.includes("://")) endpoint = `https://${endpoint}`;
    let parsed: URL;
    try {
        parsed = new URL(endpoint);
    } catch {
        return { region, endpoint };
    }
    const match = VOLCENGINE_TOS_HOST_PATTERN.exec(parsed.hostname.replace(/\.$/, ""));
    if (!match || (match[1] && match[1] !== input.bucket.toLowerCase())) return { region, endpoint };
    if (!region) region = match[2];
    const host = `tos-s3-${match[2]}.${match[3]}${parsed.port ? `:${parsed.port}` : ""}`;
    const path = parsed.pathname === "/" ? "" : parsed.pathname;
    return { region, endpoint: `${parsed.protocol}//${host}${path}${parsed.search}${parsed.hash}`.replace(/\/+$/, "") };
}

export function getS3PresetHints(preset?: S3Preset) {
    return S3_PRESET_HINTS[preset || "custom"];
}

export function changesRequireOSSRetest(changedValues: Record<string, unknown>) {
    return Object.keys(changedValues).some((key) => CONNECTION_FIELDS.has(key));
}

export function validateOSSConnectionDraft(input: OSSConnectionTestDraft) {
    // Drive 没有 Bucket：可连接的前提是 OAuth 客户端与刷新令牌齐全。
    if (input.provider === "gdrive") {
        if (!trimConnectionValue(input.gdriveClientId)) return "请填写 Google Drive 客户端 ID";
        if (!trimConnectionValue(input.gdriveClientSecret)) return "请填写 Google Drive 客户端密钥";
        if (!trimConnectionValue(input.gdriveRefreshToken)) return "请填写 Google Drive 刷新令牌";
        return "";
    }
    if (!trimConnectionValue(input.bucket)) return "请填写对象存储 Bucket";
    return "";
}

export function normalizeOSSConnectionTestInput(input: OSSConnectionTestDraft): OSSConnectionTestInput {
    return {
        provider: input.provider,
        s3Preset: input.s3Preset || "custom",
        region: trimConnectionValue(input.region),
        endpoint: trimConnectionURL(input.endpoint),
        cdnBaseUrl: trimConnectionURL(input.cdnBaseUrl),
        bucket: trimConnectionValue(input.bucket),
        accessKeyId: trimConnectionValue(input.accessKeyId),
        accessKeySecret: trimConnectionValue(input.accessKeySecret),
        sessionToken: trimConnectionValue(input.sessionToken),
        pathPrefix: (trimConnectionValue(input.pathPrefix) || DEFAULT_OSS_PATH_PREFIX).replace(/^\/+|\/+$/g, ""),
        pathStyle: input.pathStyle === true,
        gdriveClientId: trimConnectionValue(input.gdriveClientId),
        gdriveClientSecret: trimConnectionValue(input.gdriveClientSecret),
        gdriveRefreshToken: trimConnectionValue(input.gdriveRefreshToken),
        gdriveRootFolderId: trimConnectionValue(input.gdriveRootFolderId),
    };
}

function trimConnectionValue(value?: string) {
    return (value || "").trim();
}

function trimConnectionURL(value?: string) {
    return trimConnectionValue(value).replace(/\/+$/, "");
}
