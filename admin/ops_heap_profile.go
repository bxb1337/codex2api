package admin

import (
	"bytes"
	"log"
	"net/http"
	"runtime"
	"runtime/pprof"

	"github.com/gin-gonic/gin"
)

const (
	heapProfileBinaryFormat = 0
	heapProfileTextFormat   = 1
)

// GetOpsHeapProfile 导出堆采样；gc=1 先执行 GC，debug=1 附带文本内存统计。
func (h *Handler) GetOpsHeapProfile(c *gin.Context) {
	if c.Request.URL.Query().Get("gc") == "1" {
		runtime.GC()
	}

	format := heapProfileBinaryFormat
	contentType := "application/octet-stream"
	filename := "heap.pprof"
	if c.Request.URL.Query().Get("debug") == "1" {
		format = heapProfileTextFormat
		contentType = "text/plain; charset=utf-8"
		filename = "heap.txt"
	}

	var profile bytes.Buffer
	if err := pprof.Lookup("heap").WriteTo(&profile, format); err != nil {
		writeInternalError(c, err)
		return
	}

	c.Writer.Header().Set("Cache-Control", "no-store")
	c.Writer.Header().Set("Content-Type", contentType)
	c.Writer.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	c.Writer.WriteHeader(http.StatusOK)
	if _, err := c.Writer.Write(profile.Bytes()); err != nil {
		log.Printf("导出堆采样失败: %v", err)
	}
}
