package gola

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"html/template"
	"io"
	"mime"
	"strconv"
	"strings"
)

var ErrHTMLTemplatesNotConfigured = errors.New("gola: HTML templates are not configured")

// WithHTMLTemplates clones application-owned templates during configuration.
// Configure functions and parse trusted templates before calling New. A nil or
// already executed template panics, like other invalid engine options.
func WithHTMLTemplates(templates *template.Template) Option {
	return func(e *Engine) {
		e.assertMutable()
		if templates == nil {
			panic("gola: nil HTML templates")
		}
		cloned, err := templates.Clone()
		if err != nil {
			panic(fmt.Sprintf("gola: clone HTML templates: %v", err))
		}
		e.htmlTemplates = cloned
	}
}

// HTML executes a configured html/template into a buffer before committing the
// response. Template source and functions must be trusted application code.
func (c *Context) HTML(status int, name string, value any) error {
	if c.Writer.Written() {
		return ErrResponseCommitted
	}
	if c.engine == nil || c.engine.htmlTemplates == nil {
		return ErrHTMLTemplatesNotConfigured
	}
	var data bytes.Buffer
	if err := c.engine.htmlTemplates.ExecuteTemplate(&data, name, value); err != nil {
		return err
	}
	return c.Data(status, "text/html; charset=utf-8", data.Bytes())
}

// XML encodes the complete value before committing the response. Struct values
// and xml tags follow encoding/xml; no XML declaration is added automatically.
func (c *Context) XML(status int, value any) error {
	if c.Writer.Written() {
		return ErrResponseCommitted
	}
	data, err := xml.Marshal(value)
	if err != nil {
		return err
	}
	return c.Data(status, "application/xml; charset=utf-8", data)
}

// ShouldBindXML reads one bounded XML document into a non-nil struct pointer,
// then invokes the configured Validator. DTDs and directives are rejected;
// external entities and character-set conversion are not enabled. As with
// encoding/xml, conversion failure can partially change dst. Unknown elements
// follow encoding/xml semantics; WithDisallowUnknownFields affects JSON only.
func (c *Context) ShouldBindXML(dst any) error {
	if err := checkBindingTarget(dst, true); err != nil {
		return err
	}
	media, _, err := mime.ParseMediaType(c.Request.Header.Get("Content-Type"))
	if err != nil {
		return &BindingError{Kind: ErrUnsupportedMediaType, Err: err}
	}
	subtype, application := strings.CutPrefix(media, "application/")
	if media != "text/xml" && media != "application/xml" && !(application && len(subtype) > len("+xml") && strings.HasSuffix(subtype, "+xml") && !strings.Contains(subtype, "*")) {
		return &BindingError{Kind: ErrUnsupportedMediaType}
	}
	body, err := c.limitedBindingBody()
	if err != nil {
		return err
	}
	data, err := io.ReadAll(body)
	if err != nil {
		return classifyBindingReadError(err)
	}
	// UTF-8 XML may begin with one BOM. It still counts toward the body limit;
	// do not remove embedded or repeated BOMs from the document.
	data = bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf})
	if err := checkXMLDocument(data); err != nil {
		return &BindingError{Kind: ErrBindingSyntax, Err: err}
	}
	if err := xml.Unmarshal(data, dst); err != nil {
		kind := ErrBindingSyntax
		var conversion *strconv.NumError
		if errors.As(err, &conversion) {
			kind = ErrBindingType
		}
		return &BindingError{Kind: kind, Err: err}
	}
	return c.validateBinding(dst)
}

// encoding/xml.Unmarshal alone ignores trailing input. Scan the entire bounded
// document first so extra roots, text and directives cannot bypass validation.
func checkXMLDocument(data []byte) error {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	depth, roots := 0, 0
	declaration := false
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			if roots != 1 || depth != 0 {
				return errors.New("XML body must contain exactly one root element")
			}
			return nil
		}
		if err != nil {
			return err
		}
		switch token := token.(type) {
		case xml.StartElement:
			if depth == 0 {
				roots++
				if roots != 1 {
					return errors.New("XML body contains multiple root elements")
				}
			}
			depth++
		case xml.EndElement:
			depth--
		case xml.CharData:
			if depth == 0 && len(bytes.Trim(token, " \t\r\n")) != 0 {
				return errors.New("XML body contains text outside its root element")
			}
		case xml.Directive:
			return errors.New("XML directives and DTDs are not supported")
		case xml.ProcInst:
			if token.Target != "xml" || roots != 0 || declaration {
				return errors.New("XML processing instructions are not supported")
			}
			declaration = true
		}
	}
}
