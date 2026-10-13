/**
 * 提示词输入框插槽注册表。
 *
 * 输入框（画布节点面板、生成配置组装框、创作页输入框）只渲染这一个按钮区，
 * 插件把自己的按钮注册进来；新增按钮不再改输入框组件。
 *
 * 与 editor-slot-registry 分开：编辑器插槽的渲染上下文是编辑器（时间线/预览/导出），
 * 输入框插槽的上下文是提示词（创作类型、当前正文、回填函数），两者契约不同，
 * 混在一张表里会让插件拿到不相关的上下文。
 */
import { useEffect, useReducer } from "react";

import type { PromptEditorSlotContext, PromptEditorSlotKind } from "./plugin-types";

export type PromptEditorSlotRenderer = (context: PromptEditorSlotContext) => React.ReactNode;

export type PromptEditorSlotRegistration = {
    id: number;
    pluginId: string;
    slot: PromptEditorSlotKind;
    priority: number;
    order: number;
    render: PromptEditorSlotRenderer;
};

export type RegisterPromptEditorSlotInput = {
    pluginId: string;
    slot: PromptEditorSlotKind;
    priority?: number;
    render: PromptEditorSlotRenderer;
};

let nextOrder = 0;
let nextId = 0;
const registrations: PromptEditorSlotRegistration[] = [];
const listeners = new Set<() => void>();

function emitChange(): void {
    for (const listener of listeners) listener();
}

/** 订阅插槽注册表变化（插件启停时输入框按钮区刷新）。 */
export function subscribePromptEditorSlots(listener: () => void): () => void {
    listeners.add(listener);
    return () => listeners.delete(listener);
}

/** React hook：订阅插槽变化并返回当前插槽贡献（按 priority 降序、注册序升序）。 */
export function usePromptEditorSlots(slot: PromptEditorSlotKind): PromptEditorSlotRegistration[] {
    const [, force] = useReducer((x: number) => x + 1, 0);
    useEffect(() => subscribePromptEditorSlots(force), []);
    return getPromptEditorSlots(slot);
}

/** 注册一个输入框插槽贡献。同 pluginId + slot 重复注册会覆盖旧项（幂等，兼容 HMR）。 */
export function registerPromptEditorSlot(input: RegisterPromptEditorSlotInput): () => void {
    unregisterPromptEditorSlot(input.pluginId, input.slot);
    const id = nextId++;
    registrations.push({
        id,
        pluginId: input.pluginId,
        slot: input.slot,
        priority: input.priority ?? 0,
        order: nextOrder++,
        render: input.render,
    });
    emitChange();
    return () => {
        const index = registrations.findIndex((item) => item.id === id);
        if (index >= 0) {
            registrations.splice(index, 1);
            emitChange();
        }
    };
}

export function unregisterPromptEditorSlot(pluginId: string, slot: PromptEditorSlotKind): void {
    const index = registrations.findIndex((item) => item.pluginId === pluginId && item.slot === slot);
    if (index >= 0) {
        registrations.splice(index, 1);
        emitChange();
    }
}

/** 卸载某插件的全部输入框插槽（插件停用/卸载时调用）。 */
export function unregisterPluginPromptEditorSlots(pluginId: string): void {
    let changed = false;
    for (let i = registrations.length - 1; i >= 0; i--) {
        if (registrations[i].pluginId === pluginId) {
            registrations.splice(i, 1);
            changed = true;
        }
    }
    if (changed) emitChange();
}

export function getPromptEditorSlots(slot: PromptEditorSlotKind): PromptEditorSlotRegistration[] {
    return registrations
        .filter((item) => item.slot === slot)
        .sort((left, right) => right.priority - left.priority || left.order - right.order);
}
