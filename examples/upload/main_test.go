package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestUploadDigestAndCleanup(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	content := bytes.Repeat([]byte("GoLa upload\n"), 8192)
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "../../client-name.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteField("description", "body value"); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/upload?description=query-value", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	w := httptest.NewRecorder()
	newRouter().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var result struct {
		Size        int64  `json:"size"`
		SHA256      string `json:"sha256"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(content)
	if result.Size != int64(len(content)) || result.SHA256 != hex.EncodeToString(digest[:]) || result.Description != "body value" {
		t.Fatalf("unexpected upload result: %+v", result)
	}
	entries, err := os.ReadDir(tmp)
	if err != nil || len(entries) != 0 {
		t.Fatalf("temporary files left behind: %v, %v", entries, err)
	}
}

func TestUploadErrors(t *testing.T) {
	for _, tc := range []struct {
		name, media, body string
		status            int
	}{
		{"wrong media", "application/json", `{}`, http.StatusUnsupportedMediaType},
		{"missing file", "multipart/form-data; boundary=empty", "--empty--\r\n", http.StatusBadRequest},
		{"malformed", "multipart/form-data; boundary=broken", "not multipart", http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/upload", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", tc.media)
			w := httptest.NewRecorder()
			newRouter().ServeHTTP(w, req)
			if w.Code != tc.status || !json.Valid(w.Body.Bytes()) {
				t.Fatalf("status=%d body=%q", w.Code, w.Body.String())
			}
		})
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "large.bin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(bytes.Repeat([]byte{'x'}, 8<<20)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	// Exercise actual reads with an unknown Content-Length.
	req := httptest.NewRequest(http.MethodPost, "/upload", io.NopCloser(&body))
	req.ContentLength = -1
	req.Header.Set("Content-Type", writer.FormDataContentType())
	w := httptest.NewRecorder()
	newRouter().ServeHTTP(w, req)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized upload status=%d body=%q", w.Code, w.Body.String())
	}
}
