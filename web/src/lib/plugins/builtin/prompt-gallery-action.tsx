/**
 * 提示词画廊的输入框动作：按钮 + 抽屉都在插件这一侧，宿主只提供插槽和回填函数。
 *
 * 这是"插件往输入框加按钮"的样例实现：宿主组件里没有画廊相关代码，
 * 上游改输入框布局时不需要为画廊解冲突。
 */
import { useState } from "react";
import { App } from "antd";
import { LayoutGrid } from "lucide-react";

import { Tooltip } from "@/components/ui/base/tooltip";
import { PromptGalleryDrawer } from "@/components/prompt-gallery/prompt-gallery-drawer";
import type { PromptEditorSlotContext, PromptGalleryItem } from "@/lib/plugins/plugin-types";

import { usePromptGalleryProvider } from "./prompt-gallery-provider";

/** 各输入框的按钮外观：画布节点面板与创作页的按钮类名不同，跟各自工具栏保持一致。 */
const BUTTON_CLASS: Record<PromptEditorSlotContext["surface"], string> = {
    "canvas-node": "canvas-node-composer-header-action inline-flex h-6 shrink-0 items-center gap-1 rounded-md px-1.5",
    "canvas-config": "ant-btn ant-btn-text ant-btn-sm",
    "creation-composer": "creation-chat-control",
};

export function PromptGalleryAction({ context }: { context: PromptEditorSlotContext }) {
    const { message } = App.useApp();
    const provider = usePromptGalleryProvider();
    const [open, setOpen] = useState(false);

    // 只在图片/视频输入框出现：文本输入框用不到图片/视频提示词目录；
    // 插件未启用时 provider 为空，入口也不该出现。
    if (context.mode === "text" || !provider) return null;

    const applyItem = (item: PromptGalleryItem) => {
        if (!item.prompt.trim()) {
            message.warning("这条灵感没有可用正文，已跳过");
            return;
        }
        // 回填语义交给宿主：组装框按光标插入，其它输入框空则填入、非空追加。
        context.applyPrompt(item.prompt.trim());
        message.success(`已使用「${item.title}」`);
        setOpen(false);
    };

    return (
        <>
            <Tooltip title="从提示词画廊挑选，选中即填入当前输入框">
                <button
                    type="button"
                    className={BUTTON_CLASS[context.surface]}
                    onClick={() => setOpen(true)}
                    aria-label="打开提示词画廊"
                    aria-expanded={open}
                    aria-haspopup="dialog"
                >
                    <LayoutGrid className="size-3.5" aria-hidden="true" />
                    <span className="text-[var(--fs-tiny)] font-medium">画廊</span>
                </button>
            </Tooltip>
            <PromptGalleryDrawer
                open={open}
                mode={context.mode === "video" ? "video" : "image"}
                provider={provider}
                onClose={() => setOpen(false)}
                onApply={applyItem}
            />
        </>
    );
}