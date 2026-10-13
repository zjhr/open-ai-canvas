/**
 * 提示词画廊的远端索引接口。
 *
 * 站内静态目录（public/inspirations）继续走 fetch；两个 HuggingFace 大库太大，
 * 由后端流式同步入库，这里只做检索与封面地址拼装。
 */
import { apiBaseURL, http } from "@/services/api/request";

export type RemotePromptGallerySource = {
    id: string;
    name: string;
    license: string;
    revision: string;
    totalRows: number;
    lastSyncedAt?: string | null;
    running: boolean;
    lastError?: string;
};

export type RemotePromptGalleryItem = {
    id: string;
    sourceId: string;
    mode: "text" | "image" | "video";
    title: string;
    description: string;
    prompt: string;
    promptZh?: string;
    category: string;
    model: string;
    credit: string;
    sourceUrl: string;
    coverPath?: string;
    ratio: string;
};

export type RemotePromptGalleryFacet = { id: string; count: number };

export type RemotePromptGallerySearchResult = {
    items: RemotePromptGalleryItem[];
    total: number;
    sources: RemotePromptGalleryFacet[];
    categories: RemotePromptGalleryFacet[];
};

export type RemotePromptGalleryQuery = {
    keyword?: string;
    mode?: string;
    sourceId?: string;
    category?: string;
    limit?: number;
    offset?: number;
};

export async function fetchPromptGallerySources() {
    const result = await http.get<{ sources: RemotePromptGallerySource[] }>("/prompt-gallery/sources");
    return result.sources;
}

export async function searchRemotePromptGallery(query: RemotePromptGalleryQuery) {
    return http.post<RemotePromptGallerySearchResult>("/prompt-gallery/search", query);
}

/** 管理员触发一个来源的同步；同步在后台跑，用 fetchPromptGallerySources 轮询进度。 */
export async function startPromptGallerySync(sourceId: string) {
    const result = await http.post<{ sources: RemotePromptGallerySource[] }>(`/admin/prompt-gallery/sources/${encodeURIComponent(sourceId)}/sync`);
    return result.sources;
}

/** 封面走后端代理：不把上游地址暴露给浏览器，也避免跨域与防盗链问题。 */
export function promptGalleryCoverUrl(sourceId: string, coverPath: string) {
    const params = new URLSearchParams({ source: sourceId, path: coverPath });
    return `${apiBaseURL}/prompt-gallery/cover?${params.toString()}`;
}