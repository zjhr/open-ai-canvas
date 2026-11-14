// 对象存储设置的表单模型：表单值与接口载荷互转、变更检测与草稿校验。
//
// 服务器访问地址只校验 http/https 格式，允许本机、局域网与 HTTP 地址（产品约定，勿收紧）。

import { type AdminOSSSetting } from "@/services/api/auth";
import { DEFAULT_OSS_PATH_PREFIX, normalizeOSSConnectionTestInput, normalizeVolcengineTOSLocation, volcengineTOSLocationError } from "@/lib/oss-settings";
import { type S3Preset } from "@/lib/oss-settings";

export type StorageMode = "local" | AdminOSSSetting["provider"];

export type OSSFormValues = {
    mode: StorageMode;
    publicBaseUrl: string;
    region: string;
    endpoint: string;
    cdnBaseUrl: string;
    cdnAuthMode: "" | "public" | "qiniu" | string;
    requireCDN: boolean;
    allowPrivateProxy: boolean;
    bucket: string;
    accessKeyId: string;
    accessKeySecret: string;
    sessionToken: string;
    pathPrefix: string;
    s3Preset: S3Preset;
    pathStyle: boolean;
    allowUserS3: boolean;
    gdriveClientId: string;
    gdriveClientSecret: string;
    // 根文件夹由服务端连接流程创建/复用，这里只承载服务端当前值，保存时原样回传。
    gdriveRootFolderId: string;
};

export type StoragePayload = Pick<
    AdminOSSSetting,
    | "enabled"
    | "provider"
    | "region"
    | "endpoint"
    | "cdnBaseUrl"
    | "cdnAuthMode"
    | "requireCDN"
    | "allowPrivateProxy"
    | "bucket"
    | "accessKeyId"
    | "accessKeySecret"
    | "sessionToken"
    | "publicBaseUrl"
    | "pathPrefix"
    | "s3Preset"
    | "pathStyle"
    | "allowUserS3"
    | "gdriveClientId"
    | "gdriveClientSecret"
    | "gdriveRootFolderId"
>;

export function formValues(setting: AdminOSSSetting): OSSFormValues {
    return {
        mode: setting.enabled ? setting.provider : "local",
        publicBaseUrl: setting.publicBaseUrl || "",
        region: setting.region || "",
        endpoint: setting.endpoint || "",
        cdnBaseUrl: setting.cdnBaseUrl || "",
        cdnAuthMode: setting.cdnAuthMode || "",
        requireCDN: setting.requireCDN === true,
        allowPrivateProxy: setting.allowPrivateProxy === true,
        bucket: setting.bucket || "",
        accessKeyId: setting.accessKeyId || "",
        accessKeySecret: "",
        sessionToken: "",
        pathPrefix: setting.pathPrefix || DEFAULT_OSS_PATH_PREFIX,
        s3Preset: setting.s3Preset || "custom",
        pathStyle: setting.pathStyle === true,
        allowUserS3: setting.allowUserS3 === true,
        gdriveClientId: setting.gdriveClientId || "",
        gdriveClientSecret: "",
        gdriveRootFolderId: setting.gdriveRootFolderId || "",
    };
}

export function providerDraftValues(mode: Exclude<StorageMode, "local">, setting: AdminOSSSetting, pathPrefix: string): Partial<OSSFormValues> {
    // Drive 不是对象存储：没有 Bucket、Endpoint、Region 与一对访问密钥，也不支持 CDN 分发，
    // 因此草稿里始终清空这些字段，避免把上一次的 CDN/云厂商草稿带进保存请求。
    if (mode === "gdrive") {
        const sameProvider = mode === setting.provider;
        // 连接相关字段（客户端 ID、目标文件夹）属于「已经建立的连接」，不属于
        // 「当前选中的 provider」。从阿里云/本地切到 Drive 时它们必须原样带过来：
        // 清空会让保存把服务端刚建好的目标文件夹抹掉，生成的文件就落到云盘根目录。
        return {
            region: "",
            endpoint: "",
            cdnBaseUrl: "",
            cdnAuthMode: "",
            requireCDN: false,
            // Drive 的浏览器读取固定由平台代理，代理开关对它没有作用；保留已保存值，避免隐式改写。
            allowPrivateProxy: sameProvider ? setting.allowPrivateProxy === true : false,
            bucket: "",
            accessKeyId: "",
            accessKeySecret: "",
            sessionToken: "",
            pathPrefix: sameProvider ? setting.pathPrefix || pathPrefix || "" : pathPrefix || DEFAULT_OSS_PATH_PREFIX,
            s3Preset: "custom",
            pathStyle: false,
            gdriveClientId: setting.gdriveClientId || "",
            gdriveClientSecret: "",
            gdriveRootFolderId: setting.gdriveRootFolderId || "",
        };
    }
    if (mode === setting.provider) {
        return {
            region: setting.region || "",
            endpoint: setting.endpoint || "",
            cdnBaseUrl: setting.cdnBaseUrl || "",
            cdnAuthMode: setting.cdnAuthMode || "",
            requireCDN: setting.requireCDN === true,
            allowPrivateProxy: setting.allowPrivateProxy === true,
            bucket: setting.bucket || "",
            accessKeyId: setting.accessKeyId || "",
            accessKeySecret: "",
            sessionToken: "",
            pathPrefix: setting.pathPrefix || pathPrefix || "",
            s3Preset: setting.s3Preset || "custom",
            pathStyle: setting.pathStyle === true,
            gdriveClientId: "",
            gdriveClientSecret: "",
            gdriveRootFolderId: "",
        };
    }
    return {
        region: "",
        endpoint: "",
        cdnBaseUrl: "",
        cdnAuthMode: "",
        requireCDN: false,
        allowPrivateProxy: false,
        bucket: "",
        accessKeyId: "",
        accessKeySecret: "",
        sessionToken: "",
        pathPrefix: pathPrefix || DEFAULT_OSS_PATH_PREFIX,
        s3Preset: "custom",
        pathStyle: false,
        gdriveClientId: "",
        gdriveClientSecret: "",
        gdriveRootFolderId: "",
    };
}

export function normalizeStoragePayload(values: Partial<OSSFormValues>, setting: AdminOSSSetting): StoragePayload {
    const mode = values.mode || "local";
    const provider = mode === "local" ? setting.provider || "aliyun" : mode;
    // Drive 不接受也不使用 Bucket / Endpoint / Region / 一对访问密钥与 CDN，
    // 无论草稿里残留什么（例如刚从 S3 切过来），提交前一律清空。
    const isGDrive = provider === "gdrive";
    let region = values.region?.trim() || "";
    let endpoint = trimTrailingSlash(values.endpoint || "");
    const bucket = values.bucket?.trim() || "";
    if (!isGDrive && provider === "tencent" && !endpoint && region) endpoint = `https://cos.${region}.myqcloud.com`;
    if (provider === "volcengine") ({ region, endpoint } = normalizeVolcengineTOSLocation({ region, endpoint, bucket }));
    return {
        enabled: mode !== "local",
        provider,
        region: isGDrive ? "" : region,
        endpoint: isGDrive ? "" : endpoint,
        cdnBaseUrl: isGDrive ? "" : trimTrailingSlash(values.cdnBaseUrl || ""),
        cdnAuthMode: isGDrive ? "" : values.cdnAuthMode || "",
        requireCDN: isGDrive ? false : values.requireCDN === true,
        allowPrivateProxy: values.allowPrivateProxy === true,
        bucket: isGDrive ? "" : bucket,
        accessKeyId: isGDrive ? "" : values.accessKeyId?.trim() || "",
        accessKeySecret: isGDrive ? "" : values.accessKeySecret?.trim() || "",
        sessionToken: isGDrive || provider === "volcengine" ? "" : values.sessionToken?.trim() || "",
        publicBaseUrl: trimTrailingSlash(values.publicBaseUrl || ""),
        pathPrefix: (values.pathPrefix?.trim() || DEFAULT_OSS_PATH_PREFIX).replace(/^\/+|\/+$/g, ""),
        s3Preset: values.s3Preset || "custom",
        pathStyle: isGDrive || provider === "volcengine" ? false : values.pathStyle === true,
        allowUserS3: values.allowUserS3 === true,
        gdriveClientId: isGDrive ? values.gdriveClientId?.trim() || "" : "",
        gdriveClientSecret: isGDrive ? values.gdriveClientSecret?.trim() || "" : "",
        gdriveRootFolderId: isGDrive ? values.gdriveRootFolderId?.trim() || "" : "",
    };
}

export function hasStorageChanges(values: Partial<OSSFormValues>, setting: AdminOSSSetting | null) {
    if (!setting) return false;
    const draft = normalizeStoragePayload(values, setting);
    const saved = normalizeStoragePayload(formValues(setting), setting);
    if (draft.accessKeySecret || draft.sessionToken || draft.gdriveClientSecret) return true;
    return (Object.keys(saved) as Array<keyof StoragePayload>).some((key) => key !== "accessKeySecret" && key !== "sessionToken" && key !== "gdriveClientSecret" && draft[key] !== saved[key]);
}

export function validateStorageDraft(values: OSSFormValues, setting: AdminOSSSetting) {
    const draft = normalizeStoragePayload(values, setting);
    if (!draft.enabled) return validatePublicBaseURL(draft.publicBaseUrl);
    if (draft.provider === "gdrive") {
        if (!draft.gdriveClientId) return "请填写 Google Drive 客户端 ID";
        if (!draft.gdriveClientSecret && !(setting.provider === "gdrive" && setting.hasGDriveClientSecret)) return "请填写 Google Drive 客户端密钥";
        // 刷新令牌只由服务端授权流程写入，前端无法手填；服务端保存时同样要求它已存在。
        if (!setting.hasGDriveRefreshToken) return "请先点「连接 Google 账号」完成 Google Drive 授权";
        return "";
    }
    if (!draft.bucket) return "请填写对象存储 Bucket";
    if (!draft.endpoint)
        return draft.provider === "tencent"
            ? "请填写腾讯云 COS Region 或 Endpoint"
            : draft.provider === "qiniu"
              ? "请填写七牛云 Kodo 上传 Endpoint"
              : draft.provider === "volcengine"
                ? "请填写火山引擎 TOS Region 或 Endpoint"
                : draft.provider === "s3"
                  ? "请填写 S3 Endpoint 服务根 URL"
                  : "请填写阿里云 OSS Endpoint";
    if (draft.provider === "s3" && !draft.region) return "请填写 S3 Region";
    if (draft.provider === "volcengine" && !draft.region) return "请填写火山引擎 TOS Region";
    if (draft.provider === "volcengine" && volcengineTOSLocationError(draft.region, draft.endpoint)) return volcengineTOSLocationError(draft.region, draft.endpoint);
    if (!isHTTPURL(draft.endpoint)) return "Endpoint 必须是完整的 http/https 地址";
    if (draft.cdnBaseUrl && !isValidCDNBaseURL(draft.cdnBaseUrl)) return "CDN 或绑定域名只能填写 http/https 根地址，不能包含认证、路径、查询参数或片段";
    if (!draft.accessKeyId) return `请填写 ${accessKeyIdLabel(draft.provider)}`;
    if (!draft.accessKeySecret && !(setting.provider === draft.provider && setting.hasAccessKeySecret)) return `请填写 ${accessKeySecretLabel(draft.provider)}`;
    return "";
}

export function validatePublicBaseURL(value: string) {
    if (!value) return "服务器本地存储需要填写服务器访问地址";
    try {
        const parsed = new URL(value);
        if (!parsed.hostname || !["http:", "https:"].includes(parsed.protocol)) return "服务器访问地址必须是完整的 http/https 地址";
        if (parsed.search || parsed.hash) return "服务器访问地址不能包含查询参数或片段";
        if (parsed.pathname.replace(/\/+$/, "").endsWith("/api")) return "服务器访问地址请填写站点根地址，不要包含 /api";
        return "";
    } catch {
        return "服务器访问地址必须是完整的 http/https 地址";
    }
}

export function storageResponseMatches(setting: AdminOSSSetting, expected: StoragePayload) {
    const actual = normalizeStoragePayload(formValues(setting), setting);
    const fields: Array<keyof StoragePayload> = [
        "enabled",
        "provider",
        "region",
        "endpoint",
        "cdnBaseUrl",
        "cdnAuthMode",
        "requireCDN",
        "allowPrivateProxy",
        "bucket",
        "accessKeyId",
        "publicBaseUrl",
        "pathPrefix",
        "s3Preset",
        "pathStyle",
        "allowUserS3",
        "gdriveClientId",
        "gdriveRootFolderId",
    ];
    if (expected.accessKeySecret && !setting.hasAccessKeySecret) return false;
    if (expected.sessionToken && !setting.hasSessionToken) return false;
    if (expected.gdriveClientSecret && !setting.hasGDriveClientSecret) return false;
    return fields.every((key) => actual[key] === expected[key]);
}

export function isAdminOSSSetting(value: unknown): value is AdminOSSSetting {
    if (!value || typeof value !== "object") return false;
    const setting = value as Partial<AdminOSSSetting>;
    return (
        typeof setting.enabled === "boolean" &&
        ["aliyun", "tencent", "qiniu", "volcengine", "s3", "gdrive"].includes(setting.provider || "") &&
        ["aws", "r2", "b2", "rustfs", "custom"].includes(setting.s3Preset || "") &&
        typeof setting.region === "string" &&
        typeof setting.endpoint === "string" &&
        typeof setting.cdnBaseUrl === "string" &&
        typeof setting.cdnAuthMode === "string" &&
        typeof setting.requireCDN === "boolean" &&
        typeof setting.allowPrivateProxy === "boolean" &&
        typeof setting.bucket === "string" &&
        typeof setting.accessKeyId === "string" &&
        typeof setting.hasAccessKeySecret === "boolean" &&
        typeof setting.hasSessionToken === "boolean" &&
        typeof setting.gdriveClientId === "string" &&
        typeof setting.gdriveRootFolderId === "string" &&
        typeof setting.gdriveRootFolderName === "string" &&
        typeof setting.gdriveAccountEmail === "string" &&
        typeof setting.hasGDriveClientSecret === "boolean" &&
        typeof setting.hasGDriveRefreshToken === "boolean" &&
        typeof setting.pathStyle === "boolean" &&
        typeof setting.allowUserS3 === "boolean" &&
        typeof setting.publicBaseUrl === "string" &&
        typeof setting.pathPrefix === "string"
    );
}

export function storageConfigurationReady(mode: StorageMode, values: StoragePayload, setting: AdminOSSSetting) {
    if (mode === "local") return !validatePublicBaseURL(values.publicBaseUrl);
    return !validateStorageDraft({ ...formValues(setting), ...values, mode }, setting);
}

export function storageProviderLabel(provider?: StorageMode) {
    return provider === "s3" ? "S3 兼容存储" : provider === "tencent" ? "腾讯云 COS" : provider === "qiniu" ? "七牛云 Kodo" : provider === "volcengine" ? "火山引擎 TOS" : provider === "gdrive" ? "Google Drive" : provider === "aliyun" ? "阿里云 OSS" : "服务器本地";
}

export function providerGuidance(mode: Exclude<StorageMode, "local">) {
    if (mode === "s3") return "使用预设快速填写 Region 与 Endpoint，也可以选择自定义；自托管 S3 仍受服务端私网主机白名单约束。";
    if (mode === "tencent") return "腾讯云可只填写 Region，由服务端生成标准 COS Endpoint；也可填写完整 Endpoint 覆盖。";
    if (mode === "qiniu") return "七牛上传必须配置上传 Endpoint；绑定域名可选，留空时资源由当前后端使用 AK/SK 代理读取。";
    if (mode === "volcengine") return "火山引擎可只填写 Region，由服务端生成 S3 兼容 Endpoint；粘贴控制台的原生域名或 Bucket 域名也会换算为同地域 tos-s3 地址。浏览器直连源站时需在 Bucket 配置跨域访问。";
    if (mode === "gdrive") return "Drive 只填 OAuth 客户端 ID 与密钥，然后一键连接 Google 账号：刷新令牌与目标文件夹由服务端在授权后自动获得，且不支持 CDN，浏览器读取固定由平台代理。";
    return "阿里云需要完整 OSS Endpoint、Bucket 和访问密钥；CDN 域名可选。";
}

export function accessKeyIdLabel(mode: Exclude<StorageMode, "local"> | AdminOSSSetting["provider"]) {
    return mode === "tencent" ? "SecretId" : mode === "qiniu" ? "AccessKey" : mode === "volcengine" ? "Access Key ID" : "AccessKey ID";
}

export function accessKeySecretLabel(mode: Exclude<StorageMode, "local"> | AdminOSSSetting["provider"]) {
    return mode === "tencent" || mode === "qiniu" ? "SecretKey" : mode === "volcengine" ? "Secret Access Key" : "AccessKey Secret";
}

export function isHTTPURL(value: string) {
    try {
        const parsed = new URL(value);
        return Boolean(parsed.hostname) && ["http:", "https:"].includes(parsed.protocol);
    } catch {
        return false;
    }
}

export function isValidCDNBaseURL(value: string) {
    try {
        const parsed = new URL(value);
        return Boolean(parsed.hostname) && ["http:", "https:"].includes(parsed.protocol) && !parsed.username && !parsed.password && !parsed.search && !parsed.hash && !parsed.pathname.replace(/\/+$/, "");
    } catch {
        return false;
    }
}

export function trimTrailingSlash(value: string) {
    return value.trim().replace(/\/+$/, "");
}

export function connectionInput(values: Partial<OSSFormValues>) {
    return normalizeOSSConnectionTestInput({
        provider: !values.mode || values.mode === "local" ? ("aliyun" as const) : values.mode,
        s3Preset: values.s3Preset,
        region: values.region,
        endpoint: values.endpoint,
        cdnBaseUrl: values.cdnBaseUrl,
        bucket: values.bucket,
        accessKeyId: values.accessKeyId,
        accessKeySecret: values.accessKeySecret,
        sessionToken: values.sessionToken,
        pathPrefix: values.pathPrefix,
        pathStyle: values.pathStyle === true,
        gdriveClientId: values.gdriveClientId,
        gdriveClientSecret: values.gdriveClientSecret,
        gdriveRootFolderId: values.gdriveRootFolderId,
    });
}

// 一键授权的请求体：两个都填就顺带保存，都留空则沿用服务端已保存的客户端凭据。
// 只填一个几乎总是漏填，且服务端会直接拒绝，因此先在本地拦下、不发请求。
export type GDriveAuthorizeDraft = {
    input?: { clientId?: string; clientSecret?: string };
    error?: string;
};

export function gdriveAuthorizeDraft(values: Partial<OSSFormValues>, setting: AdminOSSSetting): GDriveAuthorizeDraft {
    const clientId = (values.gdriveClientId || "").trim();
    const clientSecret = (values.gdriveClientSecret || "").trim();
    const savedClientId = (setting.gdriveClientId || "").trim();
    const hasSavedCredentials = savedClientId !== "" && setting.hasGDriveClientSecret === true;
    // 表单会回显服务端保存的客户端 ID：ID 没改、密钥留空时，语义是「沿用已保存的凭据」。
    const keepsSavedCredentials = hasSavedCredentials && clientId === savedClientId;
    if (!clientId && !clientSecret) {
        if (hasSavedCredentials) return { input: {} };
        return { error: "请先填写 Google Drive 客户端 ID 与客户端密钥" };
    }
    if (!clientId || !clientSecret) {
        if (!clientSecret && keepsSavedCredentials) return { input: {} };
        return { error: "客户端 ID 与客户端密钥必须成对填写，请补全另一个再连接" };
    }
    return { input: { clientId, clientSecret } };
}

export function hasValidSettingTime(value?: string) {
    if (!value) return false;
    const date = new Date(value);
    return !Number.isNaN(date.getTime()) && date.getFullYear() >= 2000;
}

export function formatSettingTime(value: string | undefined, fallback: string) {
    if (!hasValidSettingTime(value)) return fallback;
    return `更新于 ${new Date(value as string).toLocaleString("zh-CN", { hour12: false })}`;
}
