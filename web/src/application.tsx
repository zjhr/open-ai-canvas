import React from "react";
import { createRoot } from "react-dom/client";
import "antd/dist/reset.css";
import "./styles/globals.css";
import "./styles/shared/model-picker.css";
import "./styles/shared/overlays.css";
import "./styles/shared/scrollbars.css";
import { RouterProvider } from "react-router";

import { AuthDialogHost } from "@/components/auth/auth-dialog-host";
import { AppProviders } from "@/components/layout/app-providers";
import { router } from "@/router";

// 内置插件的插槽注册是模块副作用：只有模块被加载，注册表里才会有条目。
// 此前 builtin/index 只被「插件中心 / 后台插件页」引入，直接进画布或创作页时
// prompt-gallery 等插件从未加载，输入框插槽恒为空 → 按钮静默消失。
// 改在应用入口统一加载，保证从任何页面进入都能拿到内置插槽。
import "@/lib/plugins/builtin";

// 登录弹窗渲染在路由之外（分享页等路由也要能打开），导航直接交给 router。
const navigateFromAuthDialog = (to: string) => void router.navigate(to);

document.body.style.fontFamily = '"SF Pro Display","SF Pro Text","PingFang SC","Microsoft YaHei","Helvetica Neue",sans-serif';

createRoot(document.getElementById("root")!).render(
    <React.StrictMode>
        <AppProviders>
            <RouterProvider router={router} />
            <AuthDialogHost navigate={navigateFromAuthDialog} />
        </AppProviders>
    </React.StrictMode>,
);
