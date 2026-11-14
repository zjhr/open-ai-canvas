import { describe, expect, test } from "bun:test";

// 复现「选了来源之后没有结果」的根因：
//   facets 把站内与远端的来源计数**合并**展示，
//   但 search 一看到具体 sourceId 就只查站内（远端来源除外）。
//   于是下拉里一个「远端来源」在「全部来源」视图下被统计出非零计数，
//   用户选中它走 searchRemoteOnly 是有结果的；
//   但站内来源之外、计数又来自远端的组合会落空。
//
// 这里用最小数据把「计数」与「选中后可取回」两个口径并排放，钉住不变式：
//   任何来源的展示计数，必须等于选中它之后能拿到的条数。
type Item = { id: string; sourceId: string; mode: "image" | "video" };

const LOCAL: Item[] = [
    { id: "l1", sourceId: "seedance", mode: "video" },
    { id: "l2", sourceId: "seedance", mode: "video" },
    { id: "l3", sourceId: "lanshu", mode: "video" },
    { id: "l4", sourceId: "youmind", mode: "image" },
];
const REMOTE: Item[] = [
    { id: "r1", sourceId: "hf-seedance-2", mode: "video" },
    { id: "r2", sourceId: "hf-seedance-2", mode: "video" },
    { id: "r3", sourceId: "hf-gpt-image-2", mode: "image" },
    { id: "r4", sourceId: "hf-gpt-image-2", mode: "image" },
];

/** 复刻 facets 的合并口径：站内 + 远端一起统计。 */
function facetsMergedCounts(mode: "image" | "video") {
    const counts = new Map<string, number>();
    for (const item of [...LOCAL, ...REMOTE]) {
        if (item.mode !== mode) continue;
        counts.set(item.sourceId, (counts.get(item.sourceId) ?? 0) + 1);
    }
    return counts;
}

/** 复刻 search 的实际取数口径。 */
function searchableCount(sourceId: string, mode: "image" | "video") {
    if (sourceId.startsWith("hf-")) {
        return REMOTE.filter((item) => item.sourceId === sourceId && item.mode === mode).length;
    }
    return LOCAL.filter((item) => item.sourceId === sourceId && item.mode === mode).length;
}

describe("提示词画廊来源计数与可取回条数的不变式", () => {
    test("每个来源的展示计数都等于选中后能拿到的条数", () => {
        for (const mode of ["image", "video"] as const) {
            for (const [sourceId, count] of facetsMergedCounts(mode)) {
                expect({ mode, sourceId, count }).toEqual({ mode, sourceId, count: searchableCount(sourceId, mode) });
            }
        }
    });

    test("站内来源在视频模式下计数非零时确实有结果", () => {
        const counts = facetsMergedCounts("video");
        for (const [sourceId, count] of counts) {
            if (sourceId.startsWith("hf-")) continue;
            if (count === 0) continue;
            // 这是真正会让用户看到「选了却没结果」的场景。
            expect(searchableCount(sourceId, "video")).toBeGreaterThan(0);
        }
    });
});
