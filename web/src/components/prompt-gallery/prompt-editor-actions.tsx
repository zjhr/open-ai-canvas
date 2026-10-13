/**
 * 输入框按钮区：宿主渲染插件注册的提示词输入框动作。
 *
 * 三个输入框各自只写一行 <PromptEditorActions ... />，按钮、弹层、状态都留在插件侧；
 * 这样"往提示词输入框加按钮"不再改输入框组件，避免与上游布局改动互相解冲突。
 */
import { useCallback, type ReactNode } from "react";

import { usePromptEditorSlots, type PromptEditorSlotRegistration } from "@/lib/plugins/prompt-editor-slot-registry";
import type { PromptEditorSlotContext, PromptGalleryMode } from "@/lib/plugins/plugin-types";

export type PromptEditorActionsProps = {
    /** 画布节点面板 / 生成配置组装框 / 创作页输入框。 */
    surface: "canvas-node" | "canvas-config" | "creation-composer";
    mode: PromptGalleryMode;
    /** 渲染时的正文快照，供插件做"是否有内容"这类展示判断。 */
    prompt: string;
    /** 读最新正文：回填时用，避免用渲染时快照覆盖用户刚输入的内容。 */
    readPrompt: () => string;
    /** 由宿主决定插入方式（空则填入、非空追加、组装框按光标插入）。 */
    applyPrompt: (text: string) => void;
    /** 没有插件注册任何动作时的占位（通常是 null）。 */
    fallback?: ReactNode;
};

export function PromptEditorActions({ surface, mode, prompt, readPrompt, applyPrompt, fallback = null }: PromptEditorActionsProps) {
    const slots = usePromptEditorSlots("prompt-composer-actions");

    // 回填语义统一由宿主决定：插件按钮拿到 applyPrompt 就能插入，
    // 不用自己拿 ref 或读 store，也就不会绕过"空则填入、非空追加"的规则。
    const renderSlot = useCallback(
        (slot: PromptEditorSlotRegistration) => {
            const context: PromptEditorSlotContext = { pluginId: slot.pluginId, mode, surface, prompt, readPrompt, applyPrompt };
            return slot.render(context);
        },
        [applyPrompt, mode, prompt, readPrompt, surface],
    );

    if (!slots.length) return <>{fallback}</>;
    return (
        <>
            {slots.map((slot) => (
                <span key={`${slot.pluginId}:${slot.id}`} className="contents">
                    {renderSlot(slot)}
                </span>
            ))}
        </>
    );
}