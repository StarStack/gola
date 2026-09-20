package gola_test

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	gola "github.com/starstack/gola"
)

type multipartTestFile struct {
	field, name, content string
}

func makeMultipartPayload(t testing.TB, fields [][2]string, files []multipartTestFile) ([]byte, string) {
	t.Helper()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	for _, field := range fields {
		if err := w.WriteField(field[0], field[1]); err != nil {
			t.Fatal(err)
		}
	}
	for _, file := range files {
		part, err := w.CreateFormFile(file.field, file.name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(part, file.content); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return body.Bytes(), w.FormDataContentType()
}

func multipartRequest(body []byte, media string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/?name=query&queryOnly=secret", bytes.NewReader(body))
	req.Header.Set("Content-Type", media)
	return req
}

func checkMultipartError(t *testing.T, err, kind error) {
	t.Helper()
	var classified *gola.BindingError
	if !errors.Is(err, kind) || !errors.As(err, &classified) || classified.Kind != kind {
		t.Fatalf("multipart error = %v, want BindingError with kind %v", err, kind)
	}
}

func checkMultipartTempEmpty(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("multipart temporary files remain after request: %v", entries)
	}
}

func TestMultipartMemoryConfiguration(t *testing.T) {
	if gola.DefaultMultipartMemory != 1<<20 {
		t.Fatalf("default multipart memory = %d, want 1 MiB", gola.DefaultMultipartMemory)
	}
	for _, memory := range []int64{0, 1, 1 << 20} {
		_ = gola.New(gola.WithMultipartMemory(memory))
	}
	t.Run("negative rejected", func(t *testing.T) {
		defer func() {
			if recover() == nil {
				t.Fatal("negative multipart memory was accepted")
			}
		}()
		_ = gola.New(gola.WithMultipartMemory(-1))
	})
	t.Run("configuration freezes", func(t *testing.T) {
		engine := gola.New()
		engine.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
		defer func() {
			if recover() == nil {
				t.Fatal("multipart memory changed after serving a request")
			}
		}()
		gola.WithMultipartMemory(0)(engine)
	})
}

func TestMultipartValuesFilesAndCache(t *testing.T) {
	payload, media := makeMultipartPayload(t, [][2]string{{"name", "body"}, {"name", "second"}, {"empty", ""}}, []multipartTestFile{
		{"upload", "first.txt", "first file"}, {"upload", "second.txt", "second file"},
	})
	body := &countingBindingBody{reader: bytes.NewReader(payload)}
	engine := gola.New()
	continued := false
	engine.POST("/", func(c *gola.Context) {
		form, err := c.MultipartForm()
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(form.Value["name"], []string{"body", "second"}) || !reflect.DeepEqual(form.Value["empty"], []string{""}) || len(form.Value["queryOnly"]) != 0 {
			t.Fatalf("multipart values merged query or lost order: %#v", form.Value)
		}
		files := form.File["upload"]
		if len(files) != 2 || files[0].Filename != "first.txt" || files[1].Filename != "second.txt" {
			t.Fatalf("file order = %#v", files)
		}
		for i, header := range files {
			file, err := header.Open()
			if err != nil {
				t.Fatal(err)
			}
			content, readErr := io.ReadAll(file)
			closeErr := file.Close()
			want := []string{"first file", "second file"}[i]
			if readErr != nil || closeErr != nil || string(content) != want || header.Size != int64(len(want)) {
				t.Fatalf("file %d: content=%q size=%d read=%v close=%v", i, content, header.Size, readErr, closeErr)
			}
		}
		read := body.read
		cached, err := c.MultipartForm()
		if err != nil || cached != form || body.read != read {
			t.Fatalf("multipart success not cached: form=%p cached=%p error=%v reads=%d/%d", form, cached, err, read, body.read)
		}
		first, err := c.FormFile("upload")
		if err != nil || first != files[0] {
			t.Fatalf("FormFile did not return first file: %v, %v", first, err)
		}
		if file, err := c.FormFile("missing"); file != nil || !errors.Is(err, http.ErrMissingFile) {
			t.Fatalf("missing file = %v, %v", file, err)
		}
		if c.Writer.Written() || c.IsAborted() {
			t.Fatal("multipart parsing changed response or aborted the chain")
		}
	}, func(c *gola.Context) {
		continued = true
		c.Status(http.StatusAccepted)
	})
	req := multipartRequest(payload, media)
	req.Body, req.ContentLength = body, -1
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, req)
	if !continued || recorder.Code != http.StatusAccepted || recorder.Body.Len() != 0 {
		t.Fatalf("multipart changed handler chain or response: continued=%v response=%d %q", continued, recorder.Code, recorder.Body.String())
	}
}

func TestMultipartClassifiedFailuresAreCached(t *testing.T) {
	valid, validMedia := makeMultipartPayload(t, nil, []multipartTestFile{{"upload", "a.txt", "content"}})
	for _, tt := range []struct {
		name, media string
		body        []byte
		kind        error
	}{
		{"missing media", "", valid, gola.ErrUnsupportedMediaType},
		{"wrong media", "application/x-www-form-urlencoded", valid, gola.ErrUnsupportedMediaType},
		{"mixed media", "multipart/mixed; boundary=test", valid, gola.ErrUnsupportedMediaType},
		{"malformed media", "multipart/form-data; boundary", valid, gola.ErrUnsupportedMediaType},
		{"missing boundary", "multipart/form-data", valid, gola.ErrBindingSyntax},
		{"empty boundary", `multipart/form-data; boundary=""`, valid, gola.ErrBindingSyntax},
		{"empty body", validMedia, nil, gola.ErrBindingSyntax},
		{"truncated body", validMedia, valid[:len(valid)-12], gola.ErrBindingSyntax},
	} {
		t.Run(tt.name, func(t *testing.T) {
			body := &countingBindingBody{reader: bytes.NewReader(tt.body)}
			engine := gola.New()
			continued := false
			engine.POST("/", func(c *gola.Context) {
				form, err := c.MultipartForm()
				checkMultipartError(t, err, tt.kind)
				if form != nil {
					t.Fatal("failed parsing exposed a partial form")
				}
				read := body.read
				// A later mutation must not turn a failed parse into a second attempt.
				c.Request.Header.Set("Content-Type", validMedia)
				c.Request.Body = io.NopCloser(bytes.NewReader(valid))
				cached, cachedErr := c.MultipartForm()
				_, fileErr := c.FormFile("upload")
				if cached != nil || cachedErr != err || fileErr != err || body.read != read {
					t.Fatalf("parse failure not cached: first=%v second=%v file=%v reads=%d/%d", err, cachedErr, fileErr, read, body.read)
				}
				if c.Writer.Written() || c.IsAborted() {
					t.Fatal("failed parsing changed response or aborted the chain")
				}
			}, func(c *gola.Context) {
				continued = true
				c.Status(http.StatusTeapot)
			})
			req := multipartRequest(tt.body, tt.media)
			req.Body, req.ContentLength = body, -1
			recorder := httptest.NewRecorder()
			engine.ServeHTTP(recorder, req)
			if !continued || recorder.Code != http.StatusTeapot || recorder.Body.Len() != 0 {
				t.Fatalf("failure changed response or chain: continued=%v status=%d", continued, recorder.Code)
			}
		})
	}
}

func TestMultipartTotalBodyLimit(t *testing.T) {
	payload, media := makeMultipartPayload(t, nil, []multipartTestFile{{"upload", "a.txt", strings.Repeat("x", 512)}})
	for _, length := range []int64{-1, 0, int64(len(payload))} {
		for _, limit := range []int64{int64(len(payload) - 1), int64(len(payload)), int64(len(payload) + 1)} {
			t.Run("length="+strconv.FormatInt(length, 10)+"/limit="+strconv.FormatInt(limit, 10), func(t *testing.T) {
				body := &countingBindingBody{reader: bytes.NewReader(payload)}
				engine := gola.New(gola.WithBodyLimit(limit))
				engine.POST("/", func(c *gola.Context) {
					_, err := c.MultipartForm()
					if int64(len(payload)) > limit {
						checkMultipartError(t, err, gola.ErrBodyTooLarge)
					} else if err != nil {
						t.Fatal(err)
					}
					if int64(body.read) > limit+1 || (length > limit && body.read != 0) {
						t.Fatalf("read %d bytes with declared length %d and limit %d", body.read, length, limit)
					}
					if c.Writer.Written() || c.IsAborted() {
						t.Fatal("body limit committed or aborted response")
					}
				})
				req := multipartRequest(payload, media)
				req.Body, req.ContentLength = body, length
				engine.ServeHTTP(httptest.NewRecorder(), req)
			})
		}
	}
}

func TestMultipartDefaultLimitWithoutMiddleware(t *testing.T) {
	payload, media := makeMultipartPayload(t, nil, []multipartTestFile{{"upload", "large.txt", strings.Repeat("x", 1<<20)}})
	for _, length := range []int64{-1, int64(len(payload))} {
		engine := gola.New()
		engine.POST("/", func(c *gola.Context) {
			_, err := c.MultipartForm()
			checkMultipartError(t, err, gola.ErrBodyTooLarge)
		})
		req := multipartRequest(payload, media)
		req.ContentLength = length
		engine.ServeHTTP(httptest.NewRecorder(), req)
	}
}

func TestMultipartLimitIncludesEpilogue(t *testing.T) {
	payload, media := makeMultipartPayload(t, [][2]string{{"name", "Danny"}}, nil)
	const limit = 8192 // Exceeds the parser's read buffer, so the tail cannot be ignored.
	for _, total := range []int{limit, limit + 1, limit * 2} {
		t.Run(strconv.Itoa(total), func(t *testing.T) {
			body := &countingBindingBody{reader: io.MultiReader(bytes.NewReader(payload), strings.NewReader(strings.Repeat("!", total-len(payload))))}
			engine := gola.New(gola.WithBodyLimit(limit))
			engine.POST("/", func(c *gola.Context) {
				form, err := c.MultipartForm()
				if total > limit {
					checkMultipartError(t, err, gola.ErrBodyTooLarge)
					if form != nil {
						t.Fatal("oversized epilogue exposed a form")
					}
				} else if err != nil || form.Value["name"][0] != "Danny" {
					t.Fatalf("valid exact-limit epilogue failed: form=%v error=%v", form, err)
				}
			})
			req := multipartRequest(nil, media)
			req.Body, req.ContentLength = body, -1
			engine.ServeHTTP(httptest.NewRecorder(), req)
			if body.read > limit+1 {
				t.Fatalf("oversized epilogue consumed %d bytes", body.read)
			}
		})
	}
}

func TestMultipartStandardLibraryResourceLimits(t *testing.T) {
	t.Setenv("GODEBUG", os.Getenv("GODEBUG")+",multipartmaxparts=2,multipartmaxheaders=2")
	parts, partsMedia := makeMultipartPayload(t, [][2]string{{"a", "1"}, {"b", "2"}, {"c", "3"}}, nil)
	var headers bytes.Buffer
	w := multipart.NewWriter(&headers)
	part, err := w.CreatePart(textproto.MIMEHeader{
		"Content-Disposition": {`form-data; name="upload"; filename="a.txt"`},
		"Content-Type":        {"text/plain"},
		"X-Extra":             {"metadata"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(part, "data"); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name  string
		body  []byte
		media string
	}{{"parts", parts, partsMedia}, {"headers", headers.Bytes(), w.FormDataContentType()}} {
		t.Run(tt.name, func(t *testing.T) {
			engine := gola.New()
			engine.POST("/", func(c *gola.Context) {
				_, err := c.MultipartForm()
				checkMultipartError(t, err, gola.ErrBodyTooLarge)
				if !errors.Is(err, multipart.ErrMessageTooLarge) {
					t.Fatalf("standard library resource error lost: %v", err)
				}
			})
			engine.ServeHTTP(httptest.NewRecorder(), multipartRequest(tt.body, tt.media))
		})
	}
}

func TestMultipartTemporaryFilesLiveThroughHandlerChain(t *testing.T) {
	for _, memory := range []int64{0, 8} {
		for _, outcome := range []string{"success", "abort", "panic", "abort panic", "recovery"} {
			t.Run(strconv.FormatInt(memory, 10)+"/"+outcome, func(t *testing.T) {
				dir := t.TempDir()
				t.Setenv("TMPDIR", dir)
				filename := filepath.Join(dir, "client-chosen.txt")
				payload, media := makeMultipartPayload(t, nil, []multipartTestFile{{"upload", filename, strings.Repeat("x", 64)}})
				engine := gola.New(gola.WithMultipartMemory(memory), gola.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
				if outcome == "recovery" {
					engine.Use(gola.Recovery())
				}
				var header *multipart.FileHeader
				postCalled, nextCalled := false, false
				engine.Use(func(c *gola.Context) {
					defer func() {
						postCalled = true
						if header == nil {
							t.Error("handler did not parse a file")
							return
						}
						file, err := header.Open()
						if err != nil {
							t.Errorf("file cleaned before middleware post-processing: %v", err)
							return
						}
						if err := file.Close(); err != nil {
							t.Error(err)
						}
					}()
					c.Next()
				})
				engine.POST("/", func(c *gola.Context) {
					var err error
					header, err = c.FormFile("upload")
					if err != nil {
						t.Fatal(err)
					}
					entries, err := os.ReadDir(dir)
					if err != nil || len(entries) == 0 {
						t.Fatalf("memory threshold did not spill files: entries=%v error=%v", entries, err)
					}
					for _, entry := range entries {
						if entry.IsDir() || !strings.HasPrefix(entry.Name(), "multipart-") {
							t.Errorf("client filename selected a server path: %s", entry.Name())
						}
					}
					switch outcome {
					case "abort":
						c.Abort()
					case "panic", "recovery":
						panic("handler failure")
					case "abort panic":
						panic(http.ErrAbortHandler)
					}
				}, func(c *gola.Context) { nextCalled = true })
				var panicked any
				func() {
					defer func() { panicked = recover() }()
					engine.ServeHTTP(httptest.NewRecorder(), multipartRequest(payload, media))
				}()
				if !postCalled || nextCalled != (outcome == "success") {
					t.Fatalf("handler lifecycle: post=%v next=%v outcome=%s", postCalled, nextCalled, outcome)
				}
				if outcome == "panic" && panicked != "handler failure" || outcome == "abort panic" && panicked != http.ErrAbortHandler || outcome != "panic" && outcome != "abort panic" && panicked != nil {
					t.Fatalf("unexpected panic propagation: %v", panicked)
				}
				checkMultipartTempEmpty(t, dir)
				if file, err := header.Open(); err == nil {
					_ = file.Close()
					t.Fatal("disk-backed upload still available after ServeHTTP returned")
				}
			})
		}
	}
}

func TestMultipartTemporaryFilesRemovedOnParsingFailure(t *testing.T) {
	payload, media := makeMultipartPayload(t, nil, []multipartTestFile{{"upload", "first.txt", strings.Repeat("x", 128)}, {"upload", "second.txt", strings.Repeat("y", 128)}})
	for _, cause := range []string{"truncated", "oversized epilogue"} {
		t.Run(cause, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("TMPDIR", dir)
			body, limit, kind := payload[:len(payload)-12], int64(4096), gola.ErrBindingSyntax
			if cause == "oversized epilogue" {
				body = append(append([]byte(nil), payload...), bytes.Repeat([]byte("!"), 8192)...)
				kind = gola.ErrBodyTooLarge
			}
			engine := gola.New(gola.WithMultipartMemory(0), gola.WithBodyLimit(limit))
			engine.POST("/", func(c *gola.Context) {
				form, err := c.MultipartForm()
				checkMultipartError(t, err, kind)
				if form != nil {
					t.Fatal("partial files exposed on parse failure")
				}
			})
			req := multipartRequest(body, media)
			req.ContentLength = -1
			engine.ServeHTTP(httptest.NewRecorder(), req)
			checkMultipartTempEmpty(t, dir)
		})
	}
}

func TestMultipartCleanupSurvivesApplicationFormMutation(t *testing.T) {
	for _, mutation := range []string{"files map", "file header", "request form"} {
		t.Run(mutation, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("TMPDIR", dir)
			payload, media := makeMultipartPayload(t, nil, []multipartTestFile{{"upload", "a.txt", "disk-backed content"}})
			engine := gola.New(gola.WithMultipartMemory(0))
			engine.POST("/", func(c *gola.Context) {
				form, err := c.MultipartForm()
				if err != nil {
					t.Fatal(err)
				}
				entries, err := os.ReadDir(dir)
				if err != nil || len(entries) == 0 {
					t.Fatalf("expected temporary file before mutation: entries=%v error=%v", entries, err)
				}
				switch mutation {
				case "files map":
					delete(form.File, "upload")
				case "file header":
					*form.File["upload"][0] = multipart.FileHeader{}
				case "request form":
					c.Request.MultipartForm = &multipart.Form{}
				}
			})
			engine.ServeHTTP(httptest.NewRecorder(), multipartRequest(payload, media))
			checkMultipartTempEmpty(t, dir)
		})
	}
}

func TestMultipartDoesNotBorrowOrRemoveExternalForm(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)
	payload, media := makeMultipartPayload(t, nil, []multipartTestFile{{"upload", "external.txt", "owned by caller"}})
	req := multipartRequest(payload, media)
	if err := req.ParseMultipartForm(0); err != nil {
		t.Fatal(err)
	}
	external := req.MultipartForm
	defer external.RemoveAll()
	engine := gola.New()
	engine.POST("/", func(c *gola.Context) {
		form, err := c.MultipartForm()
		checkMultipartError(t, err, gola.ErrBindingSyntax)
		if form != nil {
			t.Fatal("framework borrowed a form parsed outside its limits")
		}
	})
	engine.ServeHTTP(httptest.NewRecorder(), req)
	if req.MultipartForm != external {
		t.Fatal("externally owned request form was replaced")
	}
	file, err := external.File["upload"][0].Open()
	if err != nil {
		t.Fatalf("framework removed caller-owned temporary file: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestMultipartChunkedHTTPBodyLimit(t *testing.T) {
	payload, media := makeMultipartPayload(t, nil, []multipartTestFile{{"upload", "chunked.txt", strings.Repeat("x", 2048)}})
	type result struct {
		err     error
		chunked bool
	}
	results := make(chan result, 1)
	engine := gola.New(gola.WithBodyLimit(1024))
	engine.POST("/", func(c *gola.Context) {
		_, err := c.MultipartForm()
		results <- result{err, c.Request.ContentLength == -1 && reflect.DeepEqual(c.Request.TransferEncoding, []string{"chunked"})}
		if errors.Is(err, gola.ErrBodyTooLarge) {
			c.Status(http.StatusRequestEntityTooLarge)
			return
		}
		c.Status(http.StatusBadRequest)
	})
	server := httptest.NewServer(engine)
	defer server.Close()
	req, err := http.NewRequest(http.MethodPost, server.URL, io.NopCloser(bytes.NewReader(payload)))
	if err != nil {
		t.Fatal(err)
	}
	req.ContentLength = -1
	req.Header.Set("Content-Type", media)
	response, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if err := response.Body.Close(); err != nil {
		t.Fatal(err)
	}
	got := <-results
	checkMultipartError(t, got.err, gola.ErrBodyTooLarge)
	if !got.chunked || response.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("chunked request: chunked=%v status=%d", got.chunked, response.StatusCode)
	}
}

func TestMultipartClientDisconnectCleanup(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)
	payload, media := makeMultipartPayload(t, nil, []multipartTestFile{{"upload", "interrupted.txt", strings.Repeat("x", 64<<10)}})
	closingBoundary := bytes.LastIndex(payload, []byte("\r\n--"))
	if closingBoundary < 0 {
		t.Fatal("generated upload is missing its closing boundary")
	}
	parseResult := make(chan error, 1)
	finished := make(chan struct{})
	engine := gola.New(gola.WithMultipartMemory(0))
	engine.POST("/", func(c *gola.Context) {
		_, err := c.MultipartForm()
		parseResult <- err
	})
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(finished)
		engine.ServeHTTP(w, r)
	}))
	// Bound server teardown too, should a regression leave a body read blocked.
	server.Config.ReadTimeout = 10 * time.Second
	server.Start()
	defer server.Close()
	client, err := net.DialTimeout("tcp", server.Listener.Addr().String(), 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if err := client.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Fprintf(client, "POST / HTTP/1.1\r\nHost: localhost\r\nContent-Type: %s\r\nContent-Length: %d\r\nConnection: close\r\n\r\n", media, len(payload)); err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(client, bytes.NewReader(payload[:closingBoundary])); err != nil {
		t.Fatal(err)
	}
	// Observe actual disk content before disconnecting, with a fixed deadline.
	// This ensures the test exercises cleanup of an in-progress upload.
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	spilled := false
	for !spilled {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			info, err := entry.Info()
			if err != nil {
				t.Fatal(err)
			}
			if strings.HasPrefix(entry.Name(), "multipart-") && info.Size() > 0 {
				spilled = true
			}
		}
		if !spilled {
			select {
			case <-finished:
				t.Fatal("handler finished before the client disconnected")
			case <-deadline.C:
				t.Fatal("upload did not reach disk before the observation deadline")
			case <-tick.C:
			}
		}
	}
	select {
	case <-finished:
		t.Fatal("handler finished before the client disconnected")
	default:
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-finished:
	case <-time.After(3 * time.Second):
		t.Fatal("handler remained blocked after the client disconnected")
	}
	checkMultipartError(t, <-parseResult, gola.ErrBindingSyntax)
	checkMultipartTempEmpty(t, dir)
}
