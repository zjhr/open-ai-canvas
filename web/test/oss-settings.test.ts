import { describe, expect, test } from "bun:test";

import { changesRequireOSSRetest, DEFAULT_OSS_PATH_PREFIX, getS3PresetHints, normalizeOSSConnectionTestInput, validateOSSConnectionDraft } from "../src/lib/oss-settings";
import { formValues, gdriveAuthorizeDraft, hasStorageChanges, normalizeStoragePayload, providerDraftValues, storageProviderLabel, validateStorageDraft, type OSSFormValues } from "../src/pages/admin/settings/storage-settings-model";
import { authorizeAdminGDrive, disconnectAdminGDrive, getAdminGDriveSetup, type AdminOSSSetting, type GDriveConnectStatus } from "../src/services/api/auth";
import { apiClient } from "../src/services/api/request";

describe("OSS settings helpers", () => {
    test("provides editable S3 endpoint hints for known presets", () => {
        expect(getS3PresetHints("r2")).toMatchObject({ region: "auto" });
        expect(getS3PresetHints("b2").endpoint).toContain("backblazeb2.com");
    });

    test("only connection fields invalidate a previous test", () => {
        expect(changesRequireOSSRetest({ endpoint: "https://s3.example.com" })).toBe(true);
        expect(changesRequireOSSRetest({ enabled: true })).toBe(false);
        expect(changesRequireOSSRetest({ allowUserS3: true })).toBe(false);
    });

    test("uses the product path prefix by default", () => {
        expect(DEFAULT_OSS_PATH_PREFIX).toBe("open-ai-canvas");
    });

    test("rejects a connection test draft without a bucket", () => {
        expect(validateOSSConnectionDraft({ provider: "aliyun", bucket: "  " })).toBe("请填写对象存储 Bucket");
        expect(validateOSSConnectionDraft({ provider: "aliyun", bucket: "canvas-assets" })).toBe("");
    });

    test("requires the Drive OAuth triple instead of a bucket", () => {
        expect(validateOSSConnectionDraft({ provider: "gdrive" })).toBe("请填写 Google Drive 客户端 ID");
        expect(validateOSSConnectionDraft({ provider: "gdrive", gdriveClientId: " client-id " })).toBe("请填写 Google Drive 客户端密钥");
        expect(validateOSSConnectionDraft({ provider: "gdrive", gdriveClientId: "client-id", gdriveClientSecret: "client-secret" })).toBe("请填写 Google Drive 刷新令牌");
        expect(validateOSSConnectionDraft({ provider: "gdrive", gdriveClientId: "client-id", gdriveClientSecret: "client-secret", gdriveRefreshToken: "refresh-token" })).toBe("");
        // 根文件夹可留空（写入「我的云端硬盘」根目录），Bucket 即便残留也不参与 Drive 校验。
        expect(validateOSSConnectionDraft({ provider: "gdrive", bucket: "  ", gdriveClientId: "client-id", gdriveClientSecret: "client-secret", gdriveRefreshToken: "refresh-token", gdriveRootFolderId: "" })).toBe("");
    });

    test("normalizes Drive credentials and treats every Drive field as connection state", () => {
        expect(
            normalizeOSSConnectionTestInput({
                provider: "gdrive",
                gdriveClientId: " client-id ",
                gdriveClientSecret: " client-secret ",
                gdriveRefreshToken: " refresh-token ",
                gdriveRootFolderId: " folder-id ",
            }),
        ).toMatchObject({ provider: "gdrive", gdriveClientId: "client-id", gdriveClientSecret: "client-secret", gdriveRefreshToken: "refresh-token", gdriveRootFolderId: "folder-id" });
        expect(normalizeOSSConnectionTestInput({ provider: "gdrive" })).toMatchObject({ gdriveClientId: "", gdriveClientSecret: "", gdriveRefreshToken: "", gdriveRootFolderId: "" });

        for (const changed of [{ gdriveClientId: "client-id" }, { gdriveClientSecret: "client-secret" }, { gdriveRefreshToken: "refresh-token" }, { gdriveRootFolderId: "folder-id" }]) {
            expect(changesRequireOSSRetest(changed)).toBe(true);
        }
    });

    test("normalizes a Tencent COS test draft when S3-only fields are not mounted", () => {
        const input = normalizeOSSConnectionTestInput({
            provider: "tencent",
            region: " ap-guangzhou ",
            endpoint: " https://cos.ap-guangzhou.myqcloud.com/ ",
            bucket: " example-1250000000 ",
            accessKeyId: " secret-id ",
            accessKeySecret: " secret-key ",
            pathPrefix: " /canvas/ ",
        });

        expect(input).toMatchObject({
            provider: "tencent",
            region: "ap-guangzhou",
            endpoint: "https://cos.ap-guangzhou.myqcloud.com",
            cdnBaseUrl: "",
            bucket: "example-1250000000",
            accessKeyId: "secret-id",
            accessKeySecret: "secret-key",
            sessionToken: "",
            pathPrefix: "canvas",
            s3Preset: "custom",
            pathStyle: false,
        });
    });
});

describe("Google Drive storage draft", () => {
    function adminSetting(overrides: Partial<AdminOSSSetting> = {}): AdminOSSSetting {
        return {
            enabled: true,
            provider: "gdrive",
            s3Preset: "custom",
            region: "",
            endpoint: "",
            cdnBaseUrl: "",
            cdnAuthMode: "",
            requireCDN: false,
            allowPrivateProxy: false,
            bucket: "",
            accessKeyId: "",
            hasAccessKeySecret: false,
            hasSessionToken: false,
            pathStyle: false,
            allowUserS3: true,
            publicBaseUrl: "",
            pathPrefix: DEFAULT_OSS_PATH_PREFIX,
            gdriveClientId: "client-id",
            gdriveRootFolderId: "",
            gdriveRootFolderName: "yingce-storage",
            gdriveAccountEmail: "ops@example.com",
            hasGDriveClientSecret: true,
            hasGDriveRefreshToken: true,
            ...overrides,
        };
    }

    function draft(setting: AdminOSSSetting, overrides: Partial<OSSFormValues> = {}): OSSFormValues {
        return { ...formValues(setting), mode: "gdrive", ...overrides };
    }

    test("switching provider to Drive keeps the established connection state", () => {
        // 真实故障回归：平台还在阿里云上，用户点 Drive 卡片准备切过去。
        // 连接状态（客户端 ID、目标文件夹）属于「已经连上的 Google 账号」，
        // 不属于「当前选中的 provider」。清空它们会让保存把服务端刚建好的
        // 目标文件夹抹掉，之后生成的文件就静默落到云盘根目录。
        const aliyunSetting = adminSetting({
            provider: "aliyun",
            bucket: "canvas-assets",
            endpoint: "https://oss-cn-hangzhou.aliyuncs.com",
            gdriveClientId: "client-id",
            gdriveRootFolderId: "folder-abc",
        });
        const values = providerDraftValues("gdrive", aliyunSetting, "brand");
        expect(values.gdriveClientId).toBe("client-id");
        expect(values.gdriveRootFolderId).toBe("folder-abc");
        // 对象存储的字段仍然要清空，否则会把阿里云的配置带进 Drive 的保存请求。
        expect(values.bucket).toBe("");
        expect(values.endpoint).toBe("");
    })

    test("labels the provider and clears every object-storage field Drive does not accept", () => {
        expect(storageProviderLabel("gdrive")).toBe("Google Drive");

        const payload = normalizeStoragePayload(
            draft(adminSetting(), {
                region: "oss-cn-hangzhou",
                endpoint: "https://oss-cn-hangzhou.aliyuncs.com",
                cdnBaseUrl: "https://media.example.com",
                cdnAuthMode: "public",
                requireCDN: true,
                bucket: "canvas-assets",
                accessKeyId: "access-key-id",
                accessKeySecret: "access-key-secret",
                sessionToken: "session-token",
                pathStyle: true,
                gdriveClientId: " client-id ",
                gdriveClientSecret: " client-secret ",
                gdriveRootFolderId: " folder-id ",
            }),
            adminSetting(),
        );

        expect(payload).toMatchObject({
            enabled: true,
            provider: "gdrive",
            region: "",
            endpoint: "",
            cdnBaseUrl: "",
            cdnAuthMode: "",
            requireCDN: false,
            bucket: "",
            accessKeyId: "",
            accessKeySecret: "",
            sessionToken: "",
            pathStyle: false,
            gdriveClientId: "client-id",
            gdriveClientSecret: "client-secret",
            gdriveRootFolderId: "folder-id",
        });
        // 刷新令牌由服务端授权流程写入，前端不再持有也不提交。
        expect(payload).not.toHaveProperty("gdriveRefreshToken");
    });

    test("Drive 校验只要求客户端 ID 与客户端密钥，密钥已保存时允许留空", () => {
        const setting = adminSetting();
        expect(validateStorageDraft(draft(setting), setting)).toBe("");
        expect(validateStorageDraft(draft(setting, { gdriveClientSecret: "" }), setting)).toBe("");
        expect(validateStorageDraft(draft(setting, { gdriveClientSecret: "new-secret" }), setting)).toBe("");
        expect(validateStorageDraft(draft(setting, { gdriveClientId: "" }), setting)).toBe("请填写 Google Drive 客户端 ID");

        // 从别的厂商切过来时后端不会复用 Drive 凭据，草稿必须重新填写客户端密钥。
        const switched = adminSetting({ provider: "s3", gdriveClientId: "", hasGDriveClientSecret: false });
        expect(validateStorageDraft(draft(switched, { gdriveClientId: "client-id", gdriveClientSecret: "" }), switched)).toBe("请填写 Google Drive 客户端密钥");
        expect(validateStorageDraft(draft(switched, { gdriveClientId: "client-id", gdriveClientSecret: "client-secret" }), switched)).toBe("");
    });

    test("未连接时先提示连接 Google 账号，而不是等服务端报缺少刷新令牌", () => {
        const notConnected = adminSetting({ hasGDriveRefreshToken: false });
        expect(validateStorageDraft(draft(notConnected), notConnected)).toBe("请先点「连接 Google 账号」完成 Google Drive 授权");
        // 授权完成后服务端一定带回刷新令牌，保存不再被本地校验拦住。
        expect(validateStorageDraft(draft(adminSetting({ hasGDriveRefreshToken: true })), adminSetting({ hasGDriveRefreshToken: true }))).toBe("");
    });

    test("一键授权只接受成对填写，客户端 ID 未改动时沿用已保存的凭据", () => {
        const setting = adminSetting();
        expect(gdriveAuthorizeDraft({ gdriveClientId: "client-id", gdriveClientSecret: "" }, setting)).toEqual({ input: {} });
        expect(gdriveAuthorizeDraft({ gdriveClientId: "", gdriveClientSecret: "" }, setting)).toEqual({ input: {} });
        expect(gdriveAuthorizeDraft({ gdriveClientId: " client-id ", gdriveClientSecret: " secret " }, setting)).toEqual({ input: { clientId: "client-id", clientSecret: "secret" } });
        expect(gdriveAuthorizeDraft({ gdriveClientId: "other-id", gdriveClientSecret: "" }, setting)).toEqual({ error: "客户端 ID 与客户端密钥必须成对填写，请补全另一个再连接" });
        expect(gdriveAuthorizeDraft({ gdriveClientId: "", gdriveClientSecret: "secret" }, setting)).toEqual({ error: "客户端 ID 与客户端密钥必须成对填写，请补全另一个再连接" });

        const empty = adminSetting({ gdriveClientId: "", hasGDriveClientSecret: false });
        expect(gdriveAuthorizeDraft({ gdriveClientId: "", gdriveClientSecret: "" }, empty)).toEqual({ error: "请先填写 Google Drive 客户端 ID 与客户端密钥" });
    });

    test("marks a Drive client-credential edit dirty and an untouched Drive form clean", () => {
        const setting = adminSetting();
        expect(hasStorageChanges(draft(setting), setting)).toBe(false);
        expect(hasStorageChanges(draft(setting, { gdriveClientSecret: "new-secret" }), setting)).toBe(true);
        expect(hasStorageChanges(draft(setting, { gdriveClientId: "other-client" }), setting)).toBe(true);
    });
});

describe("Google Drive one-click authorization API", () => {
    type RecordedRequest = { method?: string; url?: string; data?: unknown };

    async function callWithStub<T>(payload: unknown, call: () => Promise<T>) {
        const original = apiClient.request;
        const recorded: RecordedRequest = {};
        let result: T | undefined;
        apiClient.request = (async (config: RecordedRequest) => {
            recorded.method = config.method;
            recorded.url = config.url;
            recorded.data = config.data;
            return { data: { code: 0, data: payload, msg: "ok" }, status: 200, headers: {} };
        }) as typeof apiClient.request;
        try {
            result = await call();
        } finally {
            apiClient.request = original;
        }
        return { recorded, result };
    }

    const status: GDriveConnectStatus = {
        redirectUri: "https://canvas.example.com/api/admin/settings/oss/gdrive/callback",
        connected: false,
        accountEmail: "",
        rootFolderId: "",
        rootFolderName: "yingce-storage",
        hasClientCredentials: true,
    };

    test("setup 用 GET 读取连接状态", async () => {
        const { recorded, result } = await callWithStub({ status }, () => getAdminGDriveSetup());

        expect(recorded).toMatchObject({ method: "get", url: "/admin/settings/oss/gdrive/setup" });
        expect(recorded.data).toBeUndefined();
        expect(result).toEqual({ status });
    });

    test("authorize 用 POST 携带客户端凭据并返回跳转地址", async () => {
        const { recorded, result } = await callWithStub({ authorizeUrl: "https://accounts.google.com/o/oauth2/v2/auth?x=1" }, () => authorizeAdminGDrive({ clientId: "client-id", clientSecret: "client-secret" }));

        expect(recorded).toMatchObject({ method: "post", url: "/admin/settings/oss/gdrive/authorize", data: { clientId: "client-id", clientSecret: "client-secret" } });
        expect(result).toEqual({ authorizeUrl: "https://accounts.google.com/o/oauth2/v2/auth?x=1" });

        // 两个都留空表示沿用服务端已保存的凭据，仍然是同一个 POST。
        const reused = await callWithStub({ authorizeUrl: "https://accounts.google.com/o/oauth2/v2/auth?x=2" }, () => authorizeAdminGDrive({}));
        expect(reused.recorded).toMatchObject({ method: "post", url: "/admin/settings/oss/gdrive/authorize", data: {} });
    });

    test("disconnect 用 POST 且不带请求体", async () => {
        const setting = { enabled: true } as AdminOSSSetting;
        const { recorded, result } = await callWithStub({ setting }, () => disconnectAdminGDrive());

        expect(recorded).toMatchObject({ method: "post", url: "/admin/settings/oss/gdrive/disconnect" });
        expect(recorded.data).toBeUndefined();
        expect(result).toEqual({ setting });
    });
});
