package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRunCleansUploadedFilesAfterCancellation(t *testing.T) {
	parent := t.TempDir()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finished := make(chan error, 1)
	go func() { finished <- run(ctx, parent, func() (net.Listener, error) { return listener, nil }) }()
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	part, err := form.CreateFormFile("file", "test.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(part, "stored upload"); err != nil {
		t.Fatal(err)
	}
	if err := form.Close(); err != nil {
		t.Fatal(err)
	}
	base := "http://" + listener.Addr().String()
	client := &http.Client{Timeout: 5 * time.Second}
	response, err := client.Post(base+"/upload", form.FormDataContentType(), &body)
	if err != nil {
		t.Fatal(err)
	}
	var uploaded struct{ ID, Download string }
	err = json.NewDecoder(response.Body).Decode(&uploaded)
	response.Body.Close()
	if err != nil || response.StatusCode != http.StatusCreated || uploaded.ID == "" {
		t.Fatal("upload failed", response.StatusCode, uploaded, err)
	}
	download, err := client.Get(base + uploaded.Download)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(download.Body)
	download.Body.Close()
	if err != nil || download.StatusCode != http.StatusOK || string(data) != "stored upload" {
		t.Fatal("download failed", download.StatusCode, string(data), err)
	}
	dirs, err := os.ReadDir(parent)
	if err != nil || len(dirs) != 1 {
		t.Fatal("temporary store not created", dirs, err)
	}
	storeDir := filepath.Join(parent, dirs[0].Name())
	files, err := os.ReadDir(storeDir)
	if err != nil || len(files) != 1 {
		t.Fatal("upload was not persisted", files, err)
	}
	cancel() // signal.NotifyContext produces the same cancellation on Ctrl+C.
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server did not shut down")
	}
	if _, err := os.Stat(storeDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("temporary upload directory was retained", err)
	}
	if dirs, err := os.ReadDir(parent); err != nil || len(dirs) != 0 {
		t.Fatal("temporary parent was not emptied", dirs, err)
	}
}

func TestRunCleansDirectoryAfterListenFailure(t *testing.T) {
	parent := t.TempDir()
	sentinel := errors.New("address unavailable")
	err := run(context.Background(), parent, func() (net.Listener, error) { return nil, sentinel })
	if !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
	if dirs, err := os.ReadDir(parent); err != nil || len(dirs) != 0 {
		t.Fatal("startup failure left temporary files", dirs, err)
	}
}

func TestServeDrainsActiveRequestBeforeReturning(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started, release := make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	finished := make(chan error, 1)
	go func() {
		finished <- serve(ctx, listener, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			close(started)
			<-release
			if err := r.Context().Err(); err != nil {
				t.Error("graceful shutdown cancelled active request", err)
			}
			_, _ = io.WriteString(w, "completed")
		}))
	}()
	clientDone := make(chan error, 1)
	go func() {
		client := &http.Client{Timeout: 5 * time.Second}
		response, err := client.Get("http://" + listener.Addr().String())
		if err == nil {
			defer response.Body.Close()
			var data []byte
			data, err = io.ReadAll(response.Body)
			if err == nil && (response.StatusCode != http.StatusOK || string(data) != "completed") {
				err = errors.New("active request did not complete normally")
			}
		}
		clientDone <- err
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("request did not start")
	}
	cancel()
	select {
	case err := <-finished:
		t.Fatal("server returned before active request completed", err)
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	if err := <-clientDone; err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server did not finish after draining")
	}
}
