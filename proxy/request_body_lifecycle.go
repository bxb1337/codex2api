package proxy

import (
	"io"
	"net/http"

	"github.com/gin-gonic/gin"
)

// releaseReadRequestBody 移除入站 reader 的额外引用；正文仍由 raw_body 和
// 必需的签名快照持有。后续代理处理从缓存读取，不再重复消费入站流。
func releaseReadRequestBody(c *gin.Context) {
	if c == nil || c.Request == nil || c.Request.Body == nil {
		return
	}
	_ = c.Request.Body.Close()
	c.Request.Body = http.NoBody
	c.Request.GetBody = nil
}

func readRawRequestBodyUsing(c *gin.Context, reader io.Reader) ([]byte, error) {
	if body, ok := rawRequestBodyFromContext(c); ok {
		releaseReadRequestBody(c)
		return body, nil
	}
	body, err := io.ReadAll(reader)
	if err != nil {
		return nil, err
	}
	setRawRequestBody(c, body)
	releaseReadRequestBody(c)
	return body, nil
}

func readLimitedRawRequestBody(c *gin.Context, limit int64) ([]byte, error) {
	return readRawRequestBodyUsing(c, io.LimitReader(c.Request.Body, limit))
}
