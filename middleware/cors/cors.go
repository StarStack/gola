// Package cors provides explicit-origin browser CORS policy for GoLa.
// CORS does not authenticate requests or replace CSRF protection.
package cors

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/starstack/gola"
)

// Config is copied and validated by New. Origins are exact HTTP(S) origins.
// A sole "*" origin permits any origin only when credentials are disabled.
// Methods and headers must be explicit tokens; wildcard tokens are rejected.
type Config struct {
	AllowOrigins     []string
	AllowMethods     []string
	AllowHeaders     []string
	ExposeHeaders    []string
	AllowCredentials bool
	MaxAge           time.Duration
}

// New creates middleware or returns an initialization error for invalid policy.
// Denied cross-origin requests receive 403. Valid preflights receive 204 and
// abort the remaining chain; actual requests continue through authentication.
func New(config Config) (gola.HandlerFunc, error) {
	if len(config.AllowOrigins) == 0 || len(config.AllowMethods) == 0 {
		return nil, fmt.Errorf("cors: origins and methods must be explicit")
	}
	if config.MaxAge < 0 {
		return nil, fmt.Errorf("cors: negative max age")
	}
	origins := make(map[string]bool, len(config.AllowOrigins))
	wildcard := false
	for _, origin := range config.AllowOrigins {
		if origin == "*" {
			if len(config.AllowOrigins) != 1 || config.AllowCredentials {
				return nil, fmt.Errorf("cors: wildcard origin must stand alone without credentials")
			}
			wildcard = true
		} else if !validOrigin(origin) {
			return nil, fmt.Errorf("cors: invalid origin %q", origin)
		}
		origins[origin] = true
	}
	methods, methodList, err := tokenSet(config.AllowMethods, false)
	if err != nil {
		return nil, err
	}
	headers, headerList, err := tokenSet(config.AllowHeaders, true)
	if err != nil {
		return nil, err
	}
	_, exposeList, err := tokenSet(config.ExposeHeaders, true)
	if err != nil {
		return nil, err
	}
	credentials := config.AllowCredentials
	maxAge := strconv.FormatInt(int64(config.MaxAge/time.Second), 10)
	return func(c *gola.Context) {
		h := c.Writer.Header()
		for _, name := range []string{"Access-Control-Allow-Origin", "Access-Control-Allow-Credentials", "Access-Control-Allow-Methods", "Access-Control-Allow-Headers", "Access-Control-Expose-Headers", "Access-Control-Max-Age"} {
			h.Del(name)
		}
		addVary(h, "Origin")
		originsIn := c.Request.Header.Values("Origin")
		if len(originsIn) == 0 {
			return
		}
		origin := originsIn[0]
		_, hasMethod := c.Request.Header["Access-Control-Request-Method"]
		preflight := c.Request.Method == http.MethodOptions && hasMethod
		if preflight {
			addVary(h, "Access-Control-Request-Method", "Access-Control-Request-Headers")
		}
		if len(originsIn) != 1 || !validOrigin(origin) || (!wildcard && !origins[origin]) {
			c.AbortWithStatus(http.StatusForbidden)
			return
		}
		if preflight {
			requestedMethods := c.Request.Header.Values("Access-Control-Request-Method")
			if len(requestedMethods) != 1 || !methods[requestedMethods[0]] || !allowedRequestHeaders(c.Request.Header.Values("Access-Control-Request-Headers"), headers) {
				c.AbortWithStatus(http.StatusForbidden)
				return
			}
		} else if !methods[c.Request.Method] {
			c.AbortWithStatus(http.StatusForbidden)
			return
		}
		if wildcard {
			h.Set("Access-Control-Allow-Origin", "*")
		} else {
			h.Set("Access-Control-Allow-Origin", origin)
		}
		if credentials {
			h.Set("Access-Control-Allow-Credentials", "true")
		}
		if preflight {
			h.Set("Access-Control-Allow-Methods", methodList)
			if headerList != "" {
				h.Set("Access-Control-Allow-Headers", headerList)
			}
			if maxAge != "0" {
				h.Set("Access-Control-Max-Age", maxAge)
			}
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		if exposeList != "" {
			h.Set("Access-Control-Expose-Headers", exposeList)
		}
	}, nil
}

func validOrigin(origin string) bool {
	u, err := url.Parse(origin)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.Opaque != "" || u.Path != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.ContainsAny(origin, "\r\n\t ,\\%#*") {
		return false
	}
	return u.Hostname() != ""
}

func tokenSet(values []string, header bool) (map[string]bool, string, error) {
	set := make(map[string]bool, len(values))
	list := make([]string, 0, len(values))
	for _, value := range values {
		if !validToken(value) || value == "*" {
			return nil, "", fmt.Errorf("cors: invalid method/header token %q", value)
		}
		key, output := value, value
		if header {
			key, output = strings.ToLower(value), http.CanonicalHeaderKey(value)
		}
		if !set[key] {
			set[key] = true
			list = append(list, output)
		}
	}
	return set, strings.Join(list, ", "), nil
}

func allowedRequestHeaders(values []string, allowed map[string]bool) bool {
	for _, value := range values {
		for token := range strings.SplitSeq(value, ",") {
			token = strings.TrimSpace(token)
			if !validToken(token) || !allowed[strings.ToLower(token)] {
				return false
			}
		}
	}
	return true
}

func validToken(value string) bool {
	if value == "" {
		return false
	}
	for i := range len(value) {
		c := value[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("!#$%&'*+-.^_`|~", rune(c)) {
			continue
		}
		return false
	}
	return true
}

func addVary(h http.Header, names ...string) {
	for _, name := range names {
		found := false
		for _, existing := range h.Values("Vary") {
			for token := range strings.SplitSeq(existing, ",") {
				if token = strings.TrimSpace(token); token == "*" || strings.EqualFold(token, name) {
					found = true
				}
			}
		}
		if !found {
			h.Add("Vary", name)
		}
	}
}
