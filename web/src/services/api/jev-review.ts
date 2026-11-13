import { http } from "@/services/api/request";

export type JevReviewProtocol = "typesafe" | "openrouter";

export type JevReviewSetting = {
    enabled: boolean;
    protocol: JevReviewProtocol;
    baseUrl: string;
    model: string;
    hasApiKey: boolean;
    updatedAt?: string;
};

export type JevReviewSettingInput = {
    enabled: boolean;
    protocol: JevReviewProtocol;
    baseUrl: string;
    model: string;
    apiKey?: string;
};

export function getAdminJevReviewSetting() {
    return http.get<{ setting: JevReviewSetting }>("/admin/settings/jev-review");
}

export function updateAdminJevReviewSetting(input: JevReviewSettingInput) {
    return http.patch<{ setting: JevReviewSetting }>("/admin/settings/jev-review", input);
}
