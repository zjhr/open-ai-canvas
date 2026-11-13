import { App, Button, Input, Skeleton } from "antd";
import { RefreshCw, Save, ShieldCheck } from "lucide-react";
import { useCallback, useEffect, useState } from "react";

import { Select, Switch } from "@/pages/admin/ui/controls";
import { getAdminJevReviewSetting, updateAdminJevReviewSetting, type JevReviewProtocol, type JevReviewSetting } from "@/services/api/jev-review";
import { AdminPageFrame } from "../components/admin-shell";
import { AdminStatusBadge, SettingsSectionCard } from "../components/admin-ui";

const protocolDefaults: Record<JevReviewProtocol, { baseUrl: string; model: string }> = {
    typesafe: { baseUrl: "https://api.typesafe.ai", model: "jev-latest" },
    openrouter: { baseUrl: "https://openrouter.ai/api/v1", model: "typesafe/jev-router" },
};

type Draft = {
    enabled: boolean;
    protocol: JevReviewProtocol;
    baseUrl: string;
    model: string;
    apiKey: string;
};

function toDraft(setting: JevReviewSetting): Draft {
    return { enabled: setting.enabled, protocol: setting.protocol || "typesafe", baseUrl: setting.baseUrl || "", model: setting.model || "", apiKey: "" };
}

export default function JevReviewSettingsPage() {
    const { message } = App.useApp();
    const [saved, setSaved] = useState<JevReviewSetting | null>(null);
    const [draft, setDraft] = useState<Draft | null>(null);
    const [loading, setLoading] = useState(true);
    const [saving, setSaving] = useState(false);
    const [error, setError] = useState("");

    const load = useCallback(async () => {
        setLoading(true);
        setError("");
        try {
            const result = await getAdminJevReviewSetting();
            setSaved(result.setting);
            setDraft(toDraft(result.setting));
        } catch (cause) {
            setError(cause instanceof Error ? cause.message : "读取内容审核配置失败");
        } finally {
            setLoading(false);
        }
    }, []);

    useEffect(() => {
        void load();
    }, [load]);

    const update = (patch: Partial<Draft>) => {
        setDraft((current) => (current ? { ...current, ...patch } : current));
        setError("");
    };

    const changeProtocol = (protocol: JevReviewProtocol) => {
        if (!draft) return;
        const previous = protocolDefaults[draft.protocol];
        const next = protocolDefaults[protocol];
        update({
            protocol,
            baseUrl: !draft.baseUrl || draft.baseUrl === previous.baseUrl ? next.baseUrl : draft.baseUrl,
            model: !draft.model || draft.model === previous.model ? next.model : draft.model,
        });
    };

    const dirty = Boolean(saved && draft && (draft.enabled !== saved.enabled || draft.protocol !== saved.protocol || draft.baseUrl.trim() !== saved.baseUrl || draft.model.trim() !== saved.model || draft.apiKey.trim() !== ""));

    const save = async () => {
        if (!saved || !draft || saving) return;
        if (draft.enabled && !saved.hasApiKey && !draft.apiKey.trim()) {
            message.error("启用内容审核前请填写 API Key");
            return;
        }
        setSaving(true);
        setError("");
        try {
            const result = await updateAdminJevReviewSetting({
                enabled: draft.enabled,
                protocol: draft.protocol,
                baseUrl: draft.baseUrl.trim(),
                model: draft.model.trim(),
                apiKey: draft.apiKey.trim(),
            });
            setSaved(result.setting);
            setDraft(toDraft(result.setting));
            message.success("内容审核配置已保存");
        } catch (cause) {
            const text = cause instanceof Error ? cause.message : "保存内容审核配置失败";
            setError(text);
            message.error(text);
        } finally {
            setSaving(false);
        }
    };

    const defaults = draft ? protocolDefaults[draft.protocol] : protocolDefaults.typesafe;

    return (
        <AdminPageFrame title="内容审核（Jev）" description="为 Agent 提供剧本、提示词、镜头和图片的独立判断" scroll>
            <div className="admin-settings-stack">
                {loading && !draft ? (
                    <div className="p-4">
                        <Skeleton active paragraph={{ rows: 6 }} />
                    </div>
                ) : null}

                {!loading && !draft ? (
                    <div className="flex items-center gap-3 p-4 text-sm" role="alert">
                        <span className="flex-1">{error || "当前没有可显示的配置"}</span>
                        <Button icon={<RefreshCw className="size-4" />} onClick={() => void load()}>
                            重新读取
                        </Button>
                    </div>
                ) : null}

                {draft ? (
                    <SettingsSectionCard
                        icon={<ShieldCheck className="size-4" aria-hidden="true" />}
                        title="接入配置"
                        description="Agent 的 content_review 工具会调用这里配置的服务。Key 加密保存在服务端，留空表示沿用已保存的值。"
                        status={<AdminStatusBadge label={draft.enabled ? "已启用" : "已停用"} tone={draft.enabled ? "success" : "neutral"} />}
                        footer={
                            <>
                                <span className="text-xs text-muted-foreground">{dirty ? "有未保存的修改" : "与服务端一致"}</span>
                                <div className="flex items-center gap-2">
                                    <Button icon={<RefreshCw className="size-4" />} disabled={saving} onClick={() => void load()}>
                                        刷新
                                    </Button>
                                    <Button type="primary" icon={<Save className="size-4" />} loading={saving} disabled={!dirty} onClick={() => void save()}>
                                        保存修改
                                    </Button>
                                </div>
                            </>
                        }
                    >
                        <div className="grid gap-5">
                            {error ? (
                                <div className="rounded-md border border-destructive/30 bg-destructive/5 px-3 py-2 text-sm text-destructive" role="alert">
                                    {error}
                                </div>
                            ) : null}

                            <div className="flex items-center justify-between gap-4">
                                <div>
                                    <strong className="block text-sm">启用内容审核</strong>
                                    <span className="text-xs text-muted-foreground">关闭后 Agent 不会暴露 content_review 工具。</span>
                                </div>
                                <Switch checked={draft.enabled} disabled={saving} aria-label="启用内容审核" onChange={(enabled) => update({ enabled })} />
                            </div>

                            <label className="grid gap-1.5">
                                <span className="text-sm font-medium">协议</span>
                                <Select
                                    value={draft.protocol}
                                    ariaLabel="内容审核协议"
                                    disabled={saving}
                                    onChange={changeProtocol}
                                    options={[
                                        { value: "typesafe", label: "TypeSafe System One（/v1/systemone）" },
                                        { value: "openrouter", label: "OpenRouter（Chat Completions 兼容）" },
                                    ]}
                                />
                                <span className="text-xs text-muted-foreground">{draft.protocol === "openrouter" ? "OpenRouter 上的 Jev 只返回文本，后端会要求固定 JSON 后归一化。" : "官方原生接口，直接返回带概率与置信度的结构化答案。"}</span>
                            </label>

                            <label className="grid gap-1.5">
                                <span className="text-sm font-medium">Base URL</span>
                                <Input value={draft.baseUrl} disabled={saving} placeholder={defaults.baseUrl} onChange={(event) => update({ baseUrl: event.target.value })} />
                            </label>

                            <label className="grid gap-1.5">
                                <span className="text-sm font-medium">模型</span>
                                <Input value={draft.model} disabled={saving} placeholder={defaults.model} onChange={(event) => update({ model: event.target.value })} />
                            </label>

                            <label className="grid gap-1.5">
                                <span className="text-sm font-medium">API Key</span>
                                <Input.Password
                                    value={draft.apiKey}
                                    disabled={saving}
                                    autoComplete="new-password"
                                    placeholder={saved?.hasApiKey ? "已保存，留空则不修改" : "填写服务端 API Key"}
                                    onChange={(event) => update({ apiKey: event.target.value })}
                                />
                            </label>
                        </div>
                    </SettingsSectionCard>
                ) : null}
            </div>
        </AdminPageFrame>
    );
}
