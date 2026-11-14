import { describe, expect, test } from "bun:test";

import { remoteCategoryOf, localCategoryOf } from "../src/lib/plugins/builtin/prompt-gallery";

// 复刻「切换来源后来源下拉合计变小」的故障链，确认修复后语义正确。
//
// 背景数据（真实库）：
//   视频：站内 seedance 767 + lanshu 604，远端 hf-seedance-2 8833   共 10204
//   图片：站内 haohaoxue 297 + youmind 255，远端 hf-gpt-image-2 21113 共 21665
//
// 故障：选中任意站内来源时，前端把该 id 直接传给远端 facet 接口查计数。
// 后端索引里没有 lanshu / seedance 这些 id，于是远端计数全部归零，
// 来源下拉里远端那一项（8833）整体消失 → 合计远小于总数。
const LOCAL_SOURCES = ["seedance", "lanshu", "haohaoxue", "youmind"] as const;
const REMOTE_SOURCES = ["hf-seedance-2", "hf-gpt-image-2"] as const;

function isRemote(sourceId: string) {
    return sourceId.startsWith("hf-");
}

/** 复刻修复后的取值：只有远端来源才允许作为远端 facet 的 sourceId。 */
function remoteSourceForFacet(selected: string | undefined, ignoreSource: boolean) {
    if (ignoreSource || !selected) return undefined;
    return isRemote(selected) ? selected : undefined;
}

describe("切换来源后来源下拉的合计不应缩水", () => {
    test("选中任意站内来源时，不进远端接口的 sourceId", () => {
        for (const sourceId of LOCAL_SOURCES) {
            // 关键断言：把站内 id 传给远端会让远端计数归零。
            expect(remoteSourceForFacet(sourceId, false)).toBeUndefined();
        }
    });

    test("选中远端来源时，仍然传它（用于分类维度按来源收窄）", () => {
        for (const sourceId of REMOTE_SOURCES) {
            expect(remoteSourceForFacet(sourceId, false)).toBe(sourceId);
        }
    });

    test("来源维度忽略已选来源，始终能列出全部来源", () => {
        // 无论选了哪个来源，来源维度的远端请求都不带 sourceId，
        // 这样下拉里所有来源都在，合计才能与总数对齐。
        for (const sourceId of [...LOCAL_SOURCES, ...REMOTE_SOURCES, undefined]) {
            expect(remoteSourceForFacet(sourceId, true)).toBeUndefined();
        }
    });

    test("未选来源时不带 sourceId", () => {
        expect(remoteSourceForFacet(undefined, false)).toBeUndefined();
        expect(remoteSourceForFacet("all", false)).toBeUndefined();
    });
});

describe("分类前缀在两个方向上仍然自洽", () => {
    test("远端分类能还原、站内分类不受影响", () => {
        expect(remoteCategoryOf("hf:Entertainment")).toBe("Entertainment");
        expect(remoteCategoryOf("cinematic")).toBeUndefined();
        expect(localCategoryOf("hf:Entertainment")).toBeUndefined();
        expect(localCategoryOf("cinematic")).toBe("cinematic");
    });
});
