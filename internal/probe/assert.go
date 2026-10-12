package probe

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/yoockh/sebab/internal/config"
)

// maxAssertBody bounds how much of the body is kept for assertions.
const maxAssertBody = 1 << 20

// checkAssertions returns a description of the first failing assertion, or "".
func checkAssertions(asserts []config.Assertion, h http.Header, body []byte) (string, string) {
	var parsed any
	parsedOK, parsedTried := false, false
	for _, a := range asserts {
		var ok bool
		var desc, actual string
		switch a.Type {
		case "body":
			ok = strings.Contains(string(body), a.Value)
			if a.Negate {
				desc = fmt.Sprintf("body contains %q", a.Value)
			} else {
				desc = fmt.Sprintf("body does not contain %q", a.Value)
			}
		case "header":
			v, present := h[http.CanonicalHeaderKey(a.Name)]
			joined := strings.Join(v, ", ")
			ok = present && strings.Contains(strings.ToLower(joined), strings.ToLower(a.Value))
			actual = joined
			switch {
			case a.Negate:
				desc = fmt.Sprintf("header %s is %q", a.Name, joined)
			case !present:
				desc = fmt.Sprintf("header %s is missing", a.Name)
			default:
				desc = fmt.Sprintf("header %s is %q, expected it to contain %q", a.Name, short(joined), a.Value)
			}
		case "json":
			if !parsedTried {
				parsedTried = true
				parsedOK = json.Unmarshal(body, &parsed) == nil
			}
			if !parsedOK {
				return "response is not valid JSON", ""
			}
			v, found := lookup(parsed, a.Path)
			text := jsonText(v)
			ok = found && (a.Value == "" || text == a.Value)
			actual = text
			switch {
			case a.Negate:
				desc = fmt.Sprintf("%s is %q", a.Path, short(text))
			case !found:
				desc = fmt.Sprintf("%s is missing", a.Path)
			default:
				desc = fmt.Sprintf("%s is %q, expected %q", a.Path, short(text), a.Value)
			}
		}
		if ok == a.Negate {
			return desc, actual
		}
	}
	return "", ""
}

func short(s string) string {
	if len(s) > 60 {
		return s[:57] + "..."
	}
	return s
}

// jsonText renders a decoded JSON value the way users write it in config:
// strings without quotes, everything else as JSON.
func jsonText(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// lookup walks a path like data.items[0].state.
func lookup(root any, path string) (any, bool) {
	cur := root
	for _, part := range splitPath(path) {
		if isIndex(part) {
			idx, err := strconv.Atoi(part[1:])
			if err != nil {
				return nil, false
			}
			arr, ok := cur.([]any)
			if !ok || idx < 0 || idx >= len(arr) {
				return nil, false
			}
			cur = arr[idx]
			continue
		}
		obj, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		cur, ok = obj[part]
		if !ok {
			return nil, false
		}
	}
	return cur, true
}

// splitPath turns "a.b[0].c" into ["a", "b", "#0", "c"] with indices marked.
func splitPath(path string) []string {
	var out []string
	for _, seg := range strings.Split(path, ".") {
		for seg != "" {
			i := strings.IndexByte(seg, '[')
			if i < 0 {
				out = append(out, seg)
				break
			}
			if i > 0 {
				out = append(out, seg[:i])
			}
			j := strings.IndexByte(seg, ']')
			if j < i {
				out = append(out, seg)
				break
			}
			out = append(out, "#"+seg[i+1:j])
			seg = seg[j+1:]
		}
	}
	return out
}

func isIndex(part string) bool { return strings.HasPrefix(part, "#") }
