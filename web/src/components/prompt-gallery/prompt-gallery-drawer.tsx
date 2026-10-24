/**
 * 提示词画廊抽屉：输入框旁的统一入口，宿主只渲染一次，画布节点面板和创作页共用。
 *
 * 数据全部来自插件 provider（当前是灵感目录），抽屉自己不拉远端、不读协议文件；
 * 收藏通过宿主灵感 store 写入，因此画廊和灵感页看到的是同一份用户偏好。
 */
import { useCallback, useEffect, useMemo, useRef, useState, type KeyboardEvent as ReactKeyboardEvent } from "react";
import { App, Button, Input, Tooltip } from "antd";
import { ExternalLink, FileText, Heart, Image as ImageIcon, LoaderCircle, Search, Sparkles, Video } from "lucide-react";

import { PaginationBar } from "@/components/layout/workspace-page";
import { Select } from "@/components/ui/base/select";
import { SegmentedControl } from "@/components/ui/base/segmented-control";
import { AppDrawer } from "@/components/ui/product/app-drawer";
import { Callout } from "@/components/ui/product/callout";
import { useDebouncedValue } from "@/hooks/use-debounced-value";
import { PROMPT_GALLERY_PAGE_SIZE } from "@/lib/inspirations/prompt-gallery";
import type { PromptGalleryFacet, PromptGalleryItem, PromptGalleryMode, PromptGalleryProvider, PromptGallerySearchPlan } from "@/lib/plugins/plugin-types";
import { useInspirationStore } from "@/stores/use-inspiration-store";

export type PromptGalleryDrawerProps = {
    open: boolean;
    /** 当前输入框的创作类型，打开时作为默认筛选。 */
    mode: PromptGalleryMode;
    /** 插件提供的检索能力；为空表示插件未启用，宿主不应该打开抽屉。 */
    provider: PromptGalleryProvider | null;
    onClose: () => void;
    /** 使用某条提示词；由宿主机决定“空则填入、非空则追加”，抽屉不直接改输入框。 */
    onApply: (item: PromptGalleryItem) => void;
};

const MODE_LABELS: Record<PromptGalleryMode, string> = { image: "图片", video: "视频", text: "文本" };
const MODE_ICONS: Record<PromptGalleryMode, typeof ImageIcon> = { image: ImageIcon, video: Video, text: FileText };
const PAGE_SIZE_OPTIONS = [PROMPT_GALLERY_PAGE_SIZE, PROMPT_GALLERY_PAGE_SIZE * 2, PROMPT_GALLERY_PAGE_SIZE * 4];

export function PromptGalleryDrawer({ open, mode, provider, onClose, onApply }: PromptGalleryDrawerProps) {
    const { message } = App.useApp();
    const [keyword, setKeyword] = useState("");
    const debouncedKeyword = useDebouncedValue(keyword, 250);
    const [modeFilter, setModeFilter] = useState<PromptGalleryMode | "all">(mode);
    const [sourceFilter, setSourceFilter] = useState("all");
    const [categoryFilter, setCategoryFilter] = useState("all");
    const [favoritesOnly, setFavoritesOnly] = useState(false);
    const [plan, setPlan] = useState<PromptGallerySearchPlan | null>(null);
    const [planning, setPlanning] = useState(false);
    const [planError, setPlanError] = useState("");
    const [page, setPage] = useState(1);
    const [pageSize, setPageSize] = useState(PROMPT_GALLERY_PAGE_SIZE);
    const [items, setItems] = useState<PromptGalleryItem[]>([]);
    const [total, setTotal] = useState(0);
    const [facets, setFacets] = useState<{ sources: PromptGalleryFacet[]; categories: PromptGalleryFacet[] }>({ sources: [], categories: [] });
    const [loading, setLoading] = useState(false);
    const [error, setError] = useState("");
    const [warnings, setWarnings] = useState<string[]>([]);
    const [refreshKey, setRefreshKey] = useState(0);
    const requestSequence = useRef(0);
    const planAbort = useRef<AbortController | null>(null);

    // 手动改检索条件就丢弃上一轮 AI 条件，避免“看不见的条件”继续生效；
    // 只能在用户操作里清，不能挂在筛选值变化上——AI 自己也会改这些值。
    const clearPlan = useCallback(() => {
        setPlan(null);
        setPlanError("");
    }, []);

    useEffect(() => {
        if (!open) return;
        setModeFilter(mode);
        clearPlan();
        setPage(1);
    }, [clearPlan, open, mode]);

    useEffect(() => () => planAbort.current?.abort(), []);

    /** 分面计数只跟筛选条件有关、与翻页无关，单独拆一份，翻页时不必重算。 */
    const facetQuery = useMemo(
        () => ({
            keyword: plan?.keyword ?? debouncedKeyword,
            mode: modeFilter === "all" ? undefined : modeFilter,
            sourceId: sourceFilter,
            category: categoryFilter,
            favoritesOnly,
        }),
        [categoryFilter, debouncedKeyword, favoritesOnly, modeFilter, plan?.keyword, sourceFilter],
    );

    const query = useMemo(() => ({ ...facetQuery, limit: pageSize, offset: (page - 1) * pageSize }), [facetQuery, page, pageSize]);

    useEffect(() => {
        if (!open || !provider) return;
        const sequence = requestSequence.current + 1;
        requestSequence.current = sequence;
        setLoading(true);
        setError("");
        provider
            .search(query)
            .then((result) => {
                if (requestSequence.current !== sequence) return;
                setItems(result.items);
                setTotal(result.total);
                setWarnings(result.warnings ?? []);
            })
            .catch((reason: unknown) => {
                if (requestSequence.current !== sequence) return;
                setItems([]);
                setTotal(0);
                setWarnings([]);
                setError(reason instanceof Error ? reason.message : "提示词画廊加载失败");
            })
            .finally(() => {
                if (requestSequence.current === sequence) setLoading(false);
            });
    }, [open, provider, query, refreshKey]);

    useEffect(() => {
        if (!open || !provider?.facets) return;
        let cancelled = false;
        // 带上当前筛选：下拉里的计数必须等于"再选中它"能筛出的条数，否则用户会以为漏了条目。
        void provider
            .facets(facetQuery)
            .then((result) => {
                if (!cancelled) setFacets(result);
            })
            .catch(() => {
                if (!cancelled) setFacets({ sources: [], categories: [] });
            });
        return () => {
            cancelled = true;
        };
    }, [facetQuery, open, provider, refreshKey]);

    const runAiSearch = useCallback(async () => {
        const request = keyword.trim();
        if (!provider?.planSearch || !request || planning) return;
        const controller = new AbortController();
        planAbort.current?.abort();
        planAbort.current = controller;
        setPlanning(true);
        setPlanError("");
        // 解析期间先撤掉上一轮条件，避免旧条件和新结果同时显示。
        setPlan(null);
        try {
            const next = await provider.planSearch(request, {
                signal: controller.signal,
                base: { mode: modeFilter === "all" ? mode : modeFilter, sourceId: sourceFilter, category: categoryFilter, favoritesOnly },
            });
            if (controller.signal.aborted) return;
            setPlan(next);
            if (next.mode && next.mode !== modeFilter) setModeFilter(next.mode);
            if (next.sourceId) setSourceFilter(next.sourceId);
            if (next.category) setCategoryFilter(next.category);
            if (next.favoritesOnly !== undefined) setFavoritesOnly(next.favoritesOnly);
            setPage(1);
        } catch (reason) {
            if (controller.signal.aborted) return;
            setPlanError(reason instanceof Error ? reason.message : "AI 搜索失败，请改用关键词搜索");
        } finally {
            if (!controller.signal.aborted) setPlanning(false);
        }
    }, [categoryFilter, favoritesOnly, keyword, mode, modeFilter, planning, provider, sourceFilter]);

    const toggleFavorite = (item: PromptGalleryItem) => {
        useInspirationStore.getState().toggleFavorite(item.id);
        setRefreshKey((current) => current + 1);
    };

    const applyItem = (item: PromptGalleryItem) => {
        if (!item.prompt.trim()) {
            message.warning("这条灵感没有可用正文，已跳过");
            return;
        }
        onApply(item);
        message.success(`已使用「${item.title}」`);
        onClose();
    };

    const onCardKeyDown = (event: ReactKeyboardEvent<HTMLElement>, item: PromptGalleryItem) => {
        if (event.key !== "Enter" && event.key !== " ") return;
        event.preventDefault();
        applyItem(item);
    };

    const modeOptions = useMemo(
        () => [
            { label: "全部", value: "all" },
            { label: MODE_LABELS.image, value: "image" },
            { label: MODE_LABELS.video, value: "video" },
            { label: MODE_LABELS.text, value: "text" },
        ],
        [],
    );

    const activePlanChips = useMemo(() => {
        if (!plan) return [];
        const chips: string[] = [];
        if (plan.keyword) chips.push(`关键词：${plan.keyword}`);
        if (plan.mode) chips.push(`类型：${MODE_LABELS[plan.mode]}`);
        if (plan.sourceId) chips.push(`来源：${facets.sources.find((facet) => facet.id === plan.sourceId)?.label ?? plan.sourceId}`);
        if (plan.category) chips.push(`分类：${facets.categories.find((facet) => facet.id === plan.category)?.label ?? plan.category}`);
        if (plan.favoritesOnly) chips.push("仅收藏");
        return chips;
    }, [facets.categories, facets.sources, plan]);

    return (
        <AppDrawer
            open={open}
            onClose={onClose}
            title={
                <span className="flex items-center gap-2">
                    <Sparkles className="size-4 text-primary" aria-hidden="true" />
                    <span>提示词画廊</span>
                    <span className="text-[var(--fs-micro)] font-normal text-foreground/50">当前输入：{MODE_LABELS[mode]}</span>
                </span>
            }
            placement="right"
            width="min(1000px, 96vw)"
            flush
        >
            <div className="flex h-full min-h-0 flex-col">
                <div className="flex flex-col gap-3 border-b border-border/60 px-5 py-4">
                    <div className="flex flex-wrap items-center gap-2">
                        <Input
                            allowClear
                            prefix={<Search className="size-4 text-foreground/40" aria-hidden="true" />}
                            placeholder="搜标题、标签、风格或提示词正文"
                            value={keyword}
                            onChange={(event) => {
                                setKeyword(event.target.value);
                                clearPlan();
                                setPage(1);
                            }}
                            className="max-w-md min-w-[220px] flex-1"
                            aria-label="搜索提示词"
                        />
                        {provider?.planSearch ? (
                            <Tooltip title="用一句话描述你要找什么，由当前文本模型解析成检索条件">
                                <Button icon={<Sparkles className="size-4" />} loading={planning} disabled={!keyword.trim()} onClick={() => void runAiSearch()} aria-label="AI 搜索提示词">
                                    AI 搜索
                                </Button>
                            </Tooltip>
                        ) : null}
                        <Tooltip title={favoritesOnly ? "显示全部条目" : "只看我收藏的条目"}>
                            <Button type={favoritesOnly ? "primary" : "default"} icon={<Heart className="size-4" fill={favoritesOnly ? "currentColor" : "none"} />} aria-pressed={favoritesOnly} onClick={() => { setFavoritesOnly((current) => !current); clearPlan(); setPage(1); }}>
                                收藏
                            </Button>
                        </Tooltip>
                    </div>
                    <div className="flex flex-wrap items-center gap-2">
                        <SegmentedControl options={modeOptions} value={modeFilter} onChange={(value) => { setModeFilter(value as PromptGalleryMode | "all"); clearPlan(); setPage(1); }} ariaLabel="按创作类型筛选" size="sm" />
                        {/* 来源可能到十几个（站内 4 个 + 远端若干），默认高度会把后面的来源折叠掉，
                            所以放开列表高度并允许搜索，避免"来源存在但看不见"。 */}
                        <Select
                            size="sm"
                            value={sourceFilter}
                            onChange={(value) => { setSourceFilter(String(value)); clearPlan(); setPage(1); }}
                            ariaLabel="按来源筛选"
                            showSearch
                            optionFilterProp="label"
                            listHeight={420}
                            options={[{ value: "all", label: "全部来源" }, ...facets.sources.map((facet) => ({ value: facet.id, label: `${facet.label} ${facet.count}` }))]}
                            className="min-w-[150px]"
                        />
                        <Select
                            size="sm"
                            value={categoryFilter}
                            onChange={(value) => { setCategoryFilter(String(value)); clearPlan(); setPage(1); }}
                            ariaLabel="按分类筛选"
                            showSearch
                            optionFilterProp="label"
                            listHeight={420}
                            options={[{ value: "all", label: "全部分类" }, ...facets.categories.map((facet) => ({ value: facet.id, label: `${facet.label} ${facet.count}` }))]}
                            className="min-w-[150px]"
                        />
                    </div>
                    {plan ? (
                        <div className="flex flex-wrap items-center gap-2 rounded-[var(--r-md)] border border-border/60 bg-muted/25 px-3 py-2 text-[var(--fs-micro)] text-foreground/70">
                            <span className="font-medium text-foreground">AI 理解</span>
                            <span className="min-w-0 flex-1">{plan.explanation || "已按解析出的条件检索"}</span>
                            {activePlanChips.map((chip) => (
                                <span key={chip} className="rounded-full bg-background px-2 py-0.5">{chip}</span>
                            ))}
                            <Button type="link" size="small" onClick={() => { clearPlan(); setPage(1); }}>
                                清除
                            </Button>
                        </div>
                    ) : null}
                    {planError ? <Callout tone="error" title="AI 搜索失败">{planError}</Callout> : null}
                </div>

                <div className="thin-scrollbar min-h-0 flex-1 overflow-y-auto px-5 py-4">
                    {warnings.length ? (
                        <div className="mb-3 space-y-2">
                            {warnings.map((warning) => (
                                <Callout key={warning} tone="warning" title="部分结果不可用">{warning}</Callout>
                            ))}
                        </div>
                    ) : null}
                    {error ? (
                        <div className="py-10">
                            <Callout tone="error" title="画廊加载失败">{error}</Callout>
                        </div>
                    ) : loading ? (
                        <div className="flex min-h-[240px] items-center justify-center gap-2 text-[var(--fs-body)] text-foreground/60" role="status">
                            <LoaderCircle className="size-4 animate-spin motion-reduce:animate-none" aria-hidden="true" />
                            <span>正在读取提示词目录…</span>
                        </div>
                    ) : items.length === 0 ? (
                        <div className="flex min-h-[240px] flex-col items-center justify-center gap-1 text-center">
                            <p className="text-[var(--fs-body)] text-foreground/70">{favoritesOnly ? "还没有符合条件的收藏" : "没有找到匹配的提示词"}</p>
                            <p className="text-[var(--fs-micro)] text-foreground/50">换个关键词，或试试「AI 搜索」用一句话描述你要找的画面。</p>
                        </div>
                    ) : (
                        <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-3 xl:grid-cols-4">
                            {items.map((item) => {
                                const ModeIcon = MODE_ICONS[item.mode];
                                return (
                                    <article
                                        key={item.id}
                                        role="button"
                                        tabIndex={0}
                                        aria-label={`使用提示词 ${item.title}`}
                                        onClick={() => applyItem(item)}
                                        onKeyDown={(event) => onCardKeyDown(event, item)}
                                        className="group flex min-h-0 cursor-pointer flex-col overflow-hidden rounded-[var(--r-md)] border border-border/60 bg-background transition-colors hover:border-primary/50 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-primary motion-reduce:transition-none"
                                    >
                                        <div className="relative aspect-[4/3] w-full overflow-hidden bg-muted/40">
                                            {item.image ? (
                                                <img src={item.image} alt="" loading="lazy" decoding="async" referrerPolicy="no-referrer" className="size-full object-cover" />
                                            ) : (
                                                // 纯提示词清单没有封面：用文字版式交代"哪一类、什么规格"，不放无关配图。
                                                <span className="flex size-full flex-col items-center justify-center gap-1 px-3 text-center text-foreground/45">
                                                    <ModeIcon className="size-5" aria-hidden="true" />
                                                    {item.categoryLabel ? <span className="text-[11px] text-foreground/65">{item.categoryLabel}</span> : null}
                                                    {item.ratio ? <span className="text-[10px]">{item.ratio}</span> : null}
                                                </span>
                                            )}
                                            <span className="absolute top-2 left-2 flex items-center gap-1 rounded-full bg-black/55 px-2 py-0.5 text-[10px] text-white">
                                                <ModeIcon className="size-3" aria-hidden="true" />
                                                {MODE_LABELS[item.mode]}
                                            </span>
                                            <Tooltip title={item.favorite ? "取消收藏" : "收藏"}>
                                                <button
                                                    type="button"
                                                    className="absolute top-2 right-2 grid size-6 place-items-center rounded-full bg-black/55 text-white hover:bg-black/75 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-1 focus-visible:outline-white"
                                                    aria-pressed={Boolean(item.favorite)}
                                                    aria-label={item.favorite ? `取消收藏 ${item.title}` : `收藏 ${item.title}`}
                                                    onClick={(event) => {
                                                        event.stopPropagation();
                                                        toggleFavorite(item);
                                                    }}
                                                >
                                                    <Heart className="size-3.5" fill={item.favorite ? "currentColor" : "none"} aria-hidden="true" />
                                                </button>
                                            </Tooltip>
                                        </div>
                                        <div className="flex min-h-0 flex-1 flex-col gap-2 px-3 py-2.5">
                                            <h3 className="line-clamp-2 text-[var(--fs-body)] font-medium text-foreground" title={item.title}>{item.title}</h3>
                                            <p className="line-clamp-2 text-[var(--fs-micro)] leading-5 text-foreground/60">{item.description || item.prompt.slice(0, 60)}</p>
                                            <div className="mt-auto flex flex-wrap items-center gap-1 text-[10px] text-foreground/55">
                                                {item.sourceLabel ? <span className="rounded-full bg-muted/50 px-1.5 py-0.5">{item.sourceLabel}</span> : null}
                                                {item.categoryLabel ? <span className="rounded-full bg-muted/50 px-1.5 py-0.5">{item.categoryLabel}</span> : null}
                                                {item.ratio ? <span className="rounded-full bg-muted/50 px-1.5 py-0.5">{item.ratio}</span> : null}
                                                {item.credit ? <span className="truncate max-w-[9rem]" title={item.credit}>{item.credit}</span> : null}
                                                {item.sourceUrl ? (
                                                    <Tooltip title="打开来源页面">
                                                        <a
                                                            href={item.sourceUrl}
                                                            target="_blank"
                                                            rel="noreferrer"
                                                            className="ml-auto inline-flex items-center gap-0.5 text-foreground/60 hover:text-primary"
                                                            onClick={(event) => event.stopPropagation()}
                                                        >
                                                            <ExternalLink className="size-3" aria-hidden="true" />
                                                            原文
                                                        </a>
                                                    </Tooltip>
                                                ) : null}
                                            </div>
                                        </div>
                                    </article>
                                );
                            })}
                        </div>
                    )}
                </div>

                <div className="border-t border-border/60 px-5 py-2">
                    <PaginationBar current={page} pageSize={pageSize} total={total} onChange={(next, size) => { setPage(next); setPageSize(size); }} pageSizeOptions={PAGE_SIZE_OPTIONS} itemLabel="条" />
                    <p className="text-[10px] leading-4 text-foreground/45">
                        外部提示词与封面来自对应开源来源，按各自许可使用；卡片上的来源与作者即署名信息，导出或商用前请自行确认授权范围。
                    </p>
                </div>
            </div>
        </AppDrawer>
    );
}
