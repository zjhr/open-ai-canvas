import type { Asset } from "@/stores/use-asset-store";

export const PLUGIN_API_VERSION = "yingce.plugin/v1" as const;
export const PLUGIN_API_VERSION_V2 = "yingce.plugin/v2" as const;

export type EditorSlotKind =
    | "timeline-panel"
    | "preview-renderer"
    | "inspector"
    | "asset-ingest"
    | "subtitle-tool"
    | "transcription-provider"
    | "export-renderer"
    | "ai-assistant";

export type EditorSlotContribution = {
    slot: EditorSlotKind;
    priority?: number;
};

export type EditorPluginPermission = "timeline.read" | "timeline.command" | "export.run";

/**
 * 提示词输入框插槽。
 *
 * 宿主在图片/视频提示词输入框旁边提供唯一的按钮区，插件把自己的按钮注册进去，
 * 这样"往输入框加按钮"不需要再改输入框组件本身（此前 prompt-optimizer 与
 * prompt-gallery 各自内联了一段按钮 + 状态，上游一动布局就要跟着解冲突）。
 */
export type PromptGalleryMode = "text" | "image" | "video";

export type PromptEditorSlotKind = "prompt-composer-actions";

export type PromptEditorSlotContribution = {
    slot: PromptEditorSlotKind;
    priority?: number;
};

/** 插槽渲染时宿主交给插件的上下文；插件据此决定按钮可用性与回填行为。 */
export type PromptEditorSlotContext = {
    /** 宿主侧稳定的插件 ID，用于日志与权限判断。 */
    pluginId: string;
    /** 当前输入框的创作类型。 */
    mode: PromptGalleryMode;
    /** 画布节点面板 / 生成配置组装框 / 创作页输入框。 */
    surface: "canvas-node" | "canvas-config" | "creation-composer";
    /** 当前输入框已有内容（可能为空字符串）。 */
    prompt: string;
    /** 读当前输入框内容：回填时要用最新值，不能依赖渲染时快照。 */
    readPrompt: () => string;
    /**
     * 写入输入框。宿主按各输入框的既有语义处理插入方式
     * （画布节点与创作页空则填入、非空按新行追加；组装框按光标插入）。
     */
    applyPrompt: (text: string) => void;
};

export type PluginContributionsV2 = PluginContributions & {
    editorSlots?: EditorSlotContribution[];
    promptEditorSlots?: PromptEditorSlotContribution[];
};

// 注意：必须把 permissions 一并 Omit 掉再完全替换，否则数组交叉类型会被归约为 v1 的 PluginPermission[]，v2 权限字面量无法赋值。
export type PluginManifestV2 = Omit<PluginManifest, "apiVersion" | "contributes" | "permissions"> & {
    apiVersion: typeof PLUGIN_API_VERSION_V2;
    permissions: Array<PluginPermission | EditorPluginPermission>;
    contributes: PluginContributionsV2;
};

export type PluginContributionKind = "provider" | "payment-provider" | "workflow" | "canvas-node" | "transform" | "command" | "asset-source" | "usage-observer" | "ai-capability" | "agent" | "import-export";
export type PluginSurface = "node" | "fullscreen" | "hybrid" | "asset-source" | "settings" | "wallet" | "prompt-gallery";
export type ProtocolCapability = "text" | "image" | "video" | "audio";
export type ProtocolScope = "admin.system-channel" | "user.custom-channel" | "canvas" | "creation" | "agent" | string;
export type PluginRuntime = "declarative" | "sandbox" | "worker" | "trusted-backend";
export type PluginField = {
    name: string;
    type: "string" | "number" | "boolean" | "secret" | "url" | "select" | "json";
    label?: string;
    required?: boolean;
    default?: string | number | boolean;
    description?: string;
    values?: string[];
};
export type PluginParameter = {
    name: string;
    type: string;
    required?: boolean;
    description?: string;
    values?: string[];
    mapping?: string;
};
export type PluginProviderOperation = {
    method: "GET" | "POST" | "PUT" | "DELETE";
    path: string;
    contentType?: string;
    fields?: Record<string, string>;
};
export type PluginProviderContribution = {
    id: string;
    label: string;
    capabilities: ProtocolCapability[];
    scopes: ProtocolScope[];
    baseUrl?: string;
    auth?: { type: "bearer" | "api-key" | "custom"; field: string; header?: string };
    parameters?: PluginParameter[];
    create: PluginProviderOperation;
    poll?: PluginProviderOperation;
    cancel?: PluginProviderOperation;
    response: {
        taskIdPaths?: string[];
        statusPaths?: string[];
        messagePaths?: string[];
        textPaths?: string[];
        reasoningPaths?: string[];
        resultPaths?: string[];
        resultKind?: "image" | "video" | "audio";
        resultEphemeral?: boolean;
    };
};
export type PluginWorkflowContribution = {
    id: string;
    label: string;
    providerId: string;
    capability: ProtocolCapability;
    parameters: PluginParameter[];
    defaults?: Record<string, string | number | boolean>;
};
export type PluginPaymentProviderContribution = {
    id: string;
    label: string;
    icon: string;
    checkoutMode: "qr_code" | "redirect";
    expiryPolicy: { defaultMinutes: number; minMinutes: number; maxMinutes: number };
};
export type PluginCanvasNodeContribution = {
    id: string;
    label: string;
    defaultTitle: string;
    defaultSize: { width: number; height: number };
    schema: Record<string, unknown>;
    renderer: "declarative" | "sandbox";
    /** Optional input contract for nodes that consume one or more media kinds. */
    acceptsInputKind?: "image" | "video" | "audio" | "text" | Array<"image" | "video" | "audio" | "text">;
    /** Optional maximum number of direct inputs. */
    maxInputCount?: number;
    /** Analysis/sink nodes can hide the right-side output connection. */
    showOutputConnection?: boolean;
};
export type PluginTransformContribution = {
    id: string;
    input: "media" | "generation";
    output: "provider-request" | "media";
    runtime: PluginRuntime;
};
export type PluginContributions = {
    providers?: PluginProviderContribution[];
    paymentProviders?: PluginPaymentProviderContribution[];
    workflows?: PluginWorkflowContribution[];
    canvasNodes?: PluginCanvasNodeContribution[];
    transforms?: PluginTransformContribution[];
    commands?: Array<{ id: string; label: string }>;
    assetSources?: string[];
    usageObservers?: string[];
    aiCapabilities?: string[];
    agents?: string[];
    importExport?: string[];
    /** 提示词画廊的数据来源标识；宿主按标识决定用哪份目录渲染画廊。 */
    promptSources?: string[];
};
export type PluginPermission =
    | "canvas.read"
    | "canvas.write"
    | "asset.read"
    | "asset.search"
    | "asset.import"
    | "asset.upload"
    | "generation.run"
    | "ai.text"
    | "media.read"
    | "usage.read"
    | "payment.create"
    | "payment.query"
    | "payment.close"
    | "payment.reconcile"
    | "external.open";

export type PluginManifest = {
    apiVersion: typeof PLUGIN_API_VERSION;
    id: string;
    name: string;
    version: string;
    publishedAt?: string;
    updatedAt?: string;
    description: string;
    documentation?: string;
    author?: string;
    entry?: string;
    surfaces?: PluginSurface[];
    permissions: PluginPermission[];
    trusted?: boolean;
    configuration?: { fields: PluginField[] };
    runtime?: { backend?: PluginRuntime; web?: PluginRuntime };
    contributes: PluginContributions;
};

export type PluginStorage = {
    get<T>(key: string): Promise<T | null>;
    set<T>(key: string, value: T): Promise<void>;
    remove(key: string): Promise<void>;
};

export type PluginTextContentPart = { type: "text"; text: string } | { type: "image_url"; image_url: { url: string } };

export type PluginTextMessage = {
    role: "system" | "user" | "assistant";
    content: string | PluginTextContentPart[];
};

export type PluginTextTool = {
    type: "function";
    function: {
        name: string;
        description?: string;
        parameters: Record<string, unknown>;
        strict?: boolean;
    };
};

export type PluginTextToolChoice = "auto" | "required" | { type: "function"; name: string };

export type PluginTextToolCall = {
    name: string;
    arguments: string;
};

export type PluginTextResponse = {
    content: string;
    toolCalls: PluginTextToolCall[];
};

export type PluginTextRequest = {
    model?: string;
    messages: PluginTextMessage[];
    tools?: PluginTextTool[];
    toolChoice?: PluginTextToolChoice;
    signal?: AbortSignal;
    onDelta?: (text: string) => void;
};

export type PluginAiTextService = {
    requestToolResponse: (request: PluginTextRequest) => Promise<PluginTextResponse>;
};

export type PluginHostServices = {
    ai?: {
        text?: PluginAiTextService;
    };
    media?: {
        resolve: (reference: { url?: string; dataUrl?: string; kind?: string }, signal?: AbortSignal) => Promise<{ dataUrl: string; mimeType: string }>;
    };
    usage?: {
        list: (scope?: string) => Promise<ReadonlyArray<Record<string, unknown>>>;
    };
};

export type PluginHostContext = {
    manifest: PluginManifest | PluginManifestV2;
    permissions: ReadonlySet<PluginPermission | EditorPluginPermission>;
    storage: PluginStorage;
    config: Readonly<PluginInstallation["config"]>;
    services?: PluginHostServices;
};

export type PromptOptimizationMode = "expand" | "refine" | "style" | "model-adapt" | "reference";

export type PromptOptimizationInput = {
    prompt: string;
    mode: PromptOptimizationMode;
    generationMode: "image" | "video";
    targetModel?: string;
    targetProtocol?: string;
    optimizerModel?: string;
    context?: {
        texts?: Array<{ title: string; text: string }>;
        images?: Array<{ title: string; url: string }>;
    };
};

export type PromptOptimizationVariant = {
    label: string;
    prompt: string;
};

export type PromptOptimizationResult = {
    optimizedPrompt: string;
    negativePrompt: string;
    changes: string[];
    assumptions: string[];
    variants: PromptOptimizationVariant[];
    modelProfile?: { id: string; label: string };
};

export type PromptOptimizerProvider = {
    optimize: (input: PromptOptimizationInput, options?: { signal?: AbortSignal; onDelta?: (text: string) => void }) => Promise<PromptOptimizationResult>;
};

/** 画廊检索条件。AI 搜索解析出的结构化条件与用户手动筛选共用这一份形状。 */
export type PromptGalleryQuery = {
    keyword?: string;
    mode?: PromptGalleryMode;
    sourceId?: string;
    category?: string;
    favoritesOnly?: boolean;
    limit?: number;
    offset?: number;
};

/** 画廊条目：宿主只要求渲染和回填需要的字段，来源私有字段留在宿主数据层。 */
export type PromptGalleryItem = {
    id: string;
    title: string;
    description: string;
    /** 封面。来源没有配图时缺省，宿主渲染文字版式而不是破图。 */
    image?: string;
    mode: PromptGalleryMode;
    prompt: string;
    sourceId?: string;
    sourceLabel?: string;
    sourceUrl?: string;
    credit?: string;
    license?: string;
    category?: string;
    categoryLabel?: string;
    /** 画面比例/时长等规格，例如 "16:9 · 10s"；上游有才带。 */
    ratio?: string;
    tags?: string[];
    favorite?: boolean;
};

export type PromptGallerySearchResult = {
    items: PromptGalleryItem[];
    total: number;
    /**
     * 非致命问题（例如远端索引暂时取不到，只返回了站内目录）。
     * 宿主必须展示，不能让"少了一半结果"看起来像"就这么多"。
     */
    warnings?: string[];
};

export type PromptGalleryFacet = {
    id: string;
    label: string;
    count: number;
};

export type PromptGallerySearchPlan = PromptGalleryQuery & {
    /** 面向用户的解释：AI 从自然语言里读出了哪些条件。 */
    explanation?: string;
};

export type PromptGalleryProvider = {
    /** 按条件检索并分页；offset/limit 由调用方给。 */
    search: (query: PromptGalleryQuery) => Promise<PromptGallerySearchResult>;
    /** 来源与分类筛选项。目录加载失败时抛错，由宿主显示错误态。 */
    facets?: () => Promise<{ sources: PromptGalleryFacet[]; categories: PromptGalleryFacet[] }>;
    /**
     * 把自然语言需求解析成检索条件；没有 ai.text 权限时宿主会拒绝调用。
     * base 是用户当前已选条件，模型没给出的维度回落到它。
     */
    planSearch?: (query: string, options?: { signal?: AbortSignal; base?: PromptGalleryQuery }) => Promise<PromptGallerySearchPlan>;
};

export type AssetSourceQuery = {
    keyword?: string;
    folderId?: string;
    tags?: string[];
    kind?: Asset["kind"];
    limit?: number;
    offset?: number;
    signal?: AbortSignal;
};

export type ExternalAssetFolder = {
    id: string;
    name: string;
    parentId?: string;
};

export type ExternalAssetItem = {
    id: string;
    title: string;
    kind: Asset["kind"];
    thumbnailUrl?: string;
    fileUrl?: string;
    mimeType?: string;
    width?: number;
    height?: number;
    bytes?: number;
    tags?: string[];
    folderId?: string;
    folderIds?: string[];
    folderPath?: string[];
    description?: string;
    metadata?: Record<string, unknown>;
};

export type ExternalAssetPickerReference = {
    sourceId: string;
    sourceName: string;
    item: ExternalAssetItem;
};

export type AssetSourceProvider = {
    listFolders?: (signal?: AbortSignal) => Promise<ExternalAssetFolder[]>;
    list?: (query: AssetSourceQuery) => Promise<ExternalAssetItem[]>;
    importAsset?: (item: ExternalAssetItem, signal?: AbortSignal) => Promise<Asset>;
    uploadAsset?: (asset: Asset, signal?: AbortSignal) => Promise<ExternalAssetItem>;
    uploadAssetToFolder?: (asset: Asset, folderId?: string, signal?: AbortSignal) => Promise<ExternalAssetItem>;
    uploadFile?: (file: File, folderId?: string, signal?: AbortSignal) => Promise<ExternalAssetItem>;
    createFolder?: (name: string, parentId?: string) => Promise<void>;
    openAsset?: (item: ExternalAssetItem) => Promise<void>;
};

export type PluginAgentAction = {
    id: string;
    description: string;
    inputSchema: Record<string, unknown>;
    /** Pure planner: validate input and return operations, never perform side effects here. */
    buildOperations: (input: Record<string, unknown>, snapshot: import("@/lib/canvas/canvas-operation-contract").CanvasSnapshot) => import("@/lib/canvas/canvas-operation-contract").CanvasOperation[];
};

export type RegisteredPlugin = {
    agentActions?: PluginAgentAction[];
    /** Pure, bounded projection: never return embedded media or provider credentials. */
    readAgentNode?: (node: import("@/types/canvas").CanvasNodeData, snapshot: import("@/lib/canvas/canvas-operation-contract").CanvasSnapshot) => Record<string, unknown>;
    /** v1 或 v2 插件清单；v2 清单结构为 v1 超集（含 editorSlots 声明）。 */
    manifest: PluginManifest | PluginManifestV2;
    source?: "bundled" | "uploaded" | string;
    activate?: (context: PluginHostContext) => Promise<void> | void;
    deactivate?: (context: PluginHostContext) => Promise<void> | void;
    createAssetSource?: (context: PluginHostContext) => AssetSourceProvider;
    createPromptOptimizer?: (context: PluginHostContext) => PromptOptimizerProvider;
    createPromptGallery?: (context: PluginHostContext) => PromptGalleryProvider;
    /** v2 插件由注册器从 manifest 提取的编辑器插槽声明（v1 插件无此字段）。 */
    editorSlots?: EditorSlotContribution[];
    /** v2 插件 UI 插槽的实际渲染函数由插件 activate() 阶段经 registerEditorSlot 提供。 */
};

export type PluginInstallation = {
    manifest: PluginManifest | PluginManifestV2;
    enabled: boolean;
    config: Record<string, string | number | boolean>;
    installedAt: string;
    updatedAt: string;
    lastError?: string;
};
