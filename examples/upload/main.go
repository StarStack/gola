package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log"
	"net/http"

	"github.com/starstack/gola"
)

// This example reads an upload and returns its digest. It does not persist files.
func newRouter() *gola.Engine {
	r := gola.Default(gola.WithBodyLimit(8<<20), gola.WithMultipartMemory(64<<10))
	r.POST("/upload", func(c *gola.Context) {
		header, err := c.FormFile("file")
		if err != nil {
			status, message := http.StatusBadRequest, "invalid multipart upload"
			switch {
			case errors.Is(err, gola.ErrBodyTooLarge):
				status, message = http.StatusRequestEntityTooLarge, "upload exceeds the request limit"
			case errors.Is(err, gola.ErrUnsupportedMediaType):
				status, message = http.StatusUnsupportedMediaType, "multipart/form-data required"
			case errors.Is(err, http.ErrMissingFile):
				message = "file field is required"
			}
			if err := c.AbortWithStatusJSON(status, gola.H{"error": message}); err != nil {
				c.Error(err)
			}
			return
		}
		file, err := header.Open()
		if err != nil {
			c.Fail(err)
			return
		}
		defer file.Close()
		digest := sha256.New()
		size, err := io.Copy(digest, file)
		if err != nil {
			c.Fail(err)
			return
		}
		form, err := c.MultipartForm()
		if err != nil {
			c.Fail(err)
			return
		}
		description := ""
		if values := form.Value["description"]; len(values) > 0 {
			description = values[0]
		}
		if err := c.JSON(http.StatusOK, gola.H{
			"size": size, "sha256": hex.EncodeToString(digest.Sum(nil)), "description": description,
		}); err != nil {
			c.Fail(err)
		}
	})
	return r
}

func main() {
	if err := newRouter().Run("127.0.0.1:8080"); err != nil {
		log.Fatal(err)
	}
}
