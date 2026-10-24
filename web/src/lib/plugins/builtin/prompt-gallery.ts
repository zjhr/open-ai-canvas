/**
 * 提示词画廊插件：把提示词目录接到生成输入框旁边。
 *
 * 插件只提供“查目录”和“把自然语言解析成检索条件”两个能力，按钮、抽屉和回填都由宿主渲染，
 * 因此没有 web 入口代码，也不需要沙箱运行时。
 *
 * 目录有两份：
 *   - 站内静态目录（public/inspirations 的导入产物，见 inspiration-catalog）；
 *   - 远端索引（后端流式同步的 HuggingFace 大库，见 services/api/prompt-gallery）。
 * 两者按来源区分：站内来源是 seedance/haohaoxue/youmind/lanshu，远端来源以 `hf-` 开头。
 * 检索时站内条目优先、远端条目接在后面；任一端失败都必须带 warnings 返回，不能静默少给结果。
 */
import { createElement } from "react";

import { buildPromptGalleryItems, normalizePromptGalleryPlan, planMergedPage, PROMPT_GALLERY_PAGE_SIZE, promptGalleryFacets, searchPromptGalleryItems } from "@/lib/inspirations/prompt-gallery";
import { resolveInspirations } from "@/lib/inspirations/resolve";
import type { CreationInspiration } from "@/lib/inspirations/catalog";
import { loadInspirationCatalog } from "@/services/inspiration-catalog";
import { fetchPromptGallerySources, promptGalleryCoverUrl, searchRemotePromptGallery, type RemotePromptGalleryItem, type RemotePromptGallerySource } from "@/services/api/prompt-gallery";
import { useInspirationStore } from "@/stores/use-inspiration-store";

import { registerPlugin } from "../plugin-registry";
import { registerPromptEditorSlot } from "../prompt-editor-slot-registry";
import { PromptGalleryAction } from "./prompt-gallery-action";
import type {
    PluginManifestV2,
    PluginTextMessage,
    PluginTextTool,
    PromptGalleryFacet,
    PromptGalleryItem,
    PromptGalleryProvider,
    PromptGalleryQuery,
    PromptGallerySearchPlan,
    PromptGallerySearchResult,
    RegisteredPlugin,
} from "../plugin-types";

export const PROMPT_GALLERY_PLUGIN_ID = "prompt-gallery";
/** 站内静态目录的来源标识。 */
export const PROMPT_GALLERY_SOURCE_ID = "inspiration-catalog";
/** 后端远端索引的来源标识。 */
export const PROMPT_GALLERY_REMOTE_SOURCE_ID = "remote-index";

const manifest: PluginManifestV2 = {
    id: PROMPT_GALLERY_PLUGIN_ID,
    name: "提示词画廊",
    version: "0.1.0",
    apiVersion: "yingce.plugin/v2",
    description: "在图片和视频的提示词输入框旁打开画廊，按关键词或自然语言检索提示词目录，选中即回填当前输入框。",
    author: "内置工具",
    permissions: ["ai.text"],
    trusted: true,
    surfaces: ["prompt-gallery", "settings"],
    runtime: { web: "trusted-backend" },
    // 输入框按钮走宿主插槽：宿主组件里没有画廊代码，加按钮/改布局互不影响。
    contributes: {
        promptSources: [PROMPT_GALLERY_SOURCE_ID, PROMPT_GALLERY_REMOTE_SOURCE_ID],
        promptEditorSlots: [{ slot: "prompt-composer-actions", priority: 10 }],
    },
};

const PLACEHOLDER = "（留空）";
/** 远端来源 id 前缀：与站内静态来源（seedance / haohaoxue / youmind / lanshu）区分开。 */
const REMOTE_SOURCE_PREFIX = "hf-";
/** 远端来源列表（名称/许可/同步状态）缓存时长，避免每次检索都打一次接口。 */
const REMOTE_SOURCES_TTL_MS = 60_000;

let remoteSourcesCache: { sources: RemotePromptGallerySource[]; loadedAt: number } | null = null;

/** 该来源是否由后端索引提供。 */
export function isRemotePromptGallerySource(sourceId: string): boolean {
    return sourceId.startsWith(REMOTE_SOURCE_PREFIX);
}

/** 清空远端来源缓存；管理员触发同步后调用，让前端立刻看到新状态。 */
export function invalidateRemotePromptGallerySources() {
    remoteSourcesCache = null;
}

async function loadRemoteSources(): Promise<RemotePromptGallerySource[]> {
    if (remoteSourcesCache && Date.now() - remoteSourcesCache.loadedAt < REMOTE_SOURCES_TTL_MS) return remoteSourcesCache.sources;
    const sources = await fetchPromptGallerySources();
    remoteSourcesCache = { sources, loadedAt: Date.now() };
    return sources;
}

function remoteSourceLabel(source: RemotePromptGallerySource | undefined, fallbackId: string) {
    if (!source) return fallbackId;
    return source.license ? `${source.name} · ${source.license}` : source.name;
}

/** 远端条目 → 画廊卡片。封面走后端代理，正文优先中文，收藏态取宿主本地偏好。 */
function toRemoteGalleryItem(item: RemotePromptGalleryItem, sources: Map<string, RemotePromptGallerySource>, favorites: ReadonlySet<string>): PromptGalleryItem {
    const source = sources.get(item.sourceId);
    return {
        id: item.id,
        title: item.title,
        description: item.description,
        image: item.coverPath ? promptGalleryCoverUrl(item.sourceId, item.coverPath) : undefined,
        mode: item.mode,
        prompt: (item.promptZh || item.prompt || "").trim(),
        sourceId: item.sourceId,
        sourceLabel: remoteSourceLabel(source, item.sourceId),
        sourceUrl: item.sourceUrl || undefined,
        credit: item.credit || undefined,
        license: source?.license,
        category: item.category || undefined,
        categoryLabel: item.category || undefined,
        ratio: item.ratio || undefined,
        tags: item.model ? [item.model] : undefined,
        favorite: favorites.has(item.id),
    };
}

function remoteQueryOf(query: PromptGalleryQuery) {
    return {
        keyword: query.keyword?.trim() || undefined,
        mode: query.mode,
        category: query.category && query.category !== "all" ? query.category : undefined,
    };
}

/**
 * 远端分面请求：把当前筛选原样带上（含来源与分类），只取 1 条结果，计数由服务端给。
 *
 * 服务端算来源计数时忽略来源、算分类计数时忽略分类，所以两个维度都得发过去；
 * 漏掉来源会让「选定来源后的分类计数」仍按全库算，数字就又骗人了。
 */
function remoteFacetQuery(query: PromptGalleryQuery | undefined) {
    return {
        ...remoteQueryOf(query ?? {}),
        sourceId: query?.sourceId && query.sourceId !== "all" ? query.sourceId : undefined,
        limit: 1,
    };
}

function remoteFailureMessage(error: unknown) {
    const detail = error instanceof Error ? error.message : String(error);
    return `远端提示词索引暂时不可用，本次只返回站内目录（${detail}）`;
}

/** 按 id 合并两组 facet，计数相加、保留先出现的标签。 */
function mergeFacets(primary: PromptGalleryFacet[], extra: PromptGalleryFacet[]): PromptGalleryFacet[] {
    const merged = new Map<string, PromptGalleryFacet>();
    for (const facet of [...primary, ...extra]) {
        const current = merged.get(facet.id);
        if (current) current.count += facet.count;
        else merged.set(facet.id, { ...facet });
    }
    return [...merged.values()];
}

const planTool: PluginTextTool = {
    type: "function",
    function: {
        name: "plan_prompt_gallery_search",
        description: "把用户的自然语言需求解析成提示词画廊的一次目录检索条件。",
        strict: true,
        parameters: {
            type: "object",
            additionalProperties: false,
            required: ["keyword", "mode", "sourceId", "category", "favoritesOnly", "explanation"],
            properties: {
                keyword: { type: "string", description: "用于目录全文检索的关键词，2-6 个最有区分度的词，用空格分隔；没有明确线索时返回空字符串。" },
                mode: { type: "string", enum: ["image", "video", "text", "any"], description: "目标创作类型；不确定时用 any，由宿主回落到当前输入框类型。" },
                sourceId: { type: "string", description: "来源标识，只能取给定清单里的值；不确定时返回空字符串。" },
                category: { type: "string", description: "分类标识，只能取给定清单里的值；不确定时返回空字符串。" },
                favoritesOnly: { type: "boolean", description: "用户是否只想看自己收藏的条目。" },
                explanation: { type: "string", description: "一句中文，说明你从需求里读出了哪些条件。" },
            },
        },
    },
};

const systemPrompt = [
    "你是提示词画廊的检索助手，负责把用户的一句话需求翻译成一次可执行的目录检索。",
    "你只做检索规划，不写提示词、不创作内容、不评价条目。",
    "keyword 要短而具体：优先保留主体、场景、风格、镜头或模型名，去掉“帮我找”“有没有”这类无检索价值的词。",
    "mode 默认沿用用户当前输入框的创作类型，只有用户明确要别的类型时才改。",
    "sourceId 与 category 只能取给定清单里的标识；清单里没有就返回空字符串，不要臆造来源或分类。",
    "不要编造条目数量、作者或具体提示词内容。",
    "最终必须调用 plan_prompt_gallery_search 工具，不要输出 Markdown 或在工具调用之外解释。",
].join("\n");

/** 站内目录加载 + 用户叠加层合成。收藏态参与筛选和排序，所以每次检索都读一次最新 store。 */
async function loadLocalGalleryItems(): Promise<{
    items: PromptGalleryItem[];
    categoryIds: ReadonlySet<string>;
    sourceIds: ReadonlySet<string>;
}> {
    const catalog = await loadInspirationCatalog();
    const { custom, overrides, hidden, favorites } = useInspirationStore.getState();
    const entries = resolveInspirations(catalog.entries, { custom, overrides, hidden });
    const categoryLabel = (item: CreationInspiration) => {
        const sourceId = item.sourceId ?? "original";
        return catalog.categoriesBySource[sourceId]?.find((option) => option.id === item.category)?.label;
    };
    const items = buildPromptGalleryItems(entries, { favorites: new Set(favorites), categoryLabel });
    return {
        items,
        categoryIds: new Set(items.map((item) => item.category).filter((id): id is string => Boolean(id))),
        sourceIds: new Set(items.map((item) => item.sourceId).filter((id): id is string => Boolean(id))),
    };
}

function facetPrompt(facets: { sources: Array<{ id: string; label: string }>; categories: Array<{ id: string; label: string }> }) {
    const format = (items: Array<{ id: string; label: string }>) => (items.length ? items.map((item) => `${item.id}=${item.label}`).join("；") : PLACEHOLDER);
    return [`可用来源：${format(facets.sources)}`, `可用分类：${format(facets.categories)}`].join("\n");
}

function planMessage(query: string, base: PromptGalleryQuery, facetsText: string): PluginTextMessage {
    return {
        role: "user",
        content: [
            `用户当前的创作类型：${base.mode ?? "未指定"}`,
            `用户当前已选来源：${base.sourceId ?? "全部"}`,
            `用户当前已选分类：${base.category ?? "全部"}`,
            "",
            facetsText,
            "",
            "用户需求：",
            query.trim(),
        ].join("\n"),
    };
}

function parseJsonObject(value: string): Record<string, unknown> | null {
    const cleaned = value
        .trim()
        .replace(/^```(?:json)?\s*/i, "")
        .replace(/\s*```$/, "");
    const candidates = [cleaned];
    const start = cleaned.indexOf("{");
    const end = cleaned.lastIndexOf("}");
    if (start >= 0 && end > start) candidates.push(cleaned.slice(start, end + 1));
    for (const candidate of candidates) {
        try {
            const parsed: unknown = JSON.parse(candidate);
            if (parsed && typeof parsed === "object" && !Array.isArray(parsed)) return parsed as Record<string, unknown>;
        } catch {
            // 兼容模型在 JSON 外再包一层说明的情况，继续尝试截取对象。
        }
    }
    return null;
}

/** 只查远端索引（来源是 hf-* 时）。 */
async function searchRemoteOnly(query: PromptGalleryQuery, sourceId: string, limit: number, offset: number): Promise<PromptGallerySearchResult> {
    try {
        const [sources, remote] = await Promise.all([
            loadRemoteSources(),
            searchRemotePromptGallery({ ...remoteQueryOf(query), sourceId, limit, offset }),
        ]);
        const byId = new Map(sources.map((source) => [source.id, source]));
        const favorites = new Set(useInspirationStore.getState().favorites);
        return { items: remote.items.map((item) => toRemoteGalleryItem(item, byId, favorites)), total: remote.total };
    } catch (error) {
        return { items: [], total: 0, warnings: [remoteFailureMessage(error)] };
    }
}

/**
 * 站内 + 远端合并检索。
 * 站内条目优先、远端接在后面，所以第 N 页可能同时含两边：先按 offset 切站内匹配，
 * 站内不够一页时再用剩余额度去远端取（远端 offset 要减掉站内命中总数）。
 */
async function searchAllSources(query: PromptGalleryQuery): Promise<PromptGallerySearchResult> {
    const limit = query.limit && query.limit > 0 ? query.limit : PROMPT_GALLERY_PAGE_SIZE;
    const offset = Math.max(0, Math.floor(query.offset ?? 0));
    const { items: localItems } = await loadLocalGalleryItems();
    const localAll = searchPromptGalleryItems(localItems, { ...query, offset: 0, limit: Number.MAX_SAFE_INTEGER });
    const page = planMergedPage(localAll.total, offset, limit);
    const localPage = localAll.items.slice(page.localOffset, page.localOffset + page.localLimit);

    try {
        const [sources, remote] = await Promise.all([
            loadRemoteSources(),
            searchRemotePromptGallery({ ...remoteQueryOf(query), limit: Math.max(1, page.remoteLimit), offset: page.remoteOffset }),
        ]);
        const byId = new Map(sources.map((source) => [source.id, source]));
        const favorites = new Set(useInspirationStore.getState().favorites);
        // remoteLimit 为 0 说明这一页已被站内填满；那次远端请求只用来拿 total。
        const remoteItems = page.remoteLimit > 0 ? remote.items.slice(0, page.remoteLimit).map((item) => toRemoteGalleryItem(item, byId, favorites)) : [];
        return { items: [...localPage, ...remoteItems], total: localAll.total + remote.total };
    } catch (error) {
        return { items: localPage, total: localAll.total, warnings: [remoteFailureMessage(error)] };
    }
}

function createPromptGallery(context: Parameters<NonNullable<RegisteredPlugin["createPromptGallery"]>>[0]): PromptGalleryProvider {
    const textService = context.services?.ai?.text;
    return {
        search: async (query) => {
            const sourceId = query.sourceId && query.sourceId !== "all" ? query.sourceId : "";
            const limit = query.limit && query.limit > 0 ? query.limit : PROMPT_GALLERY_PAGE_SIZE;
            const offset = Math.max(0, Math.floor(query.offset ?? 0));
            if (sourceId && isRemotePromptGallerySource(sourceId)) return searchRemoteOnly(query, sourceId, limit, offset);
            if (sourceId) {
                const { items } = await loadLocalGalleryItems();
                return searchPromptGalleryItems(items, { ...query, limit, offset });
            }
            return searchAllSources(query);
        },
        facets: async (query) => {
            const { items } = await loadLocalGalleryItems();
            const local = promptGalleryFacets(items, query);
            try {
                const [sources, remote] = await Promise.all([loadRemoteSources(), searchRemotePromptGallery(remoteFacetQuery(query))]);
                const byId = new Map(sources.map((source) => [source.id, source]));
                return {
                    sources: mergeFacets(
                        local.sources,
                        remote.sources.map((facet) => ({ id: facet.id, label: remoteSourceLabel(byId.get(facet.id), facet.id), count: facet.count })),
                    ),
                    categories: mergeFacets(
                        local.categories,
                        remote.categories.map((facet) => ({ id: facet.id, label: facet.id, count: facet.count })),
                    ),
                };
            } catch {
                // 远端不可用时只展示站内筛选项；检索结果里会带 warning 说明少了一部分。
                return local;
            }
        },
        planSearch: async (query, options) => {
            const base: PromptGalleryQuery = options?.base ?? {};
            if (!textService) throw new Error("提示词画廊暂未获得文本模型服务");
            const { items, categoryIds, sourceIds } = await loadLocalGalleryItems();
            const facets = promptGalleryFacets(items);
            // 远端来源也要进清单，否则 AI 无法把"找 Seedance 数据集里的"解析成 sourceId。
            const allSourceIds = new Set(sourceIds);
            let knownCategories = new Set(categoryIds);
            try {
                const [sources, remote] = await Promise.all([loadRemoteSources(), searchRemotePromptGallery({ limit: 1 })]);
                const byId = new Map(sources.map((source) => [source.id, source]));
                for (const facet of remote.sources) {
                    allSourceIds.add(facet.id);
                    facets.sources.push({ id: facet.id, label: remoteSourceLabel(byId.get(facet.id), facet.id), count: facet.count });
                }
                knownCategories = new Set([...knownCategories, ...remote.categories.map((facet) => facet.id)]);
                for (const facet of remote.categories) facets.categories.push({ id: facet.id, label: facet.id, count: facet.count });
            } catch {
                // 远端取不到时只按站内来源规划检索条件。
            }
            const response = await textService.requestToolResponse({
                messages: [
                    { role: "system", content: systemPrompt },
                    planMessage(query, base, facetPrompt(facets)),
                ],
                tools: [planTool],
                toolChoice: { type: "function", name: "plan_prompt_gallery_search" },
                signal: options?.signal,
            });
            const toolCall = response.toolCalls.find((call) => call.name === "plan_prompt_gallery_search");
            const parsed = parseJsonObject(toolCall?.arguments || response.content);
            if (!parsed) throw new Error("AI 没有返回可用的检索条件，请改用关键词搜索");
            const plan: PromptGallerySearchPlan = normalizePromptGalleryPlan(parsed, base, { sourceIds: allSourceIds, categoryIds: knownCategories });
            // 模型说 any 时 normalizePromptGalleryPlan 已经回落到 base.mode，这里只兜底 base 缺失的情况。
            if (!plan.mode && base.mode) plan.mode = base.mode;
            return plan;
        },
    };
}

export const promptGalleryPlugin: RegisteredPlugin = {
    manifest,
    createPromptGallery,
};

/**
 * 注册输入框按钮。
 *
 * 由 builtin/prompt-gallery-action.tsx 的模块副作用调用：注册表里存的是渲染函数，
 * 宿主在三个输入框各渲染一处 PromptEditorActions 就能拿到这个按钮。
 */
export function registerPromptGalleryEditorAction() {
    registerPromptEditorSlot({
        pluginId: PROMPT_GALLERY_PLUGIN_ID,
        slot: "prompt-composer-actions",
        priority: 10,
        render: (context) => createElement(PromptGalleryAction, { context }),
    });
}

registerPlugin(promptGalleryPlugin);
registerPromptGalleryEditorAction();