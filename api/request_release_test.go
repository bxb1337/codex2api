package api

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestRequestReleasePreservesCopiedBodyAndPostMiddleware(t *testing.T) {
	for _, mode := range []string{"complete", "abort", "panic"} {
		t.Run(mode, func(t *testing.T) { checkRequestReleaseOwnership(t, mode) })
	}
}

func checkRequestReleaseOwnership(t *testing.T, mode string) {
	t.Helper()
	body := bytes.Repeat([]byte("x"), 4<<20)
	router := gin.New()
	router.Use(RequestReleaseMiddleware(), gin.CustomRecovery(func(c *gin.Context, _ any) {
		if value, _ := c.Get("raw_body"); value == nil || c.Request == nil {
			t.Error("recovery lost request references")
		}
		c.AbortWithStatus(500)
	}))
	var pooled, copied *gin.Context
	router.Use(func(c *gin.Context) {
		c.Set("raw_body", body)
		c.Next()
		if mode != "panic" && (c.Request == nil || c.Keys == nil) {
			t.Error("post middleware lost request references")
		}
	})
	router.POST("/", func(c *gin.Context) {
		pooled, copied = c, c.Copy()
		finishRequestReleaseCase(c, mode)
	})
	router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/", bytes.NewReader(body)))
	if pooled.Keys != nil || pooled.Request != nil {
		t.Fatal("pooled context still retains request references")
	}
	value, _ := copied.Get("raw_body")
	if !bytes.Equal(body, value.([]byte)) {
		t.Fatal("copied context body changed")
	}
	read, err := io.ReadAll(copied.Request.Body)
	if err != nil || !bytes.Equal(body, read) {
		t.Fatal("copied request body changed")
	}
}

func finishRequestReleaseCase(c *gin.Context, mode string) {
	switch mode {
	case "panic":
		panic("request release ownership probe")
	case "abort":
		c.AbortWithStatus(400)
	default:
		c.Status(200)
	}
}

type requestRetainingWriter struct {
	*httptest.ResponseRecorder
	request *http.Request
}

func TestRequestReleaseIsolatesBodyFromRetainedOriginalRequest(t *testing.T) {
	body := bytes.Repeat([]byte("x"), 4<<20)
	original := httptest.NewRequest("POST", "/", http.NoBody)
	writer := &requestRetainingWriter{ResponseRecorder: httptest.NewRecorder(), request: original}
	router := gin.New()
	router.Use(RequestReleaseMiddleware(), func(c *gin.Context) {
		c.Set("raw_body", body)
		c.Request.Body = io.NopCloser(bytes.NewReader(body))
		c.Request.ContentLength = int64(len(body))
		c.Next()
	})
	var copied *gin.Context
	router.POST("/", func(c *gin.Context) { copied = c.Copy(); c.Status(200) })
	router.ServeHTTP(writer, original)
	if writer.request.Body != http.NoBody || writer.request.ContentLength != 0 {
		t.Fatal("response writer retained the rewritten request body")
	}
	if copied.Request == original {
		t.Fatal("request body field storage was not isolated")
	}
	read, err := io.ReadAll(copied.Request.Body)
	if err != nil || !bytes.Equal(body, read) {
		t.Fatal("copied body snapshot changed")
	}
}

func TestRequestReleaseCleansOwnedMultipartFiles(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	var body bytes.Buffer
	multipartWriter := multipart.NewWriter(&body)
	part, err := multipartWriter.CreateFormFile("upload", "fixture.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = part.Write(bytes.Repeat([]byte("x"), 1024)); err != nil {
		t.Fatal(err)
	}
	if err = multipartWriter.Close(); err != nil {
		t.Fatal(err)
	}
	original := httptest.NewRequest("POST", "/", &body)
	original.Header.Set("Content-Type", multipartWriter.FormDataContentType())
	router := gin.New()
	router.Use(RequestReleaseMiddleware())
	var path string
	router.POST("/", func(c *gin.Context) {
		if err := c.Request.ParseMultipartForm(0); err != nil {
			t.Fatal(err)
		}
		file, err := c.Request.MultipartForm.File["upload"][0].Open()
		if err != nil {
			t.Fatal(err)
		}
		path = file.(*os.File).Name()
		_ = file.Close()
		c.Status(200)
	})
	router.ServeHTTP(httptest.NewRecorder(), original)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("owned upload file survived request completion")
	}
	if original.MultipartForm != nil {
		t.Fatal("original request form snapshot changed")
	}
}
