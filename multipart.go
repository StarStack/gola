package gola

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"mime/multipart"
	"net/http"
)

type multipartBinding struct {
	form    *multipart.Form
	cleanup *multipart.Form
	err     error
}

// MultipartForm parses a multipart/form-data body within the engine's body
// limit, without merging URL query parameters. Both results and errors are
// cached; this method neither writes a response nor aborts the handler chain.
// Forms parsed earlier through Request are rejected because their limits are
// unknown. Files are valid only until this request's handlers have finished.
func (c *Context) MultipartForm() (*multipart.Form, error) {
	if c.multipartForm != nil {
		return c.multipartForm.form, c.multipartForm.err
	}
	parsed := &multipartBinding{}
	c.multipartForm = parsed
	if c.Request.MultipartForm != nil {
		parsed.err = &BindingError{Kind: ErrBindingSyntax, Err: errors.New("multipart form was already parsed outside Context")}
		return nil, parsed.err
	}
	media, params, err := mime.ParseMediaType(c.GetHeader("Content-Type"))
	if err != nil || media != "multipart/form-data" {
		parsed.err = &BindingError{Kind: ErrUnsupportedMediaType, Err: err}
		return nil, parsed.err
	}
	boundary := params["boundary"]
	if boundary == "" {
		parsed.err = &BindingError{Kind: ErrBindingSyntax, Err: http.ErrMissingBoundary}
		return nil, parsed.err
	}
	body, err := c.limitedBindingBody()
	if err != nil {
		parsed.err = err
		return nil, err
	}
	memory := DefaultMultipartMemory
	if c.engine != nil {
		memory = c.engine.multipartMemory
	}
	form, err := multipart.NewReader(body, boundary).ReadForm(memory)
	if form != nil {
		parsed.cleanup = multipartCleanupForm(form)
	}
	if err == nil {
		// A final boundary can precede more body bytes. Read the remaining
		// bounded body, including any sticky limit error from parser read-ahead.
		_, err = io.Copy(io.Discard, body)
	}
	if err != nil {
		if errors.Is(err, multipart.ErrMessageTooLarge) {
			parsed.err = &BindingError{Kind: ErrBodyTooLarge, Err: err}
		} else {
			parsed.err = classifyBindingReadError(err)
		}
		// ReadForm cleans partial files on its own errors; this also cleans
		// a completed form when checking the remaining body fails.
		c.cleanupMultipart()
		return nil, parsed.err
	}
	parsed.form = form
	c.Request.MultipartForm = form
	return form, nil
}

// FormFile returns the first uploaded file for name, or http.ErrMissingFile.
// Callers must close files opened with FileHeader.Open and copy any data they
// need before the handler chain returns. Filename is untrusted client input;
// applications must choose their own destination paths when saving uploads.
func (c *Context) FormFile(name string) (*multipart.FileHeader, error) {
	form, err := c.MultipartForm()
	if err != nil {
		return nil, err
	}
	files := form.File[name]
	if len(files) == 0 {
		return nil, http.ErrMissingFile
	}
	return files[0], nil
}

// Keep the cleanup inventory separate from the mutable form exposed to the
// application. Replacing Request.MultipartForm or editing its maps must not
// prevent removal of temporary files that this Context created.
func multipartCleanupForm(form *multipart.Form) *multipart.Form {
	owned := &multipart.Form{File: make(map[string][]*multipart.FileHeader, len(form.File))}
	for name, headers := range form.File {
		copies := make([]*multipart.FileHeader, len(headers))
		for i, header := range headers {
			copy := *header
			copies[i] = &copy
		}
		owned.File[name] = copies
	}
	return owned
}

func (c *Context) cleanupMultipart() {
	if c.multipartForm == nil || c.multipartForm.cleanup == nil {
		return
	}
	if err := c.multipartForm.cleanup.RemoveAll(); err != nil {
		failure := fmt.Errorf("gola: remove multipart temporary files: %w", err)
		c.Error(failure)
		c.logMultipartCleanupError(failure)
		return
	}
	c.multipartForm.cleanup = nil
}

func (c *Context) logMultipartCleanupError(err error) {
	// A failing custom logging handler must not change the response or replace
	// a panic that is already unwinding through request cleanup.
	defer func() { _ = recover() }()
	logger := slog.Default()
	if c.engine != nil && c.engine.logger != nil {
		logger = c.engine.logger
	}
	logger.ErrorContext(c.Request.Context(), "multipart cleanup failed", "request_id", requestID(c), "error", err)
}
