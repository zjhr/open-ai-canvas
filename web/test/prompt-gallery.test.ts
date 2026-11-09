import { afterAll, beforeAll, describe, expect, test } from "bun:test";
import { readFileSync } from "node:fs";
import { resolve } from "node:path";

import type { CreationInspiration } from "../src/lib/inspirations/catalog";
import { buildPromptGalleryItems, normalizePromptGalleryPlan, planMergedPage, PROMPT_GALLERY_PAGE_SIZE, promptGalleryFacets, searchPromptGalleryItems } from "../src/lib/inspirations/prompt-gallery";
import { PROMPT_GALLERY_PLUGIN_ID, PROMPT_GALLERY_REMOTE_SOURCE_ID, PROMPT_GALLERY_SOURCE_ID, promptGalleryPlugin } from "../src/lib/plugins/builtin/prompt-gallery";
import type { PluginHostContext } from "../src/lib/plugins/plugin-types";

const entries: CreationInspiration[] = [
    {
        id: "seedance:rainy-street",
        title: "雨夜街道",
        description: "霓虹倒影里的跟拍镜头",
        image: "/inspiration-thumbs/seedance/a.webp",
        mode: "video",
        prompt: "rainy neon street, handheld follow shot",
        promptZh: "雨夜霓虹街道，手持跟拍镜头",
        sourceId: "seedance",
        category: "handheld-ugc-vlog",
        tags: ["夜景"],
        credit: "作者甲",
        sourceUrl: "https://example.com/a",
    },
    {
        id: "youmind:city-portrait",
        title: "城市人像",
        description: "硬光与特写",
        image: "/inspiration-thumbs/youmind/b.webp",
        mode: "image",
        prompt: "hard light city portrait, 85mm",
        sourceId: "youmind",
        category: "portrait",
    },
    {
        title: "自带原创",
        description: "本项目编写的文本模板",
        image: "/short-drama-styles/cyberpunk-neon.jpg",
        mode: "text",
        prompt: "写一场发生在客厅的母女对话戏",
    },
];

const items = buildPromptGalleryItems(entries, {
    favorites: new Set(["youmind:city-portrait"]),
    categoryLabel: (item) => (item.category === "portrait" ? "人像" : undefined),
});

describe("prompt gallery catalog projection", () => {
    test("prefers the Chinese prompt so the card shows what gets inserted", () => {
        const video = items.find((item) => item.id === "seedance:rainy-street");
        expect(video?.prompt).toBe("雨夜霓虹街道，手持跟拍镜头");
        expect(video?.sourceLabel).toContain("CC BY 4.0");
        expect(video?.credit).toBe("作者甲");
        expect(video?.sourceUrl).toBe("https://example.com/a");
        expect(video?.license).toContain("CC BY 4.0");
    });

    test("keeps curated entries addressable and labels their categories", () => {
        const original = items.find((item) => item.mode === "text");
        expect(original?.id).toBe("original:自带原创");
        expect(original?.sourceLabel).toBe("原创提示词");
        expect(items.find((item) => item.id === "youmind:city-portrait")?.categoryLabel).toBe("人像");
    });
});

describe("prompt gallery search", () => {
    test("matches the prompt body, not just the title", () => {
        const result = searchPromptGalleryItems(items, { keyword: "85mm" });
        expect(result.total).toBe(1);
        expect(result.items[0]?.id).toBe("youmind:city-portrait");
    });

    test("filters by mode, source and favorites without dropping the total", () => {
        expect(searchPromptGalleryItems(items, { mode: "video" }).total).toBe(1);
        expect(searchPromptGalleryItems(items, { sourceId: "youmind" }).total).toBe(1);
        expect(searchPromptGalleryItems(items, { sourceId: "all", favoritesOnly: true }).total).toBe(1);
        expect(searchPromptGalleryItems(items, { mode: "image", favoritesOnly: true }).total).toBe(1);
    });

    test("keeps favorites on top and pages with the default page size", () => {
        const sorted = searchPromptGalleryItems(items, {}).items;
        expect(sorted[0]?.id).toBe("youmind:city-portrait");

        const many = Array.from({ length: PROMPT_GALLERY_PAGE_SIZE + 5 }, (_, index) => ({ ...items[0]!, id: `bulk-${index}` }));
        const page = searchPromptGalleryItems(many, {});
        expect(page.total).toBe(PROMPT_GALLERY_PAGE_SIZE + 5);
        expect(page.items).toHaveLength(PROMPT_GALLERY_PAGE_SIZE);
        expect(searchPromptGalleryItems(many, { offset: PROMPT_GALLERY_PAGE_SIZE }).items).toHaveLength(5);
    });
});

describe("prompt gallery facets", () => {
    test("counts sources and keeps only sources with entries", () => {
        const facets = promptGalleryFacets(items);
        expect(facets.sources.map((facet) => facet.id).sort()).toEqual(["original", "seedance", "youmind"]);
        expect(facets.sources.find((facet) => facet.id === "seedance")?.count).toBe(1);
        expect(facets.categories.find((facet) => facet.id === "portrait")?.label).toBe("人像");
    });
});

describe("prompt gallery facets follow the active filters", () => {
    test("counts only the rows matching the other filters, not the whole catalog", () => {
        // 只看视频：图片来源与图片分类都不该再出现在下拉里。
        const video = promptGalleryFacets(items, { mode: "video" });
        expect(video.sources.map((facet) => facet.id)).toEqual(["seedance"]);
        expect(video.categories.map((facet) => facet.id)).toEqual(["handheld-ugc-vlog"]);

        const image = promptGalleryFacets(items, { mode: "image" });
        expect(image.sources.map((facet) => facet.id)).toEqual(["youmind"]);
    });

    test("ignores its own dimension so the current choice stays switchable", () => {
        // 已在某个来源下：来源维度仍要列出全部来源，否则用户换不回去。
        const bySource = promptGalleryFacets(items, { sourceId: "seedance" });
        expect(bySource.sources.map((facet) => facet.id).sort()).toEqual(["original", "seedance", "youmind"]);
        // 分类维度收窄到该来源。
        expect(bySource.categories.map((facet) => facet.id)).toEqual(["handheld-ugc-vlog"]);

        // 已在某个分类下：分类维度仍要列出全部分类，来源维度收窄到该分类。
        const byCategory = promptGalleryFacets(items, { category: "portrait" });
        expect(byCategory.categories.map((facet) => facet.id).sort()).toEqual(["handheld-ugc-vlog", "portrait"]);
        expect(byCategory.sources.map((facet) => facet.id)).toEqual(["youmind"]);
    });

    test("keyword and favorites narrow the counts the same way as the search", () => {
        const byKeyword = promptGalleryFacets(items, { keyword: "85mm" });
        expect(byKeyword.sources.map((facet) => facet.id)).toEqual(["youmind"]);
        expect(byKeyword.categories.map((facet) => facet.id)).toEqual(["portrait"]);

        const favorites = promptGalleryFacets(items, { favoritesOnly: true });
        expect(favorites.sources.map((facet) => facet.id)).toEqual(["youmind"]);
    });

    test("the number shown equals what the search returns once that value is selected", () => {
        // 这条不变量就是回归本体：下拉标 8000、选中只出来 2000 的日子不能回来。
        const video = promptGalleryFacets(items, { mode: "video" });
        for (const facet of video.sources) {
            expect(searchPromptGalleryItems(items, { mode: "video", sourceId: facet.id }).total).toBe(facet.count);
        }
        const image = promptGalleryFacets(items, { mode: "image" });
        for (const facet of image.categories) {
            expect(searchPromptGalleryItems(items, { mode: "image", category: facet.id }).total).toBe(facet.count);
        }
    });
});

describe("AI search plan normalization", () => {
    test("accepts keyword arrays and known filter ids", () => {
        const plan = normalizePromptGalleryPlan(
            { keywords: ["雨夜", "霓虹"], mode: "video", sourceId: "seedance", category: "handheld-ugc-vlog", favoritesOnly: true, explanation: "雨夜霓虹的手持镜头" },
            { mode: "image" },
            { sourceIds: new Set(["seedance"]), categoryIds: new Set(["handheld-ugc-vlog"]) },
        );
        expect(plan.keyword).toBe("雨夜 霓虹");
        expect(plan.mode).toBe("video");
        expect(plan.sourceId).toBe("seedance");
        expect(plan.category).toBe("handheld-ugc-vlog");
        expect(plan.favoritesOnly).toBe(true);
        expect(plan.explanation).toBe("雨夜霓虹的手持镜头");
    });

    test("drops hallucinated filters and falls back to the current mode", () => {
        const plan = normalizePromptGalleryPlan(
            { keyword: "cinematic", mode: "audio", sourceId: "not-a-source", category: "not-a-category" },
            { mode: "video", sourceId: "youmind" },
            { sourceIds: new Set(["seedance"]), categoryIds: new Set(["portrait"]) },
        );
        expect(plan.mode).toBe("video");
        expect(plan.sourceId).toBeUndefined();
        expect(plan.category).toBeUndefined();
    });

    test("survives a non-object payload", () => {
        expect(normalizePromptGalleryPlan("nope", { mode: "image" }).mode).toBe("image");
    });
});

describe("prompt gallery plugin manifest", () => {
    test("declares the catalog source and the text-model permission", () => {
        const manifest = promptGalleryPlugin.manifest;
        expect(manifest.id).toBe(PROMPT_GALLERY_PLUGIN_ID);
        expect(manifest.id).toMatch(/^[a-z0-9]+(?:-[a-z0-9]+)*$/);
        expect(manifest.apiVersion).toBe("yingce.plugin/v2");
        expect(manifest.permissions).toEqual(["ai.text"]);
        expect(manifest.surfaces).toContain("prompt-gallery");
        expect(manifest.contributes.promptSources).toEqual([PROMPT_GALLERY_SOURCE_ID, PROMPT_GALLERY_REMOTE_SOURCE_ID]);
        // 输入框按钮走插槽声明；v2 清单里 editorSlots 与 promptEditorSlots 是并列贡献。
        expect(manifest.contributes.promptEditorSlots).toEqual([{ slot: "prompt-composer-actions", priority: 10 }]);
        expect(typeof promptGalleryPlugin.createPromptGallery).toBe("function");
    });
});

/** 占位 storage：provider 的检索路径不落盘，只需要满足宿主上下文形状。 */
function galleryContext(overrides: Partial<PluginHostContext> = {}): PluginHostContext {
    return {
        manifest: promptGalleryPlugin.manifest,
        permissions: new Set(promptGalleryPlugin.manifest.permissions),
        storage: { get: async () => null, set: async () => {}, remove: async () => {} },
        config: {},
        ...overrides,
    };
}

function catalogFileEntries(file: string): CreationInspiration[] {
    return (JSON.parse(readFileSync(resolve(import.meta.dir, "../public/inspirations", file), "utf8")) as { entries: CreationInspiration[] }).entries;
}

describe("prompt gallery provider over the shipped catalog", () => {
    const originalFetch = globalThis.fetch;
    beforeAll(() => {
        // 真实目录是构建产物（web/public/inspirations/*.json）；测试直接读盘，形状与线上一致。
        globalThis.fetch = (async (input: RequestInfo | URL) => {
            const url = typeof input === "string" ? input : input instanceof URL ? input.pathname : input.url;
            if (!url.startsWith("/inspirations/")) return originalFetch(input as RequestInfo);
            const body = readFileSync(resolve(import.meta.dir, "../public", url.slice(1)), "utf8");
            return new Response(body, { status: 200, headers: { "Content-Type": "application/json" } });
        }) as typeof fetch;
    });
    afterAll(() => {
        globalThis.fetch = originalFetch;
    });

    test("searches the loaded catalog with mode filters and pagination", async () => {
        const provider = promptGalleryPlugin.createPromptGallery!(galleryContext());
        const result = await provider.search({ mode: "video", limit: 5 });
        expect(result.items).toHaveLength(5);
        // video 结果 = seedance 全部 + 其它来源与原创里的视频条目，因此只断言下界。
        expect(result.total).toBeGreaterThanOrEqual(catalogFileEntries("seedance.json").length);
        for (const item of result.items) {
            expect(item.mode).toBe("video");
            expect(item.prompt.trim().length).toBeGreaterThan(0);
            expect(item.title.trim().length).toBeGreaterThan(0);
        }

        const nextPage = await provider.search({ mode: "video", limit: 5, offset: 5 });
        expect(nextPage.items.map((item) => item.id)).not.toEqual(result.items.map((item) => item.id));
    });

    test("finds entries by prompt body keyword and restricts to a single source", async () => {
        const provider = promptGalleryPlugin.createPromptGallery!(galleryContext());
        const sourceEntries = catalogFileEntries("youmind.json");
        const target = sourceEntries[0]!;
        const keyword = target.title.slice(0, 2);

        const scoped = await provider.search({ sourceId: "youmind", keyword, limit: PROMPT_GALLERY_PAGE_SIZE });
        expect(scoped.total).toBeGreaterThan(0);
        for (const item of scoped.items) {
            expect(item.sourceId).toBe("youmind");
        }
        expect(scoped.items.some((item) => item.id === (target.id ?? `youmind:${target.title}`))).toBe(true);
    });

    test("reports facets that add up to the catalog it actually serves", async () => {
        const provider = promptGalleryPlugin.createPromptGallery!(galleryContext());
        const facets = await provider.facets!();
        const everything = await provider.search({ limit: 1 });

        expect(facets.sources.reduce((sum, facet) => sum + facet.count, 0)).toBe(everything.total);
        const seedance = facets.sources.find((facet) => facet.id === "seedance");
        expect(seedance?.count).toBe(catalogFileEntries("seedance.json").length);
        expect(seedance?.label).toContain("CC BY 4.0");
        expect(facets.categories.length).toBeGreaterThan(0);
    });

    test("counts follow the active filter over the shipped catalog", async () => {
        // 真实目录上的同一条不变量：某个筛选下的来源计数之和 == 该筛选的结果总数，
        // 且每个来源的计数 == 再加上该来源之后的结果数。
        const provider = promptGalleryPlugin.createPromptGallery!(galleryContext());
        const facets = await provider.facets!({ mode: "video" });
        const searched = await provider.search({ mode: "video", limit: 1 });

        expect(facets.sources.reduce((sum, facet) => sum + facet.count, 0)).toBe(searched.total);
        expect(facets.sources.some((facet) => facet.id === "youmind")).toBe(false);
        for (const facet of facets.sources) {
            const scoped = await provider.search({ mode: "video", sourceId: facet.id, limit: 1 });
            expect(scoped.total).toBe(facet.count);
        }
    });

    test("keeps ordinary search working and surfaces a readable error without the text service", async () => {
        const provider = promptGalleryPlugin.createPromptGallery!(galleryContext());
        const plain = await provider.search({ keyword: "   " });
        expect(plain.total).toBeGreaterThan(0);

        await expect(provider.planSearch!("雨夜霓虹的手持镜头")).rejects.toThrow("文本模型服务");
    });

    test("serves a no-cover source through the same provider", async () => {
        const provider = promptGalleryPlugin.createPromptGallery!(galleryContext());
        const result = await provider.search({ sourceId: "lanshu", limit: 5 });
        expect(result.total).toBe(catalogFileEntries("lanshu.json").length);
        for (const item of result.items) {
            expect(item.sourceId).toBe("lanshu");
            // 无封面来源必须把 image 留空，由宿主渲染文字版式，而不是塞一个假地址。
            expect(item.image).toBeUndefined();
            expect(item.sourceLabel).toContain("MIT");
            expect(item.prompt.length).toBeGreaterThan(20);
        }

        // 模型 id 进了 tags，搜模型名能命中这些配方。
        const byModel = await provider.search({ sourceId: "lanshu", keyword: "seedance", limit: 5 });
        expect(byModel.total).toBeGreaterThan(0);

        const facets = await provider.facets!();
        expect(facets.sources.find((facet) => facet.id === "lanshu")?.label).toContain("MIT");
    });

    test("degrades to the local catalog with a visible warning when the remote index is unreachable", async () => {
        const provider = promptGalleryPlugin.createPromptGallery!(galleryContext());

        // 全部来源：远端失败不影响站内结果，但必须带 warning。
        const all = await provider.search({ limit: 5 });
        expect(all.items.length).toBeGreaterThan(0);
        expect(all.total).toBeGreaterThan(0);
        expect(all.warnings?.length).toBe(1);
        expect(all.warnings?.[0]).toContain("远端提示词索引暂时不可用");

        // 只查远端来源：没有站内兜底，结果为空但同样要说清原因。
        const remoteOnly = await provider.search({ sourceId: "hf-seedance-2", limit: 5 });
        expect(remoteOnly.items).toHaveLength(0);
        expect(remoteOnly.total).toBe(0);
        expect(remoteOnly.warnings?.length).toBe(1);

        // facet 在远端不可用时回落到站内，不抛错。
        const facets = await provider.facets!();
        expect(facets.sources.length).toBeGreaterThan(0);
        expect(facets.sources.some((facet) => facet.id.startsWith("hf-"))).toBe(false);
    });
});

describe("跨端分页切分（站内优先、远端接续）", () => {
    test("页完全落在站内时不请求远端", () => {
        expect(planMergedPage(100, 0, 24)).toEqual({ localOffset: 0, localLimit: 24, remoteOffset: 0, remoteLimit: 0 });
        expect(planMergedPage(100, 72, 24)).toEqual({ localOffset: 72, localLimit: 24, remoteOffset: 0, remoteLimit: 0 });
    });

    test("页跨过交界处时两边各取一段", () => {
        // 站内 30 条：第 2 页（offset 24, limit 24）→ 站内 6 条 + 远端从 0 取 18 条
        expect(planMergedPage(30, 24, 24)).toEqual({ localOffset: 24, localLimit: 6, remoteOffset: 0, remoteLimit: 18 });
    });

    test("页完全落在远端时站内不再取数，远端 offset 减掉站内总数", () => {
        expect(planMergedPage(30, 48, 24)).toEqual({ localOffset: 30, localLimit: 0, remoteOffset: 18, remoteLimit: 24 });
    });

    test("站内为空时整页都取远端", () => {
        expect(planMergedPage(0, 24, 24)).toEqual({ localOffset: 0, localLimit: 0, remoteOffset: 24, remoteLimit: 24 });
    });
});
