package api

import "github.com/gin-gonic/gin"

// RequestReleaseMiddleware 必须先于 Recovery 注册，让内层日志和恢复处理先完成。
// Gin 只在下次取出上下文时重置；归还前释放引用，避免池中继续保留大正文。
func RequestReleaseMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		// 响应写入器会保留原请求；隔离 Body 字段，避免它持有缓存正文。
		// WithContext 仅复制请求结构体，不复制正文、Header 或 URL。
		c.Request = c.Request.WithContext(c.Request.Context())
		defer func() {
			// 服务器只清理原请求的表单；副本的上传临时文件由这里收尾。
			if c.Request.MultipartForm != nil {
				_ = c.Request.MultipartForm.RemoveAll()
			}
			c.Keys = nil
			c.Request = nil
		}()
		c.Next()
	}
}
