import { describe, expect, test } from "bun:test";

import { localCategoryOf, remoteCategoryOf } from "../src/lib/plugins/builtin/prompt-gallery";

// 「选了来源/分类却没结果」的根因是两套分类命名空间被合并进同一个下拉：
//   远端：Entertainment / Content Creation / Commercial（英文短语）
//   站内：handheld-ugc-vlog / cinematic / product-commercial（kebab 码）
// 合并后选中任意一项都只能命中一侧。修复方式是给远端分类加 `hf:` 前缀，
// 并让两个检索方向各自剥离对方的前缀。这里钉住这层转换。
describe("提示词画廊分类命名空间隔离", () => {
    test("远端分类前缀可还原成接口原值", () => {
        expect(remoteCategoryOf("hf:Entertainment")).toBe("Entertainment");
        expect(remoteCategoryOf("hf:Content Creation")).toBe("Content Creation");
    });

    test("站内分类不会被误当成远端分类", () => {
        expect(remoteCategoryOf("handheld-ugc-vlog")).toBeUndefined();
        expect(remoteCategoryOf("cinematic")).toBeUndefined();
        expect(remoteCategoryOf(undefined)).toBeUndefined();
        expect(remoteCategoryOf("")).toBeUndefined();
    });

    test("选中远端分类时站内不再按它过滤", () => {
        // 关键：返回 undefined 表示「站内不限分类」，而不是「站内匹配不到」。
        // 之前直接把 hf:Entertainment 塞给站内过滤，导致站内被全部排除。
        expect(localCategoryOf("hf:Entertainment")).toBeUndefined();
        expect(localCategoryOf("hf:Content Creation")).toBeUndefined();
    });

    test("站内分类原样交给站内检索", () => {
        expect(localCategoryOf("handheld-ugc-vlog")).toBe("handheld-ugc-vlog");
        expect(localCategoryOf("cinematic")).toBe("cinematic");
        expect(localCategoryOf(undefined)).toBeUndefined();
    });

    test("两个方向的转换互不串味", () => {
        const cases = ["hf:Entertainment", "cinematic", "hf:Commercial", "product-commercial"];
        for (const category of cases) {
            const remote = remoteCategoryOf(category);
            const local = localCategoryOf(category);
            // 同一个分类最多只能属于一侧，绝不能两边都命中。
            expect(remote === undefined || local === undefined).toBe(true);
        }
    });

    test("远端分类带空格也能正确剥离", () => {
        // 「Content Creation」这类带空格的英文短语是真实数据里最常见的形态。
        expect(remoteCategoryOf("hf:Content Creation")).toBe("Content Creation");
        expect(remoteCategoryOf("hf:")).toBe("");
    });
});
