package gola

import "strings"

type route struct {
	method, path    string
	names           []string
	group           *RouterGroup
	handlers, chain []HandlerFunc
}
type routeNode struct {
	static   map[string]*routeNode
	param    *routeNode
	wildcard *route
	endpoint *route
}

func splitPath(path string) []string {
	if !strings.HasPrefix(path, "/") {
		return nil
	}
	return strings.Split(path[1:], "/")
}
func validToken(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("!#$%&'*+-.^_`|~", c)) {
			return false
		}
	}
	return true
}
func parseRoute(path string) ([]string, []string) {
	if !strings.HasPrefix(path, "/") || strings.ContainsAny(path, "?#\r\n\x00") {
		panic("gola: route must be an absolute path without query, fragment or control characters")
	}
	parts := splitPath(path)
	names := []string{}
	seen := map[string]bool{}
	for i, p := range parts {
		if strings.HasPrefix(p, ":") || strings.HasPrefix(p, "*") {
			name := p[1:]
			if name == "" || strings.ContainsAny(name, ":* \t") || seen[name] {
				panic("gola: invalid or duplicate route parameter: " + p)
			}
			if p[0] == '*' && i != len(parts)-1 {
				panic("gola: wildcard must be the final segment")
			}
			seen[name] = true
			names = append(names, name)
		} else if strings.ContainsAny(p, ":*") {
			panic("gola: parameters must occupy an entire segment")
		}
	}
	return parts, names
}
func (e *Engine) addRoute(method, path string, g *RouterGroup, h []HandlerFunc) {
	e.assertMutable()
	checkHandlers(h)
	if !validToken(method) {
		panic("gola: invalid HTTP method")
	}
	parts, names := parseRoute(path)
	root := e.trees[method]
	if root == nil {
		root = &routeNode{}
		e.trees[method] = root
	}
	n := root
	r := &route{method: method, path: path, names: names, group: g, handlers: append([]HandlerFunc(nil), h...)}
	for _, p := range parts {
		if strings.HasPrefix(p, "*") {
			if n.wildcard != nil {
				panic("gola: conflicting wildcard route: " + method + " " + path)
			}
			n.wildcard = r
			e.routes = append(e.routes, r)
			return
		}
		if strings.HasPrefix(p, ":") {
			if n.param == nil {
				n.param = &routeNode{}
			}
			n = n.param
		} else {
			if n.static == nil {
				n.static = make(map[string]*routeNode)
			}
			child := n.static[p]
			if child == nil {
				child = &routeNode{}
				n.static[p] = child
			}
			n = child
		}
	}
	if n.endpoint != nil {
		panic("gola: duplicate or structurally conflicting route: " + method + " " + path)
	}
	n.endpoint = r
	e.routes = append(e.routes, r)
}

// match backtracks when a more specific branch cannot match the entire path.
// Captures are positional; parameter names belong to the matched route.
func (n *routeNode) match(parts []string, i int, values []string) (*route, []string) {
	if i == len(parts) {
		return n.endpoint, values
	}
	if child := n.static[parts[i]]; child != nil {
		if r, v := child.match(parts, i+1, values); r != nil {
			return r, v
		}
	}
	if n.param != nil && parts[i] != "" {
		if r, v := n.param.match(parts, i+1, append(values, parts[i])); r != nil {
			return r, v
		}
	}
	if n.wildcard != nil {
		return n.wildcard, append(values, strings.Join(parts[i:], "/"))
	}
	return nil, nil
}
