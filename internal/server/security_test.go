package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
)

func TestTokenValidation(t *testing.T) {
	for _, token := range []string{"", "short", strings.Repeat(" ", 32), strings.Repeat("x", 32) + "\n", strings.Repeat("x", 4097), strings.Repeat("я", 32)} {
		if ValidateAdminToken(token) == nil {
			t.Fatal("invalid token accepted")
		}
	}
	if err := ValidateAdminToken(strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	expect(t, request(New(NewDemo(), "short", false, "../../web"), "GET", "/api/resources/roles", "", "short"), 503)
}

func TestUnsafeQueriesDoNotWrite(t *testing.T) {
	h := New(NewDemo(), "", true, "../../web")
	for _, query := range []string{
		"namespace=default&dryRun=ture", "namespace=default&dryRun=", "namespace=default&dryRun=true&dryRun=false",
		"namespace=default&dryrun=true", "namespace=default&namespace=staging", "namespace=default&x=%zz",
	} {
		expect(t, request(h, "POST", "/api/resources/roles?"+query, role, ""), 400)
	}
	w := request(h, "GET", "/api/resources/roles", "", "")
	var list unstructured.UnstructuredList
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 0 {
		t.Fatal("invalid dry-run request wrote a resource")
	}
}

func TestManifestLimits(t *testing.T) {
	for _, body := range []string{role + "\n---\n" + role, "kind: Role\nkind: ClusterRole", "null", "[]", "{}"} {
		h := New(NewDemo(), "", true, "../../web")
		expect(t, request(h, "POST", "/api/resources/roles?namespace=default", body, ""), 400)
	}
	expect(t, request(New(NewDemo(), "", true, "../../web"), "POST", "/api/resources/roles?namespace=default", strings.Repeat("x", (1<<20)+1), ""), 413)
}

func TestCrossOriginRequests(t *testing.T) {
	token := strings.Repeat("a", 64)
	h := New(NewDemo(), token, false, "../../web")
	for _, origin := range []string{"https://attacker.example", "null", "http://example.com@attacker.example", "http://example.com/path"} {
		r := httptest.NewRequest("POST", "http://example.com/api/resources/roles?namespace=default", strings.NewReader(role))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Origin", origin)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		expect(t, w, 403)
	}
	r := httptest.NewRequest("GET", "http://example.com/api/resources/roles", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Origin", "https://example.com")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	expect(t, w, 200)
	r.Header.Del("Origin")
	r.Header.Set("Sec-Fetch-Site", "cross-site")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	expect(t, w, 403)
}

func TestAuthenticationThrottling(t *testing.T) {
	token := strings.Repeat("a", 64)
	h := New(NewDemo(), token, false, "../../web")
	for range 10 {
		expect(t, request(h, "GET", "/api/resources/roles", "", "wrong"), 401)
	}
	w := request(h, "GET", "/api/resources/roles", "", "wrong")
	expect(t, w, 429)
	if w.Header().Get("Retry-After") == "" {
		t.Fatal("missing Retry-After")
	}
	expect(t, request(h, "GET", "/api/resources/roles", "", token), 200)
	r := httptest.NewRequest("GET", "/api/resources/roles", nil)
	r.Header.Add("Authorization", "Bearer "+token)
	r.Header.Add("Authorization", "Bearer wrong")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code == 200 {
		t.Fatal("duplicate authorization headers accepted")
	}
}

func TestStaticFileAllowlist(t *testing.T) {
	dir := t.TempDir()
	for name, value := range map[string]string{"index.html": "<html>ok</html>", "app.js": "'use strict';", ".env": "SECRET=private", "private.txt": "private"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	h := New(NewDemo(), "", true, dir)
	for _, path := range []string{"/.env", "/private.txt", "/.git/config", "/subdir/", "/api/../private.txt"} {
		w := request(h, "GET", path, "", "")
		if w.Code == 200 || strings.Contains(w.Body.String(), "SECRET=private") {
			t.Fatal("private static path exposed: " + path)
		}
	}
	for _, path := range []string{"/", "/app.js"} {
		expect(t, request(h, "GET", path, "", ""), 200)
	}
	expect(t, request(h, "POST", "/app.js", "", ""), 405)
	w := request(h, "GET", "/", "", "")
	for _, header := range []string{"Content-Security-Policy", "X-Content-Type-Options", "X-Frame-Options", "Referrer-Policy", "Permissions-Policy"} {
		if w.Header().Get(header) == "" {
			t.Fatal("missing security header: " + header)
		}
	}
}

func TestStaticSymlinksCannotEscapeRoot(t *testing.T) {
	dir, outside := t.TempDir(), t.TempDir()
	private := filepath.Join(outside, "secret")
	if err := os.WriteFile(private, []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(private, filepath.Join(dir, "app.js")); err != nil {
		t.Skip("symlinks unavailable: " + err.Error())
	}
	expect(t, request(New(NewDemo(), "", true, dir), "GET", "/app.js", "", ""), 404)
}

func TestReadinessIsCachedAndBounded(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/api/v1/namespaces" || r.URL.Query().Get("limit") != "1" {
			t.Error("readiness requested an unbounded resource list")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"apiVersion":"v1","kind":"NamespaceList","items":[]}`))
	}))
	defer upstream.Close()
	client, err := dynamic.NewForConfig(&rest.Config{Host: upstream.URL})
	if err != nil {
		t.Fatal(err)
	}
	h := New(NewKubernetes(client), strings.Repeat("a", 64), false, "../../web")
	var requests sync.WaitGroup
	for range 20 {
		requests.Add(1)
		go func() {
			defer requests.Done()
			w := request(h, "GET", "/readyz", "", "")
			if w.Code != 200 {
				t.Errorf("readiness status: %d", w.Code)
			}
		}()
	}
	requests.Wait()
	if calls.Load() != 1 {
		t.Fatalf("readiness made %d upstream requests, want 1", calls.Load())
	}
}

type brokenBackend struct{ Backend }

func (b brokenBackend) Write(context.Context, resource, string, *unstructured.Unstructured, bool, bool) (*unstructured.Unstructured, error) {
	return nil, errors.New("transport failed with internal-credential")
}

func TestInternalErrorsAreNotDisclosed(t *testing.T) {
	h := New(brokenBackend{NewDemo()}, "", true, "../../web")
	w := request(h, "POST", "/api/resources/roles?namespace=default", role, "")
	expect(t, w, 502)
	if strings.Contains(w.Body.String(), "internal-credential") {
		t.Fatal("internal error disclosed to client")
	}
}

func FuzzManifestValidation(f *testing.F) {
	for _, seed := range []string{role, "null", "[]", "metadata: []", "a: &a [*a]", "kind: Role\n---\nkind: Role", `{"kind":"RoleBinding","apiVersion":"rbac.authorization.k8s.io/v1","metadata":{"name":"test","namespace":"default"},"roleRef":{"kind":"Role","name":"test","apiGroup":"rbac.authorization.k8s.io"},"subjects":[{"kind":[],"name":"test"}]}`} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, input string) {
		if len(input) > 65536 {
			return
		}
		data, err := manifestJSON([]byte(input))
		if err != nil {
			return
		}
		obj := &unstructured.Unstructured{}
		if err := obj.UnmarshalJSON(data); err != nil {
			return
		}
		for _, resource := range resources {
			ns := ""
			if resource.Namespaced {
				ns = "default"
			}
			_ = validate(obj.DeepCopy(), resource, ns, "", false)
		}
	})
}
