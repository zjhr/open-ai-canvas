/**
 * 插件自己构造 provider 的入口。
 *
 * 启用判断（服务端 effectiveEnabled + 本地安装记录）与宿主上下文构造都在这里，
 * 输入框插槽只负责渲染按钮，不需要知道 provider 怎么来。
 */
import { useMemo } from "react";

import { createPluginHostContext } from "@/services/plugin-host";
import { useConfigStore } from "@/stores/use-config-store";
import { usePluginStore } from "@/stores/use-plugin-store";

import { promptGalleryPlugin, PROMPT_GALLERY_PLUGIN_ID } from "./prompt-gallery";
import type { PromptGalleryProvider } from "../plugin-types";

/** 取本插件当前可用的 provider；插件未启用或缺少宿主能力时返回 null。 */
export function usePromptGalleryProvider(): PromptGalleryProvider | null {
    const config = useConfigStore((state) => state.config);
    const installation = usePluginStore((state) => state.installations.find((item) => item.manifest.id === PROMPT_GALLERY_PLUGIN_ID));
    const enabled = usePluginStore(
        (state) => state.pluginStates[PROMPT_GALLERY_PLUGIN_ID]?.effectiveEnabled ?? Boolean(state.installations.find((item) => item.manifest.id === PROMPT_GALLERY_PLUGIN_ID)?.enabled),
    );
    return useMemo(() => {
        if (!enabled || !promptGalleryPlugin.createPromptGallery) return null;
        // 本地安装记录只在用户打开过插件中心后才存在，而服务端状态才是启用依据；
        // 该插件没有配置字段、也不写插件存储，缺记录时补一份等价上下文即可。
        const activeInstallation = installation ?? { manifest: promptGalleryPlugin.manifest, enabled: true, config: {}, installedAt: "", updatedAt: "" };
        return promptGalleryPlugin.createPromptGallery(createPluginHostContext(promptGalleryPlugin, activeInstallation, config));
    }, [config, enabled, installation]);
}