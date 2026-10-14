/**
 * 内置插件的唯一加载入口。
 *
 * 这些插件的注册是【模块副作用】（文件末尾调用 registerPlugin / registerEditorSlot /
 * registerPromptEditorSlot），所以模块必须先被加载，注册表里才会有条目。
 *
 * 教训：`builtin/index` 曾只被插件管理页引入，导致直接进画布/创作页时
 * prompt-gallery 从未加载，输入框插槽恒为空、按钮静默消失；同时
 * ai-art-critique / media-conversion / editor-shell 又在各自页面里单独
 * 副作用 import，形成多个加载来源。现统一由应用入口
 * （src/application.tsx）引入本文件，各页面不再单独副作用 import。
 *
 * 新增内置插件时，只需在这里加一行；不要在页面里另开入口。
 */

// 需要在宿主 UI 上注册槽位/事件的插件。
import "./eagle";
import "./prompt-optimizer";
import "./prompt-gallery";
import "./workflows";
import "./ai-art-critique";
import "./media-conversion";
import "./editor/editor-shell";

/** 供测试断言使用：本文件必须保持为副作用加载的唯一入口。 */
export const BUILTIN_PLUGIN_BOOTSTRAP_ENTRIES = [
    "eagle",
    "prompt-optimizer",
    "prompt-gallery",
    "workflows",
    "ai-art-critique",
    "media-conversion",
    "editor/editor-shell",
] as const;
