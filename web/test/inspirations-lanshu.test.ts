import { describe, expect, test } from "bun:test";
import { readFileSync } from "node:fs";
import { resolve } from "node:path";

import type { CreationInspiration } from "../src/lib/inspirations/catalog";
import { inspirationSources } from "../src/lib/inspirations/catalog";

type ShippedCatalog = {
    source: string;
    revision: string;
    count: number;
    categories: Record<string, string>;
    entries: CreationInspiration[];
};

function readCatalog(file: string): ShippedCatalog {
    return JSON.parse(readFileSync(resolve(import.meta.dir, "../public/inspirations", file), "utf8")) as ShippedCatalog;
}

const lanshu = readCatalog("lanshu.json");

describe("蓝书视频提示词源（无配图来源）", () => {
    test("每条都有可检索的正文、分类、署名与来源链接", () => {
        expect(lanshu.count).toBe(lanshu.entries.length);
        expect(lanshu.entries.length).toBeGreaterThan(400);
        for (const entry of lanshu.entries) {
            expect(entry.id?.startsWith("lanshu:")).toBe(true);
            expect(entry.title.trim().length).toBeGreaterThan(0);
            expect(entry.prompt.trim().length).toBeGreaterThan(20);
            expect(entry.mode).toBe("video");
            expect(entry.sourceId).toBe("lanshu");
            expect(entry.category).toBeTruthy();
            expect(entry.credit).toBeTruthy();
            expect(entry.sourceUrl?.startsWith("http")).toBe(true);
            // 模型 id 进 tags，画廊才能靠"seedance"这类词搜到配方。
            expect(Array.isArray(entry.tags)).toBe(true);
        }
    });

    test("上游没有配图，条目不带 image 字段", () => {
        expect(lanshu.entries.some((entry) => "image" in entry)).toBe(false);
    });

    test("单模型配方与跨模型对照都在同一来源里", () => {
        const matrix = lanshu.entries.filter((entry) => (entry.tags ?? []).includes("跨模型对照"));
        expect(matrix).toHaveLength(110);
        expect(lanshu.entries.length - matrix.length).toBe(494);
        expect(lanshu.entries).toHaveLength(604);
        // 对照条目是"同一场景 × 不同模型"，标题必须带上模型名才区分得开。
        expect(new Set(matrix.map((entry) => entry.title)).size).toBe(matrix.length);
        expect(matrix.every((entry) => entry.credit === "蓝书跨模型对照矩阵")).toBe(true);
    });

    test("分类与规格带中文信息，卡片文字版式才有内容可显示", () => {
        const translated = Object.values(lanshu.categories).filter((label) => /[\u4e00-\u9fff]/.test(label));
        expect(translated.length).toBe(lanshu.categories ? Object.keys(lanshu.categories).length : 0);
        expect(lanshu.entries.filter((entry) => entry.ratio).length).toBeGreaterThan(lanshu.entries.length / 2);
    });

    test("来源表登记了许可与署名说明", () => {
        const info = inspirationSources.lanshu;
        expect(info.label).toContain("MIT");
        expect(info.repository).toContain("lanshu-awesome-ai-video-kit");
        expect(info.license).toContain("MIT");
        expect(info.notice).toContain("公开文档");
        expect(info.revision).toBeTruthy();
    });
});

describe("无封面来源的渲染兜底", () => {
    test("灵感页卡片与详情弹窗都为缺失封面留了文字版式", () => {
        const page = readFileSync(resolve(import.meta.dir, "../src/pages/inspirations/index.tsx"), "utf8");
        const detail = readFileSync(resolve(import.meta.dir, "../src/pages/inspirations/inspiration-detail-modal.tsx"), "utf8");

        // 卡片：有图渲染 img，无图走文字分支（分类 + 规格 + 来源名）。
        expect(page).toContain("{item.image ? (");
        expect(page).toContain("{item.ratio ? <span");
        // 详情弹窗：没有封面就整块不渲染，不能留一个空的破图框。
        expect(detail).toContain("{item.image ? (");

        // 来源筛选顺序必须覆盖新来源，否则条目筛不到。
        expect(page).toContain('"lanshu"');
    });

    test("目录加载与画廊条目映射都带上新来源", () => {
        const catalogService = readFileSync(resolve(import.meta.dir, "../src/services/inspiration-catalog.ts"), "utf8");
        expect(catalogService).toContain('{ key: "lanshu", file: "/inspirations/lanshu.json" }');

        const mapping = readFileSync(resolve(import.meta.dir, "../src/lib/inspirations/prompt-gallery.ts"), "utf8");
        expect(mapping).toContain("ratio: item.ratio");
    });
});
