import { describe, expect, test } from "bun:test";
import { readFileSync } from "node:fs";
import { resolve } from "node:path";

import {
    getPromptEditorSlots,
    registerPromptEditorSlot,
    unregisterPluginPromptEditorSlots,
    unregisterPromptEditorSlot,
} from "../src/lib/plugins/prompt-editor-slot-registry";

const SLOT = "prompt-composer-actions";

describe("提示词输入框插槽注册表", () => {
    test("按 priority 降序、注册序升序排列", () => {
        registerPromptEditorSlot({ pluginId: "test-plugin-a", slot: SLOT, priority: 0, render: () => null });
        registerPromptEditorSlot({ pluginId: "test-plugin-b", slot: SLOT, priority: 10, render: () => null });
        registerPromptEditorSlot({ pluginId: "test-plugin-c", slot: SLOT, priority: 10, render: () => null });

        // 真实插件（prompt-gallery）也在同一张表里，只比较本用例注册的项。
        const ids = getPromptEditorSlots(SLOT)
            .map((slot) => slot.pluginId)
            .filter((pluginId) => pluginId.startsWith("test-plugin-"));
        expect(ids).toEqual(["test-plugin-b", "test-plugin-c", "test-plugin-a"]);

        unregisterPluginPromptEditorSlots("test-plugin-a");
        unregisterPluginPromptEditorSlots("test-plugin-b");
        unregisterPluginPromptEditorSlots("test-plugin-c");
        expect(getPromptEditorSlots(SLOT).filter((slot) => slot.pluginId.startsWith("test-plugin-"))).toHaveLength(0);
    });

    test("同一插件重复注册同一插槽会替换旧项（HMR 幂等）", () => {
        const first = registerPromptEditorSlot({ pluginId: "test-plugin-hmr", slot: SLOT, render: () => "old" });
        registerPromptEditorSlot({ pluginId: "test-plugin-hmr", slot: SLOT, render: () => "new" });

        const slots = getPromptEditorSlots(SLOT).filter((slot) => slot.pluginId === "test-plugin-hmr");
        expect(slots).toHaveLength(1);
        expect(slots[0]?.render({} as never)).toBe("new");

        // 旧实例的卸载函数不能误删新注册项。
        first();
        expect(getPromptEditorSlots(SLOT).filter((slot) => slot.pluginId === "test-plugin-hmr")).toHaveLength(1);
        unregisterPromptEditorSlot("test-plugin-hmr", SLOT);
        expect(getPromptEditorSlots(SLOT).filter((slot) => slot.pluginId === "test-plugin-hmr")).toHaveLength(0);
    });

    test("停用插件时清掉它的全部输入框动作", () => {
        registerPromptEditorSlot({ pluginId: "test-plugin-off", slot: SLOT, render: () => null });
        expect(getPromptEditorSlots(SLOT).filter((slot) => slot.pluginId === "test-plugin-off")).toHaveLength(1);
        unregisterPluginPromptEditorSlots("test-plugin-off");
        expect(getPromptEditorSlots(SLOT).filter((slot) => slot.pluginId === "test-plugin-off")).toHaveLength(0);
    });

    test("画廊插件通过插槽注册按钮，宿主组件不再内联画廊 UI", () => {
        const plugin = readFileSync(resolve(import.meta.dir, "../src/lib/plugins/builtin/prompt-gallery.ts"), "utf8");
        expect(plugin).toContain('slot: "prompt-composer-actions"');
        expect(plugin).toContain("registerPromptEditorSlot(");

        // 三个输入框都只渲染插槽，不出现抽屉/按钮的内联实现。
        for (const file of ["../src/components/canvas/canvas-node-prompt-panel.tsx", "../src/components/canvas/canvas-config-composer.tsx", "../src/pages/create/creation-workspace.tsx"]) {
            const source = readFileSync(resolve(import.meta.dir, file), "utf8");
            expect(source).toContain("<PromptEditorActions");
            expect(source).not.toContain("PromptGalleryDrawer");
            expect(source).not.toContain("用PromptGalleryProvider");
            expect(source).not.toContain("usePromptGalleryProvider");
        }
    });

    // 回归：内置插槽插件必须能在「任意页面」被加载到。
    // 病根：builtin/index 的注册是模块副作用，若它只被插件中心页引入，
    // 直接进画布/创作页时插件模块从未加载，插槽恒为空、按钮静默消失。
    test("应用入口加载内置插件，保证非插件中心页面也能拿到输入框插槽", () => {
        const entry = readFileSync(resolve(import.meta.dir, "../src/application.tsx"), "utf8");
        expect(entry).toContain('import "@/lib/plugins/builtin"');

        // builtin/index 必须仍以副作用方式引入插槽插件。
        const builtin = readFileSync(resolve(import.meta.dir, "../src/lib/plugins/builtin/index.ts"), "utf8");
        expect(builtin).toContain('import "./prompt-gallery"');
    });
});
