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

    // 回归：内置插件必须在「任意页面」都能被加载到。
    // 病根：builtin/index 的注册是模块副作用，若它只被插件中心页引入，
    // 直接进画布/创作页时插件模块从未加载，插槽恒为空、按钮静默消失。
    test("应用入口加载内置插件，保证非插件中心页面也能拿到输入框插槽", () => {
        const entry = readFileSync(resolve(import.meta.dir, "../src/application.tsx"), "utf8");
        expect(entry).toContain('import "@/lib/plugins/builtin"');
    });

    // 收口：内置插件只能有一个加载来源，禁止页面再自行副作用 import。
    // 否则新增页面/插件时容易再出现"某条路径漏注册"的静默故障。
    test("内置插件只有一个加载入口，页面不再各自副作用 import", () => {
        const root = resolve(import.meta.dir, "../src");
        const builtin = readFileSync(resolve(root, "lib/plugins/builtin/index.ts"), "utf8");

        // 所有内置插件都经由 builtin/index 加载。
        for (const plugin of ["eagle", "prompt-optimizer", "prompt-gallery", "workflows", "ai-art-critique", "media-conversion", "editor/editor-shell"]) {
            expect(builtin).toContain(`import "./${plugin}";`);
        }

        // 曾在页面里单独副作用 import 的三个插件，现在不得再出现裸 import。
        // 允许 `import { ... } from "..."`（值/类型依赖），只禁止 `import "..."`（副作用依赖）。
        for (const [file, plugin] of [
            ["pages/canvas/project.tsx", "ai-art-critique"],
            ["pages/canvas/project.tsx", "media-conversion"],
            ["pages/projects/detail/editor.tsx", "editor/editor-shell"],
        ] as const) {
            const source = readFileSync(resolve(root, file), "utf8");
            expect(source).not.toContain(`import "@/lib/plugins/builtin/${plugin}";`);
        }
    });
});
