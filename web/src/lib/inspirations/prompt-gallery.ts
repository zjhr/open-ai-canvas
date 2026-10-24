/**
 * 提示词画廊的检索层：把灵感目录条目转成画廊卡片，并按条件过滤、分页。
 *
 * 这里只做纯计算，不拉数据也不碰用户偏好：内置目录、用户叠加层和收藏都由调用方传入，
 * 插件（builtin/prompt-gallery）负责把它们拼在一起，抽屉只负责渲染。
 */
import { catalogIdOf, inspirationSourceOf, type CreationInspiration } from "./catalog";
import { fillablePrompt } from "./resolve";
import type { PromptGalleryFacet, PromptGalleryItem, PromptGalleryMode, PromptGalleryQuery, PromptGallerySearchPlan } from "@/lib/plugins/plugin-types";

/** 抽屉一页 24 张，和灵感页默认值一致；分页后首屏不会一次载入上千张外部封面。 */
export const PROMPT_GALLERY_PAGE_SIZE = 24;

const GALLERY_MODES: PromptGalleryMode[] = ["text", "image", "video"];

/**
 * 灵感条目 → 画廊卡片。
 * 正文取 `fillablePrompt`（双语来源优先中文），保证卡片上看到的和回填到输入框的是同一段。
 */
export function buildPromptGalleryItems(
    entries: CreationInspiration[],
    options: { favorites: ReadonlySet<string>; categoryLabel?: (item: CreationInspiration) => string | undefined },
): PromptGalleryItem[] {
    return entries.map((item) => {
        const id = catalogIdOf(item);
        // 导入文件可能带未知来源标识；卡片宁可少一个来源标签，也不能因此整页崩掉。
        const source = inspirationSourceOf(item) ?? { label: "自定义" };
        return {
            id,
            title: item.title,
            description: item.description,
            image: item.image,
            mode: item.mode,
            prompt: fillablePrompt(item),
            sourceId: item.sourceId ?? "original",
            sourceLabel: source.label,
            sourceUrl: item.sourceUrl || source.repository,
            credit: item.credit,
            license: source.license,
            category: item.category,
            categoryLabel: options.categoryLabel?.(item),
            ratio: item.ratio,
            tags: item.tags,
            favorite: options.favorites.has(id),
        };
    });
}

/** 关键词匹配范围：标题、描述、标签、分类、署名和正文——搜正文才是提示词画廊的主要用法。 */
function gallerySearchText(item: PromptGalleryItem): string {
    return [item.title, item.description, item.categoryLabel, item.category, item.credit, item.sourceLabel, ...(item.tags ?? []), item.prompt]
        .filter(Boolean)
        .join(" ")
        .toLocaleLowerCase();
}

function matchesQuery(item: PromptGalleryItem, query: PromptGalleryQuery, keyword: string): boolean {
    if (query.mode && item.mode !== query.mode) return false;
    if (query.sourceId && query.sourceId !== "all" && item.sourceId !== query.sourceId) return false;
    if (query.category && query.category !== "all" && item.category !== query.category) return false;
    if (query.favoritesOnly && !item.favorite) return false;
    if (keyword && !gallerySearchText(item).includes(keyword)) return false;
    return true;
}

/** 关键词归一化：检索与分面计数共用同一份口径，避免两处判断不一致。 */
function galleryKeyword(query: PromptGalleryQuery | undefined): string {
    return query?.keyword?.trim().toLocaleLowerCase() ?? "";
}

/**
 * 过滤 + 稳定排序 + 分页。
 * 收藏条目排在最前：用户收藏过的提示词通常就是要反复用的那几条。
 */
export function searchPromptGalleryItems(items: PromptGalleryItem[], query: PromptGalleryQuery): { items: PromptGalleryItem[]; total: number } {
    const keyword = galleryKeyword(query);
    const matched = items.filter((item) => matchesQuery(item, query, keyword));
    const sorted = [...matched].sort((left, right) => Number(Boolean(right.favorite)) - Number(Boolean(left.favorite)));
    const offset = Math.max(0, Math.floor(query.offset ?? 0));
    const limit = query.limit && query.limit > 0 ? Math.floor(query.limit) : PROMPT_GALLERY_PAGE_SIZE;
    return { items: sorted.slice(offset, offset + limit), total: sorted.length };
}

/**
 * 跨端分页切分：站内目录在前、远端索引在后，一页可能同时来自两边。
 *
 * 给定站内命中总数和请求的 offset/limit，算出站内该取哪一段、远端该从哪个 offset 取多少。
 * 抽成纯函数是因为这段边界算术最容易出错（页正好落在交界处、页完全在远端）。
 */
export function planMergedPage(localTotal: number, offset: number, limit: number): { localOffset: number; localLimit: number; remoteOffset: number; remoteLimit: number } {
    const safeLocalTotal = Math.max(0, Math.floor(localTotal));
    const safeOffset = Math.max(0, Math.floor(offset));
    const safeLimit = Math.max(0, Math.floor(limit));
    const localOffset = Math.min(safeOffset, safeLocalTotal);
    const localLimit = Math.max(0, Math.min(safeLimit, safeLocalTotal - localOffset));
    return {
        localOffset,
        localLimit,
        remoteOffset: Math.max(0, safeOffset - safeLocalTotal),
        remoteLimit: Math.max(0, safeLimit - localLimit),
    };
}

/**
 * 分面计数的取样集合：按当前筛选过滤，但忽略正在统计的那个维度。
 *
 * 算来源时忽略已选来源、算分类时忽略已选分类：否则下拉里会只剩当前已选项，
 * 用户也无法在两个维度之间来回切换。其余条件（类型/关键词/收藏）必须与检索一致，
 * 否则就会出现「下拉标着 8000 条，选中后只出来 2000 条」。
 */
function facetSample(items: PromptGalleryItem[], query: PromptGalleryQuery | undefined, ignore: "sourceId" | "category"): PromptGalleryItem[] {
    if (!query) return items;
    const keyword = galleryKeyword(query);
    const scoped: PromptGalleryQuery = { ...query, [ignore]: undefined };
    return items.filter((item) => matchesQuery(item, scoped, keyword));
}

/** 统计单个维度的计数。只保留有条目的取值，避免下拉里出现选了必定空的选项。 */
function countFacets(items: PromptGalleryItem[], dimension: "sourceId" | "category"): PromptGalleryFacet[] {
    const counts = new Map<string, PromptGalleryFacet>();
    for (const item of items) {
        const id = dimension === "sourceId" ? item.sourceId : item.category;
        if (!id) continue;
        const label = (dimension === "sourceId" ? item.sourceLabel : item.categoryLabel) || id;
        const current = counts.get(id);
        counts.set(id, { id, label: current?.label ?? label, count: (current?.count ?? 0) + 1 });
    }
    const byCount = (left: PromptGalleryFacet, right: PromptGalleryFacet) => right.count - left.count || left.label.localeCompare(right.label, "zh-Hans-CN");
    return [...counts.values()].sort(byCount);
}

/**
 * 来源与分类筛选项。传入当前筛选时，计数按筛选后的结果计算（各维度排除自己），
 * 使下拉里显示的数字等于「再选中它」能筛出的条数；不传则退化为整库计数。
 */
export function promptGalleryFacets(items: PromptGalleryItem[], query?: PromptGalleryQuery): { sources: PromptGalleryFacet[]; categories: PromptGalleryFacet[] } {
    return {
        sources: countFacets(facetSample(items, query, "sourceId"), "sourceId"),
        categories: countFacets(facetSample(items, query, "category"), "category"),
    };
}

function asTrimmedString(value: unknown): string | undefined {
    return typeof value === "string" && value.trim() ? value.trim() : undefined;
}

/** 模型可能返回一个关键词数组，也可能返回单段字符串；两种都收敛成一次检索用的关键词。 */
function asKeyword(value: unknown): string | undefined {
    if (Array.isArray(value)) {
        const parts = value.map(asTrimmedString).filter((part): part is string => Boolean(part));
        return parts.length ? parts.join(" ") : undefined;
    }
    return asTrimmedString(value);
}

/**
 * 收敛 AI 解析出的检索条件。
 *
 * 模型返回的是外部输入：模式、来源和分类必须落在已知取值内，否则丢弃该条件；
 * 解析失败时退回用户当前看到的筛选，不能让一次幻觉把画廊清空。
 */
export function normalizePromptGalleryPlan(raw: unknown, fallback: PromptGalleryQuery, known?: { sourceIds?: ReadonlySet<string>; categoryIds?: ReadonlySet<string> }): PromptGallerySearchPlan {
    const record = raw && typeof raw === "object" && !Array.isArray(raw) ? (raw as Record<string, unknown>) : {};
    const mode = GALLERY_MODES.includes(record.mode as PromptGalleryMode) ? (record.mode as PromptGalleryMode) : fallback.mode;
    const sourceId = asTrimmedString(record.sourceId);
    const category = asTrimmedString(record.category);
    return {
        keyword: asKeyword(record.keyword) ?? asKeyword(record.keywords),
        mode,
        sourceId: sourceId && (!known?.sourceIds || known.sourceIds.has(sourceId)) ? sourceId : undefined,
        category: category && (!known?.categoryIds || known.categoryIds.has(category)) ? category : undefined,
        favoritesOnly: typeof record.favoritesOnly === "boolean" ? record.favoritesOnly : undefined,
        explanation: asTrimmedString(record.explanation),
    };
}
