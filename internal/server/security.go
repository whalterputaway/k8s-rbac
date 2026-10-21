package server

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	kubeyaml "k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/yaml"
)

func ValidateAdminToken(token string) error {
	if len(token) < 32 || len(token) > 4096 {
		return errors.New("ADMIN_TOKEN must contain between 32 and 4096 printable ASCII characters")
	}
	for _, character := range []byte(token) {
		if character < 33 || character > 126 {
			return errors.New("ADMIN_TOKEN must not contain whitespace or non-ASCII characters")
		}
	}
	return nil
}

func sameOrigin(r *http.Request) bool {
	if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		return false
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		parsed, err := url.Parse(origin)
		return err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && strings.EqualFold(parsed.Host, r.Host) && parsed.User == nil && parsed.Path == "" && parsed.RawQuery == "" && parsed.Fragment == ""
	}
	return true
}

func validateQuery(r *http.Request) error {
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return errors.New("invalid query string")
	}
	for key, values := range query {
		if len(values) != 1 {
			return errors.New("query parameters must not be repeated")
		}
		switch key {
		case "namespace":
		case "dryRun":
			if (r.Method != http.MethodPost && r.Method != http.MethodPut) || (values[0] != "true" && values[0] != "false") {
				return errors.New("dryRun must be true or false on create or update requests")
			}
		case "resourceVersion":
			if r.Method != http.MethodDelete {
				return errors.New("resourceVersion query parameter is only supported on delete requests")
			}
		default:
			return errors.New("unsupported query parameter")
		}
	}
	return nil
}

func manifestJSON(body []byte) ([]byte, error) {
	reader := kubeyaml.NewYAMLReader(bufio.NewReader(bytes.NewReader(body)))
	first, err := reader.Read()
	if err != nil {
		return nil, errors.New("a resource manifest is required")
	}
	for {
		next, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if len(bytes.TrimSpace(next)) != 0 {
			return nil, errors.New("submit exactly one resource per request")
		}
	}
	return yaml.YAMLToJSONStrict(first)
}

func staticFiles(directory string) http.Handler {
	files := map[string]string{"/": "index.html", "/index.html": "index.html", "/app.js": "app.js", "/style.css": "style.css", "/graph.js": "graph.js", "/graph-model.js": "graph-model.js"}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		name, allowed := files[r.URL.Path]
		if !allowed {
			http.NotFound(w, r)
			return
		}
		root, err := os.OpenRoot(directory)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer root.Close()
		file, err := root.Open(name)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer file.Close()
		stat, err := file.Stat()
		if err != nil || !stat.Mode().IsRegular() {
			http.NotFound(w, r)
			return
		}
		http.ServeContent(w, r, name, stat.ModTime(), file)
	})
}

func readiness(backend Backend) http.HandlerFunc {
	var mu sync.Mutex
	var next time.Time
	var lastErr error
	return func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		if time.Now().After(next) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			lastErr = backend.Ready(ctx)
			cancel()
			next = time.Now().Add(5 * time.Second)
		}
		err := lastErr
		mu.Unlock()
		if err != nil {
			respond(w, 503, map[string]string{"error": "Kubernetes unavailable"})
			return
		}
		respond(w, 200, map[string]string{"status": "ready"})
	}
}
