package gola

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"mime"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"strings"
)

// Validator is called after binding succeeds. Applications own validation rules.
type Validator interface {
	Validate(any) error
}

var (
	ErrBindingSyntax        = errors.New("gola: malformed binding input")
	ErrBindingType          = errors.New("gola: binding type conversion failed")
	ErrInvalidBindingTarget = errors.New("gola: invalid binding target")
	ErrBodyTooLarge         = errors.New("gola: request body too large")
	ErrUnsupportedMediaType = errors.New("gola: unsupported media type")
	ErrValidation           = errors.New("gola: validation failed")
)

// BindingError classifies a binding failure while preserving its underlying
// error. Use errors.Is for Kind and errors.As to inspect underlying errors.
// Field is set for Query, URI, and Form field conversion errors.
type BindingError struct {
	Kind  error
	Field string
	Err   error
}

func (e *BindingError) Error() string {
	message := e.Kind.Error()
	if e.Field != "" {
		message += " (field " + strconv.Quote(e.Field) + ")"
	}
	if e.Err != nil {
		message += ": " + e.Err.Error()
	}
	return message
}

func (e *BindingError) Unwrap() error        { return e.Err }
func (e *BindingError) Is(target error) bool { return target == e.Kind }

// ShouldBindJSON decodes exactly one JSON value without changing the response.
// Its body read limit applies even when the engine has no middleware installed.
// A failed binding may have partially changed dst, as with encoding/json.
func (c *Context) ShouldBindJSON(dst any) error {
	if err := checkBindingTarget(dst, false); err != nil {
		return err
	}
	if err := c.requireBindingMediaType(true); err != nil {
		return err
	}
	body, err := c.limitedBindingBody()
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(body)
	if c.engine != nil && c.engine.disallowUnknownFields {
		decoder.DisallowUnknownFields()
	}
	if err := decoder.Decode(dst); err != nil {
		return classifyBindingReadError(err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			err = errors.New("request body contains multiple JSON values")
		}
		return classifyBindingReadError(err)
	}
	return c.validateBinding(dst)
}

// ShouldBindQuery binds only URL query parameters. Scalars use the first value;
// slices retain every value in input order. The target must point to a struct.
func (c *Context) ShouldBindQuery(dst any) error {
	if err := checkBindingTarget(dst, true); err != nil {
		return err
	}
	values, err := url.ParseQuery(c.Request.URL.RawQuery)
	if err != nil {
		return &BindingError{Kind: ErrBindingSyntax, Err: err}
	}
	return c.bindValues(dst, values, "form")
}

// ShouldBindURI binds only matched path parameters, without decoding them again.
func (c *Context) ShouldBindURI(dst any) error {
	if err := checkBindingTarget(dst, true); err != nil {
		return err
	}
	values := make(url.Values, len(c.params))
	for key, value := range c.params {
		values[key] = []string{value}
	}
	return c.bindValues(dst, values, "uri")
}

// ShouldBindForm binds a bounded application/x-www-form-urlencoded body. Query
// values are never merged. Parsed form values are retained for PostForm access.
func (c *Context) ShouldBindForm(dst any) error {
	if err := checkBindingTarget(dst, true); err != nil {
		return err
	}
	form := c.parseBindingForm()
	if form.err != nil {
		return form.err
	}
	return c.bindValues(dst, form.values, "form")
}

// PostForm returns the first body form value. On a parse failure it returns ""
// and records the error once in Context.Errors; it never writes a response.
// Use ShouldBindForm when the caller needs to handle a parse error directly.
func (c *Context) PostForm(key string) string {
	form := c.parseBindingForm()
	if form.err != nil {
		if !form.recorded {
			c.Error(form.err)
			form.recorded = true
		}
		return ""
	}
	return form.values.Get(key)
}

type formBinding struct {
	values   url.Values
	err      error
	recorded bool
}

func (c *Context) parseBindingForm() *formBinding {
	if c.formBinding != nil {
		return c.formBinding
	}
	form := &formBinding{}
	c.formBinding = form
	if form.err = c.requireBindingMediaType(false); form.err != nil {
		return form
	}
	body, err := c.limitedBindingBody()
	if err != nil {
		form.err = err
		return form
	}
	data, err := io.ReadAll(body)
	if err != nil {
		form.err = classifyBindingReadError(err)
		return form
	}
	form.values, err = url.ParseQuery(string(data))
	if err != nil {
		form.values = nil
		form.err = &BindingError{Kind: ErrBindingSyntax, Err: err}
	}
	return form
}

func (c *Context) requireBindingMediaType(wantJSON bool) error {
	media, _, err := mime.ParseMediaType(c.Request.Header.Get("Content-Type"))
	if err != nil {
		return &BindingError{Kind: ErrUnsupportedMediaType, Err: err}
	}
	valid := media == "application/x-www-form-urlencoded"
	if wantJSON {
		subtype, application := strings.CutPrefix(media, "application/")
		valid = application && (subtype == "json" ||
			(len(subtype) > len("+json") && strings.HasSuffix(subtype, "+json") && !strings.Contains(subtype, "*")))
	}
	if !valid {
		return &BindingError{Kind: ErrUnsupportedMediaType}
	}
	return nil
}

func (c *Context) limitedBindingBody() (io.Reader, error) {
	limit := DefaultBodyLimit
	if c.engine != nil {
		limit = c.engine.bodyLimit
	}
	if c.Request.ContentLength > limit {
		return nil, &BindingError{Kind: ErrBodyTooLarge, Err: &http.MaxBytesError{Limit: limit}}
	}
	if c.Request.Body == nil {
		return http.NoBody, nil
	}
	// Retain the wrapper so a subsequent bind cannot reset the bytes allowance.
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, limit)
	return c.Request.Body, nil
}

func classifyBindingReadError(err error) error {
	var tooLarge *http.MaxBytesError
	var wrongType *json.UnmarshalTypeError
	var wrongTarget *json.InvalidUnmarshalError
	kind := ErrBindingSyntax
	switch {
	case errors.As(err, &tooLarge):
		kind = ErrBodyTooLarge
	case errors.As(err, &wrongType):
		kind = ErrBindingType
	case errors.As(err, &wrongTarget):
		kind = ErrInvalidBindingTarget
	}
	return &BindingError{Kind: kind, Err: err}
}

func checkBindingTarget(dst any, wantStruct bool) error {
	value := reflect.ValueOf(dst)
	if !value.IsValid() || value.Kind() != reflect.Pointer || value.IsNil() {
		return &BindingError{Kind: ErrInvalidBindingTarget, Err: errors.New("target must be a non-nil pointer")}
	}
	if wantStruct && value.Elem().Kind() != reflect.Struct {
		return &BindingError{Kind: ErrInvalidBindingTarget, Err: errors.New("target must point to a struct")}
	}
	return nil
}

func (c *Context) validateBinding(dst any) error {
	if c.engine != nil && c.engine.validator != nil {
		if err := c.engine.validator.Validate(dst); err != nil {
			return &BindingError{Kind: ErrValidation, Err: err}
		}
	}
	return nil
}

func (c *Context) bindValues(dst any, values url.Values, tag string) error {
	value := reflect.ValueOf(dst).Elem()
	// Convert into a shallow struct copy. Pointer and slice fields are assigned
	// fresh values, so a conversion error does not partially alter the target.
	bound := reflect.New(value.Type()).Elem()
	bound.Set(value)
	for i := 0; i < bound.NumField(); i++ {
		field := bound.Type().Field(i)
		if field.PkgPath != "" { // Skip unexported fields, including embeddings.
			continue
		}
		name, _, _ := strings.Cut(field.Tag.Get(tag), ",")
		if name == "-" {
			continue
		}
		if name == "" {
			name = field.Name
		}
		input, exists := values[name]
		if !exists || len(input) == 0 {
			continue
		}
		if err := setBindingField(bound.Field(i), input); err != nil {
			return &BindingError{Kind: ErrBindingType, Field: name, Err: err}
		}
	}
	value.Set(bound)
	return c.validateBinding(dst)
}

func setBindingField(field reflect.Value, input []string) error {
	switch field.Kind() {
	case reflect.Pointer:
		value := reflect.New(field.Type().Elem())
		if err := setBindingScalar(value.Elem(), input[0]); err != nil {
			return err
		}
		field.Set(value)
	case reflect.Slice:
		value := reflect.MakeSlice(field.Type(), len(input), len(input))
		for i, text := range input {
			if err := setBindingScalar(value.Index(i), text); err != nil {
				return err
			}
		}
		field.Set(value)
	default:
		return setBindingScalar(field, input[0])
	}
	return nil
}

func setBindingScalar(field reflect.Value, text string) error {
	switch field.Kind() {
	case reflect.String:
		field.SetString(text)
	case reflect.Bool:
		value, err := strconv.ParseBool(text)
		if err != nil {
			return err
		}
		field.SetBool(value)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		value, err := strconv.ParseInt(text, 10, field.Type().Bits())
		if err != nil {
			return err
		}
		field.SetInt(value)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		value, err := strconv.ParseUint(text, 10, field.Type().Bits())
		if err != nil {
			return err
		}
		field.SetUint(value)
	case reflect.Float32, reflect.Float64:
		value, err := strconv.ParseFloat(text, field.Type().Bits())
		if err != nil {
			return err
		}
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return errors.New("non-finite floating point value")
		}
		field.SetFloat(value)
	default:
		return fmt.Errorf("unsupported binding field type %s", field.Type())
	}
	return nil
}
