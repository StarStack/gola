package gola

import (
	"context"
	"crypto/rand"
	"errors"
	"io"
	"io/fs"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"path"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

var (
	ErrInvalidFilePath     = errors.New("gola: invalid file path")
	ErrInvalidFileLimit    = errors.New("gola: file size limit must be positive")
	ErrFileTooLarge        = errors.New("gola: file exceeds size limit")
	ErrInvalidUpload       = errors.New("gola: invalid uploaded file")
	ErrInvalidDownloadName = errors.New("gola: invalid download filename")
	ErrFileMethod          = errors.New("gola: file responses require GET or HEAD")
	ErrFileStoreClosed     = errors.New("gola: file store is closed")
	ErrFileStoreIO         = errors.New("gola: file store operation failed")
	ErrFileStorePlatform   = errors.New("gola: file store is unsupported on this platform")
)

// FileStore confines file operations to an existing directory. Names use slash
// separators and must not contain hidden components or symbolic links. Keep the
// tree under application control: hard links, mounts and filesystem permissions
// are deployment concerns, and FileStore does not implement user authorization.
// A FileStore may be used concurrently, but must not be copied after first use.
type FileStore struct {
	mu   sync.RWMutex
	root *os.Root
}

// NewFileStore opens an existing application-owned directory. It does not create
// the directory. Close the store after requests using it have finished.
func NewFileStore(dir string) (*FileStore, error) {
	if !fileStorePlatformSupported() {
		return nil, ErrFileStorePlatform
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fileStoreError(err)
	}
	return &FileStore{root: root}, nil
}

// Close releases the directory handle. It is safe to call more than once. Save
// operations in progress finish first; already-open download handles stay valid.
func (s *FileStore) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.root == nil {
		return nil
	}
	err := s.root.Close()
	s.root = nil
	return fileStoreError(err)
}

// Save saves an upload under an application-chosen relative name, without
// replacing an existing entry. maxBytes is a positive per-file limit enforced
// against bytes read, independently of FileHeader.Size. Parent directories must
// already exist. Uploaded filenames are never used as destination paths.
// A private 0600 temporary file is published only after copying completes using
// a hard link, so readers cannot observe partially saved files. Filesystems must
// support hard links. Call Save before the upload's request handlers return.
func (s *FileStore) Save(ctx context.Context, file *multipart.FileHeader, name string, maxBytes int64) (err error) {
	if ctx == nil || file == nil {
		return ErrInvalidUpload
	}
	if maxBytes <= 0 {
		return ErrInvalidFileLimit
	}
	if err := validateFilePath(name); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if s == nil {
		return ErrFileStoreClosed
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.root == nil {
		return ErrFileStoreClosed
	}
	parent, base, err := fileParent(s.root, name)
	if err != nil {
		return err
	}
	defer parent.Close()
	if _, err := parent.Lstat(base); err == nil {
		return fs.ErrExist
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fileStoreError(err)
	}
	source, err := file.Open()
	if err != nil {
		return ErrInvalidUpload
	}
	defer source.Close()

	// A cryptographically random name plus O_EXCL protects concurrent saves.
	// The leading dot also excludes staging files from public file responses.
	temporary := ".gola-upload-" + rand.Text()
	destination, err := parent.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fileStoreError(err)
	}
	published := false
	defer func() {
		_ = destination.Close()
		if cleanupErr := parent.Remove(temporary); cleanupErr != nil && !errors.Is(cleanupErr, fs.ErrNotExist) {
			err = errors.Join(err, fileStoreError(cleanupErr))
			if published {
				// A save reporting failure must not leave a visible destination.
				if rollbackErr := parent.Remove(base); rollbackErr != nil && !errors.Is(rollbackErr, fs.ErrNotExist) {
					err = errors.Join(err, fileStoreError(rollbackErr))
				}
			}
		}
	}()
	reader := &fileContextReader{ctx: ctx, reader: source}
	if _, err := io.Copy(destination, &io.LimitedReader{R: reader, N: maxBytes}); err != nil {
		return fileCopyError(ctx, err)
	}
	var extra [1]byte
	if n, readErr := reader.Read(extra[:]); n != 0 {
		return ErrFileTooLarge
	} else if readErr != io.EOF {
		if readErr != nil {
			return fileCopyError(ctx, readErr)
		}
		return ErrInvalidUpload
	}
	if err := destination.Close(); err != nil {
		return fileStoreError(err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// Link is the atomic no-overwrite publish operation. Rename would replace
	// an existing destination on Unix. Both paths use the same pinned parent.
	if err := parent.Link(temporary, base); err != nil {
		return fileStoreError(err)
	}
	published = true
	return nil
}

type fileContextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *fileContextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

func fileCopyError(ctx context.Context, err error) error {
	if contextErr := ctx.Err(); contextErr != nil {
		return contextErr
	}
	return fileStoreError(err)
}

// File sends a regular file from store using net/http.ServeContent, including
// HEAD, Range and conditional request handling. Errors opening the file do not
// write a response. After serving begins, ServeContent owns response errors.
func (c *Context) File(store *FileStore, name string) error {
	return c.serveStoredFile(store, name, "")
}

// Attachment sends a file as an attachment with an application-chosen download
// filename. The filename is header metadata only and is never a filesystem path.
// The response uses application/octet-stream and X-Content-Type-Options: nosniff.
func (c *Context) Attachment(store *FileStore, name, downloadName string) error {
	if c.Writer.Written() {
		return ErrResponseCommitted
	}
	if !validDownloadName(downloadName) {
		return ErrInvalidDownloadName
	}
	disposition := mime.FormatMediaType("attachment", map[string]string{"filename": downloadName})
	if disposition == "" {
		return ErrInvalidDownloadName
	}
	return c.serveStoredFile(store, name, disposition)
}

func (c *Context) serveStoredFile(store *FileStore, name, disposition string) error {
	if c.Writer.Written() {
		return ErrResponseCommitted
	}
	if c.Request.Method != http.MethodGet && c.Request.Method != http.MethodHead {
		return ErrFileMethod
	}
	file, info, err := store.open(name)
	if err != nil {
		return err
	}
	defer file.Close()
	if disposition != "" {
		c.Header("Content-Disposition", disposition)
		c.Header("Content-Type", "application/octet-stream")
	}
	c.Header("X-Content-Type-Options", "nosniff")
	http.ServeContent(c.Writer, c.Request, path.Base(name), info.ModTime(), file)
	return nil
}

// Static returns a terminal handler serving the relative path from a wildcard
// route parameter, for example r.GET("/assets/*path", store.Static("path")).
// Register HEAD as well. Empty paths, directories, hidden names and symlinks
// return 404, with no directory listings, index fallback or redirects.
func (s *FileStore) Static(param string) HandlerFunc {
	if param == "" {
		panic("gola: static file route parameter is empty")
	}
	return func(c *Context) {
		defer c.Abort()
		err := c.File(s, c.Param(param))
		if err == nil {
			return
		}
		if errors.Is(err, ErrResponseCommitted) {
			c.Error(err)
			return
		}
		if errors.Is(err, ErrFileMethod) {
			c.Header("Allow", "GET, HEAD")
			if writeErr := c.String(http.StatusMethodNotAllowed, "Method Not Allowed\n"); writeErr != nil {
				c.Error(writeErr)
			}
			return
		}
		status := http.StatusNotFound
		if errors.Is(err, ErrFileStoreClosed) || errors.Is(err, ErrFileStoreIO) {
			status = http.StatusInternalServerError
			c.Error(err)
		}
		if writeErr := c.String(status, "%s\n", http.StatusText(status)); writeErr != nil {
			c.Error(writeErr)
		}
	}
}

func (s *FileStore) open(name string) (*os.File, fs.FileInfo, error) {
	if err := validateFilePath(name); err != nil {
		return nil, nil, err
	}
	if s == nil {
		return nil, nil, ErrFileStoreClosed
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.root == nil {
		return nil, nil, ErrFileStoreClosed
	}
	parent, base, err := fileParent(s.root, name)
	if err != nil {
		return nil, nil, err
	}
	defer parent.Close()
	before, err := parent.Lstat(base)
	if err != nil {
		return nil, nil, fileStoreError(err)
	}
	if !before.Mode().IsRegular() {
		return nil, nil, fs.ErrNotExist
	}
	file, err := openStoredFile(parent, base)
	if err != nil {
		return nil, nil, fileStoreError(err)
	}
	after, err := file.Stat()
	if err != nil || !after.Mode().IsRegular() || !os.SameFile(before, after) {
		_ = file.Close()
		return nil, nil, fs.ErrNotExist
	}
	return file, after, nil
}

// Walk and pin every parent, rejecting symlinks even when their target lies
// inside the root. The identity check closes the Lstat/OpenRoot replacement
// race, while os.Root itself enforces traversal confinement during each open.
func fileParent(root *os.Root, name string) (*os.Root, string, error) {
	parts := strings.Split(name, "/")
	parent, err := root.OpenRoot(".")
	if err != nil {
		return nil, "", fileStoreError(err)
	}
	for _, component := range parts[:len(parts)-1] {
		before, statErr := parent.Lstat(component)
		if statErr != nil || !before.IsDir() {
			_ = parent.Close()
			return nil, "", fs.ErrNotExist
		}
		next, openErr := parent.OpenRoot(component)
		_ = parent.Close()
		if openErr != nil {
			return nil, "", fileStoreError(openErr)
		}
		after, statErr := next.Stat(".")
		if statErr != nil || !os.SameFile(before, after) {
			_ = next.Close()
			return nil, "", fs.ErrNotExist
		}
		parent = next
	}
	return parent, parts[len(parts)-1], nil
}

func validateFilePath(name string) error {
	if name == "." || !fs.ValidPath(name) || strings.ContainsAny(name, "\\:") {
		return ErrInvalidFilePath
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return ErrInvalidFilePath
		}
	}
	for _, component := range strings.Split(name, "/") {
		if strings.HasPrefix(component, ".") || strings.HasSuffix(component, ".") || strings.HasSuffix(component, " ") {
			return ErrInvalidFilePath
		}
		stem := strings.ToUpper(strings.SplitN(component, ".", 2)[0])
		if stem == "CON" || stem == "PRN" || stem == "AUX" || stem == "NUL" ||
			(len(stem) == 4 && (strings.HasPrefix(stem, "COM") || strings.HasPrefix(stem, "LPT")) && stem[3] >= '1' && stem[3] <= '9') {
			return ErrInvalidFilePath
		}
	}
	return nil
}

func validDownloadName(name string) bool {
	if len(name) > 255 || !utf8.ValidString(name) || strings.TrimSpace(name) == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\") {
		return false
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

// Never propagate PathError or LinkError: their strings expose filesystem paths.
func fileStoreError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, fs.ErrExist):
		return fs.ErrExist
	case errors.Is(err, fs.ErrNotExist):
		return fs.ErrNotExist
	case errors.Is(err, fs.ErrPermission):
		return fs.ErrPermission
	default:
		return ErrFileStoreIO
	}
}
