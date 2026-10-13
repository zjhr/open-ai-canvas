package handler

import (
	"net/http"
	"strings"

	"yingce/backend/internal/service"

	"github.com/gin-gonic/gin"
)

// RegisterPromptGalleryRoutes 注册提示词画廊的服务端索引接口。
// 检索与封面需要登录（画廊是登录后的创作工具）；同步只允许管理员触发。
func RegisterPromptGalleryRoutes(r *gin.RouterGroup, svc *service.Service) {
	gallery := r.Group("/prompt-gallery")

	gallery.GET("/sources", func(c *gin.Context) {
		if _, err := currentUser(c, svc); err != nil {
			failService(c, err)
			return
		}
		sources, err := svc.PromptGallerySources()
		if err != nil {
			failService(c, err)
			return
		}
		ok(c, gin.H{"sources": sources})
	})

	gallery.POST("/search", func(c *gin.Context) {
		if _, err := currentUser(c, svc); err != nil {
			failService(c, err)
			return
		}
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 8<<10)
		var req service.PromptGallerySearchRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			fail(c, http.StatusBadRequest, err)
			return
		}
		result, err := svc.PromptGallerySearch(req)
		if err != nil {
			failService(c, err)
			return
		}
		ok(c, result)
	})

	gallery.GET("/cover", func(c *gin.Context) {
		if _, err := currentUser(c, svc); err != nil {
			failService(c, err)
			return
		}
		source := strings.TrimSpace(c.Query("source"))
		path := c.Query("path")
		cover, err := svc.OpenPromptGalleryCover(source, path)
		if err != nil {
			failService(c, err)
			return
		}
		defer func() { _ = cover.Body.Close() }()
		c.Header("Cache-Control", "private, max-age=604800, immutable")
		c.DataFromReader(http.StatusOK, cover.Size, cover.MimeType, cover.Body, nil)
	})

	admin := r.Group("/admin/prompt-gallery")
	admin.POST("/sources/:id/sync", func(c *gin.Context) {
		actor, err := currentUser(c, svc)
		if err != nil {
			failService(c, err)
			return
		}
		if err := svc.RequireAdmin(actor); err != nil {
			failService(c, err)
			return
		}
		sourceID := strings.TrimSpace(c.Param("id"))
		if err := svc.StartPromptGallerySync(sourceID); err != nil {
			fail(c, http.StatusBadRequest, err)
			return
		}
		sources, err := svc.PromptGallerySources()
		if err != nil {
			failService(c, err)
			return
		}
		ok(c, gin.H{"sources": sources})
	})
}