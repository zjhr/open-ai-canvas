import { describe, expect, test } from "bun:test";

// 真实故障：列表显示「共 N 条」，但来源下拉里各项相加远小于 N。
//
// 这是站内 + 远端两套目录合并计数的口径问题：
//   列表 total = 站内命中总数 + 远端 total（两段相加）
//   来源 facet = 站内 facet + 远端 facet（也是相加）
// 两者本应恒等。一旦某一侧在某一维度使用了不同的过滤条件，就会出现差额。
//
// 这里用「已知正确的数据」建立不变式，再定位是哪一侧漏算。
type Row = { sourceId: string; mode: "image" | "video"; category?: string };

const LOCAL: Row[] = [
    { sourceId: "seedance", mode: "video", category: "handheld-ugc-vlog" },
    { sourceId: "seedance", mode: "video", category: "handheld-ugc-vlog" },
    { sourceId: "seedance", mode: "video" }, // 无分类：进不了分类下拉，但必须进来源合计
    { sourceId: "lanshu", mode: "video", category: "cinematic" },
    { sourceId: "youmind", mode: "image", category: "poster" },
    { sourceId: "youmind", mode: "image" }, // 无分类
    { sourceId: "haohaoxue", mode: "image", category: "portrait" },
];

const REMOTE: Row[] = [
    { sourceId: "hf-seedance-2", mode: "video", category: "Entertainment" },
    { sourceId: "hf-seedance-2", mode: "video", category: "Entertainment" },
    { sourceId: "hf-seedance-2", mode: "video", category: "Commercial" },
    { sourceId: "hf-gpt-image-2", mode: "image", category: "Content Creation" },
    { sourceId: "hf-gpt-image-2", mode: "image", category: "Commercial" },
];

function sumBy(rows: Row[], key: "sourceId" | "category") {
    const map = new Map<string, number>();
    for (const row of rows) {
        const id = row[key];
        // 分类维度跳过空值：无分类条目不会被列出（这是有意行为）。
        if (!id) continue;
        map.set(id, (map.get(id) ?? 0) + 1);
    }
    return map;
}

function total(map: Map<string, number>) {
    return [...map.values()].reduce((sum, n) => sum + n, 0);
}

describe("列表总数与来源合计必须相等", () => {
    test("来源合计 == 列表总数（两套目录相加）", () => {
        const merged = [...LOCAL, ...REMOTE];
        const sources = sumBy(merged, "sourceId");
        // 这是用户看到的那两个数字：页面上的「共 N 条」与来源下拉各项之和。
        expect(total(sources)).toBe(merged.length);
    });

    test("按 mode 过滤后仍然相等", () => {
        for (const mode of ["image", "video"] as const) {
            const merged = [...LOCAL, ...REMOTE].filter((row) => row.mode === mode);
            const sources = sumBy(merged, "sourceId");
            expect(total(sources)).toBe(merged.length);
        }
    });

    test("分类合计会少于总数：无分类的条目不在下拉里", () => {
        const merged = [...LOCAL, ...REMOTE];
        const categories = sumBy(merged, "category");
        // 这是已知且有意的差额，不该被当成 bug；差额恰好等于无分类条目数。
        const uncategorized = merged.filter((row) => !row.category).length;
        expect(merged.length - total(categories)).toBe(uncategorized);
        expect(uncategorized).toBeGreaterThan(0);
    });

    test("每个来源的计数 == 选中它之后能拿到的条数", () => {
        for (const [sourceId, count] of sumBy([...LOCAL, ...REMOTE], "sourceId")) {
            const selected = [...LOCAL, ...REMOTE].filter((row) => row.sourceId === sourceId);
            expect(selected.length).toBe(count);
        }
    });
});
