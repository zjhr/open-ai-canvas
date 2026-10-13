/**
 * 后台 · 提示词画廊索引。
 *
 * 两个 HuggingFace 大库（合计约 3 万条）由后端流式同步入库，这里只负责触发同步与看状态。
 * 同步在后台跑，页面按 running 标记轮询，不在前端等待整轮下载。
 */
import { App, Button } from "antd";
import { RefreshCw, Database, LoaderCircle, TriangleAlert } from "lucide-react";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";

import { AdminPageFrame } from "./components/admin-shell";
import { fetchPromptGallerySources, startPromptGallerySync, type RemotePromptGallerySource } from "@/services/api/prompt-gallery";
import { invalidateRemotePromptGallerySources } from "@/lib/plugins/builtin/prompt-gallery";

function formatSyncedAt(value?: string | null) {
    if (!value) return "从未同步";
    const timestamp = Date.parse(value);
    return Number.isFinite(timestamp) ? new Date(timestamp).toLocaleString() : "从未同步";
}

export function PromptGalleryIndexPage() {
    const { message } = App.useApp();
    const [sources, setSources] = useState<RemotePromptGallerySource[]>([]);
    const [loading, setLoading] = useState(true);
    const [error, setError] = useState("");
    const [triggering, setTriggering] = useState("");
    const pollTimer = useRef<number | null>(null);

    const reload = useCallback(async (options: { silent?: boolean } = {}) => {
        if (!options.silent) setLoading(true);
        try {
            const next = await fetchPromptGallerySources();
            setSources(next);
            setError("");
            invalidateRemotePromptGallerySources();
            return next;
        } catch (reason) {
            setError(reason instanceof Error ? reason.message : "读取提示词画廊索引状态失败");
            return null;
        } finally {
            if (!options.silent) setLoading(false);
        }
    }, []);

    useEffect(() => {
        void reload();
    }, [reload]);

    const anyRunning = useMemo(() => sources.some((source) => source.running), [sources]);

    // 同步在后台跑：只要有来源在同步就轮询状态，全部结束后停表。
    useEffect(() => {
        if (!anyRunning) {
            if (pollTimer.current !== null) {
                window.clearTimeout(pollTimer.current);
                pollTimer.current = null;
            }
            return;
        }
        pollTimer.current = window.setTimeout(() => {
            pollTimer.current = null;
            void reload({ silent: true });
        }, 3000);
        return () => {
            if (pollTimer.current !== null) {
                window.clearTimeout(pollTimer.current);
                pollTimer.current = null;
            }
        };
    }, [anyRunning, reload, sources]);

    const triggerSync = async (source: RemotePromptGallerySource) => {
        setTriggering(source.id);
        try {
            const next = await startPromptGallerySync(source.id);
            setSources(next);
            invalidateRemotePromptGallerySources();
            message.success(`已开始同步「${source.name}」，可以在本页查看进度`);
        } catch (reason) {
            message.error(reason instanceof Error ? reason.message : "触发同步失败");
        } finally {
            setTriggering("");
        }
    };

    return (
        <AdminPageFrame
            title="提示词画廊索引"
            description="把 HuggingFace 的提示词大库同步到站内索引，供创作输入框旁的提示词画廊检索"
            actions={
                <Button icon={<RefreshCw className="size-4" />} loading={loading} onClick={() => void reload()}>
                    刷新状态
                </Button>
            }
        >
            <div className="space-y-4">
                <p className="text-[var(--fs-body)] leading-6 text-foreground/70">
                    两个来源合计约 3 万条，元数据 155MB。同步在后台流式解析并写入索引，不会把正文打进前端包；
                    同步完成后，用户端「提示词画廊」的来源筛选里会出现对应来源。
                </p>
                {error ? (
                    <p className="rounded-[var(--r-md)] border border-border/60 bg-muted/25 px-3 py-2 text-[var(--fs-body)] text-[var(--color-danger,#d4380d)]" role="alert">{error}</p>
                ) : null}
                <div className="overflow-hidden rounded-[var(--r-md)] border border-border/60">
                    <table className="w-full border-collapse text-[var(--fs-body)]">
                        <thead className="bg-muted/30 text-left text-[var(--fs-micro)] text-foreground/60">
                            <tr>
                                <th className="px-3 py-2 font-medium">来源</th>
                                <th className="px-3 py-2 font-medium">许可</th>
                                <th className="px-3 py-2 font-medium">已索引</th>
                                <th className="px-3 py-2 font-medium">上次同步</th>
                                <th className="px-3 py-2 font-medium">操作</th>
                            </tr>
                        </thead>
                        <tbody>
                            {sources.map((source) => (
                                <tr key={source.id} className="border-t border-border/60 align-top">
                                    <td className="px-3 py-3">
                                        <div className="font-medium text-foreground">{source.name}</div>
                                        <div className="mt-0.5 text-[var(--fs-micro)] text-foreground/50">{source.id}</div>
                                        {source.lastError ? (
                                            <div className="mt-1 flex items-start gap-1 text-[var(--fs-micro)] text-[var(--color-danger,#d4380d)]">
                                                <TriangleAlert className="mt-0.5 size-3.5 shrink-0" aria-hidden="true" />
                                                <span>{source.lastError}</span>
                                            </div>
                                        ) : null}
                                    </td>
                                    <td className="px-3 py-3 text-foreground/70">{source.license || "—"}</td>
                                    <td className="px-3 py-3 tabular-nums text-foreground/80">{source.totalRows.toLocaleString()}</td>
                                    <td className="px-3 py-3 text-foreground/70">{formatSyncedAt(source.lastSyncedAt)}</td>
                                    <td className="px-3 py-3">
                                        <Button
                                            size="small"
                                            type="primary"
                                            icon={source.running ? <LoaderCircle className="size-3.5 animate-spin motion-reduce:animate-none" /> : <Database className="size-3.5" />}
                                            loading={triggering === source.id}
                                            disabled={source.running || Boolean(triggering)}
                                            onClick={() => void triggerSync(source)}
                                        >
                                            {source.running ? "同步中" : source.totalRows > 0 ? "重新同步" : "开始同步"}
                                        </Button>
                                    </td>
                                </tr>
                            ))}
                            {!sources.length && !loading ? (
                                <tr>
                                    <td colSpan={5} className="px-3 py-6 text-center text-foreground/60">没有可同步的来源</td>
                                </tr>
                            ) : null}
                        </tbody>
                    </table>
                </div>
                <p className="text-[var(--fs-micro)] leading-5 text-foreground/50">
                    封面不进仓库：用户浏览时才经后端按需读取并缓存到数据目录，上游地址不暴露给浏览器。
                </p>
            </div>
        </AdminPageFrame>
    );
}