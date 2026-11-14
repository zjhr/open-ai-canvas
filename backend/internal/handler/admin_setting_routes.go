// 管理端系统设置接口：对象存储、方舟私有素材、绘图引擎与资源/请求运行策略。

package handler

import (
	"net/http"
	"strings"

	"time"

	"yingce/backend/internal/service"

	"github.com/gin-gonic/gin"
)

// requestOrigin 还原浏览器实际访问本站的源。
// 只用于推导 OAuth 回调地址：Google 会拿它与登记值逐字符比对，
// 因此即使 Host 被伪造，结果也只是 redirect_uri_mismatch，不会变成开放重定向。
func requestOrigin(c *gin.Context) string {
	scheme := "http"
	if c.Request.TLS != nil || strings.EqualFold(strings.TrimSpace(c.GetHeader("X-Forwarded-Proto")), "https") {
		scheme = "https"
	}
	host := strings.TrimSpace(c.GetHeader("X-Forwarded-Host"))
	if host == "" {
		host = strings.TrimSpace(c.Request.Host)
	}
	if host == "" {
		return ""
	}
	return scheme + "://" + host
}

func registerAdminSettingRoutes(r *gin.RouterGroup, svc *service.Service) {
	r.GET("/admin/settings/oss", func(c *gin.Context) {
		user, err := currentUser(c, svc)
		if err != nil {
			failService(c, err)
			return
		}
		setting, err := svc.AdminOSSSetting(user)
		if err != nil {
			failService(c, err)
			return
		}
		ok(c, gin.H{"setting": setting})
	})
	r.PATCH("/admin/settings/oss", func(c *gin.Context) {
		user, err := currentUser(c, svc)
		if err != nil {
			failService(c, err)
			return
		}
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 64<<10)
		var req service.OSSSettingRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			fail(c, http.StatusBadRequest, err)
			return
		}
		setting, err := svc.UpdateOSSSetting(user, req)
		if err != nil {
			failService(c, err)
			return
		}
		ok(c, gin.H{"setting": setting})
	})
	r.POST("/admin/settings/oss/test", func(c *gin.Context) {
		user, err := currentUser(c, svc)
		if err != nil {
			failService(c, err)
			return
		}
		if !enforceRateLimit(c, "admin-storage-test:"+user.ID, 6, time.Minute) {
			return
		}
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 64<<10)
		var req service.OSSSettingRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			fail(c, http.StatusBadRequest, err)
			return
		}
		result, err := svc.TestAdminOSSSetting(user, req)
		if err != nil {
			failService(c, err)
			return
		}
		ok(c, result)
	})
	r.GET("/admin/settings/oss/gdrive/setup", func(c *gin.Context) {
		user, err := currentUser(c, svc)
		if err != nil {
			failService(c, err)
			return
		}
		status, err := svc.GDriveSetup(user, requestOrigin(c))
		if err != nil {
			failService(c, err)
			return
		}
		ok(c, gin.H{"status": status})
	})
	r.POST("/admin/settings/oss/gdrive/authorize", func(c *gin.Context) {
		user, err := currentUser(c, svc)
		if err != nil {
			failService(c, err)
			return
		}
		if !enforceRateLimit(c, "admin-gdrive-authorize:"+user.ID, 10, time.Minute) {
			return
		}
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 8<<10)
		var req service.GDriveAuthorizeRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			fail(c, http.StatusBadRequest, err)
			return
		}
		authorizeURL, err := svc.BeginGDriveAuthorization(user, requestOrigin(c), req)
		if err != nil {
			failService(c, err)
			return
		}
		ok(c, gin.H{"authorizeUrl": authorizeURL})
	})
	r.POST("/admin/settings/oss/gdrive/disconnect", func(c *gin.Context) {
		user, err := currentUser(c, svc)
		if err != nil {
			failService(c, err)
			return
		}
		setting, err := svc.DisconnectGDrive(user)
		if err != nil {
			failService(c, err)
			return
		}
		ok(c, gin.H{"setting": setting})
	})
	// Google 会以顶层导航的方式跳到这个地址，因此它必须是 GET，并且不能用
	// 统一 JSON 信封回复：浏览器拿到 JSON 不会自己跳回设置页。
	r.GET("/admin/settings/oss/gdrive/callback", func(c *gin.Context) {
		if !enforceRateLimit(c, "admin-gdrive-callback:"+c.ClientIP(), 30, 10*time.Minute) {
			return
		}
		user, err := currentUser(c, svc)
		if err != nil {
			c.Redirect(http.StatusFound, service.GDriveCallbackURL("error", "请先登录管理员账号后再完成授权"))
			return
		}
		// 管理员在同意页点了拒绝：Google 只带回 error，没有 code。
		if providerErr := strings.TrimSpace(c.Query("error")); providerErr != "" {
			c.Redirect(http.StatusFound, service.GDriveCallbackURL("cancelled", "已取消 Google Drive 授权"))
			return
		}
		if err := svc.CompleteGDriveAuthorization(c.Query("state"), c.Query("code"), requestOrigin(c), user.ID); err != nil {
			c.Redirect(http.StatusFound, service.GDriveCallbackURL("error", err.Error()))
			return
		}
		c.Redirect(http.StatusFound, service.GDriveCallbackURL("connected", ""))
	})
	r.GET("/admin/settings/ark-private-assets", func(c *gin.Context) {
		user, err := currentUser(c, svc)
		if err != nil {
			failService(c, err)
			return
		}
		setting, err := svc.AdminArkPrivateAssetSetting(user)
		if err != nil {
			failService(c, err)
			return
		}
		ok(c, gin.H{"setting": setting})
	})
	r.PATCH("/admin/settings/ark-private-assets", func(c *gin.Context) {
		user, err := currentUser(c, svc)
		if err != nil {
			failService(c, err)
			return
		}
		var req service.ArkPrivateAssetSettingRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			fail(c, http.StatusBadRequest, err)
			return
		}
		setting, err := svc.UpdateArkPrivateAssetSetting(user, req)
		if err != nil {
			failService(c, err)
			return
		}
		ok(c, gin.H{"setting": setting})
	})
	r.GET("/admin/settings/drawing-engine", func(c *gin.Context) {
		user, err := currentUser(c, svc)
		if err != nil {
			failService(c, err)
			return
		}
		setting, err := svc.AdminDrawingEngineSetting(user)
		if err != nil {
			failService(c, err)
			return
		}
		ok(c, gin.H{"setting": setting})
	})
	r.PATCH("/admin/settings/drawing-engine", func(c *gin.Context) {
		user, err := currentUser(c, svc)
		if err != nil {
			failService(c, err)
			return
		}
		var req service.DrawingEngineSetting
		if err := c.ShouldBindJSON(&req); err != nil {
			fail(c, http.StatusBadRequest, err)
			return
		}
		setting, err := svc.UpdateDrawingEngineSetting(user, req)
		if err != nil {
			failService(c, err)
			return
		}
		ok(c, gin.H{"setting": setting})
	})
	r.GET("/admin/settings/jev-review", func(c *gin.Context) {
		user, err := currentUser(c, svc)
		if err != nil {
			failService(c, err)
			return
		}
		setting, err := svc.AdminJevReviewSetting(user)
		if err != nil {
			failService(c, err)
			return
		}
		ok(c, gin.H{"setting": setting})
	})
	r.PATCH("/admin/settings/jev-review", func(c *gin.Context) {
		user, err := currentUser(c, svc)
		if err != nil {
			failService(c, err)
			return
		}
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 64<<10)
		var req service.JevReviewSettingRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			fail(c, http.StatusBadRequest, err)
			return
		}
		setting, err := svc.UpdateJevReviewSetting(user, req)
		if err != nil {
			failService(c, err)
			return
		}
		ok(c, gin.H{"setting": setting})
	})
	r.GET("/admin/settings/runtime-policy", func(c *gin.Context) {
		user, err := currentUser(c, svc)
		if err != nil {
			failService(c, err)
			return
		}
		setting, err := svc.AdminRuntimePolicySetting(user)
		if err != nil {
			failService(c, err)
			return
		}
		ok(c, gin.H{"setting": setting})
	})
	r.GET("/admin/settings/runtime-policy/self-use", func(c *gin.Context) {
		user, err := currentUser(c, svc)
		if err != nil {
			failService(c, err)
			return
		}
		setting, err := svc.AdminSelfUseRuntimePolicy(user)
		if err != nil {
			failService(c, err)
			return
		}
		ok(c, gin.H{"setting": setting})
	})
	r.PUT("/admin/settings/runtime-policy", func(c *gin.Context) {
		user, err := currentUser(c, svc)
		if err != nil {
			failService(c, err)
			return
		}
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 64<<10)
		var req service.RuntimePolicySetting
		if err := c.ShouldBindJSON(&req); err != nil {
			fail(c, http.StatusBadRequest, err)
			return
		}
		setting, err := svc.UpdateRuntimePolicySetting(user, req)
		if err != nil {
			failService(c, err)
			return
		}
		ok(c, gin.H{"setting": setting})
	})
	r.DELETE("/admin/settings/runtime-policy", func(c *gin.Context) {
		user, err := currentUser(c, svc)
		if err != nil {
			failService(c, err)
			return
		}
		setting, err := svc.ResetRuntimePolicySetting(user)
		if err != nil {
			failService(c, err)
			return
		}
		ok(c, gin.H{"setting": setting})
	})
}
