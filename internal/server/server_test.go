package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
)

const role = `{"apiVersion":"rbac.authorization.k8s.io/v1","kind":"Role","metadata":{"name":"reader","namespace":"default"},"rules":[{"apiGroups":[""],"resources":["pods"],"verbs":["get","list"]}]}`

func request(handler http.Handler, method, path, body, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	return w
}

func expect(t *testing.T, w *httptest.ResponseRecorder, code int) {
	t.Helper()
	if w.Code != code {
		t.Fatalf("got status %d, want %d: %s", w.Code, code, w.Body.String())
	}
}

func TestAuthentication(t *testing.T) {
	token := strings.Repeat("a", 32)
	h := New(NewDemo(), token, false, "../../web")
	for _, candidate := range []string{"", "wrong"} {
		expect(t, request(h, "GET", "/api/resources/roles", "", candidate), 401)
	}
	expect(t, request(h, "GET", "/api/resources/roles", "", token), 200)
	expect(t, request(h, "GET", "/healthz", "", ""), 200)
	expect(t, request(h, "GET", "/", "", ""), 200)
	expect(t, request(New(NewDemo(), "", false, "../../web"), "GET", "/api/resources/roles", "", ""), 503)
}

func TestCRUDAndConcurrency(t *testing.T) {
	h := New(NewDemo(), "", true, "../../web")
	path := "/api/resources/roles?namespace=default"
	w := request(h, "POST", path, role, "")
	expect(t, w, 201)
	expect(t, request(h, "POST", path, role, ""), 409)
	var obj unstructured.Unstructured
	if err := json.Unmarshal(w.Body.Bytes(), &obj); err != nil {
		t.Fatal(err)
	}
	version := obj.GetResourceVersion()
	expect(t, request(h, "GET", "/api/resources/roles?namespace=staging", "", ""), 200)
	list := request(h, "GET", "/api/resources/roles", "", "")
	if !strings.Contains(list.Body.String(), "reader") {
		t.Fatal("cluster-wide list lost namespaced role")
	}
	body, _ := obj.MarshalJSON()
	update := "/api/resources/roles/reader?namespace=default"
	expect(t, request(h, "PUT", update, string(body), ""), 200)
	expect(t, request(h, "PUT", update, string(body), ""), 409)
	expect(t, request(h, "DELETE", update+"&resourceVersion="+version, "", ""), 409)
	w = request(h, "GET", path, "", "")
	var objects unstructured.UnstructuredList
	if err := json.Unmarshal(w.Body.Bytes(), &objects); err != nil {
		t.Fatal(err)
	}
	if len(objects.Items) != 1 {
		t.Fatal("expected one role")
	}
	expect(t, request(h, "DELETE", update+"&resourceVersion="+objects.Items[0].GetResourceVersion(), "", ""), 204)
	expect(t, request(h, "DELETE", update+"&resourceVersion=1", "", ""), 404)
}

func TestDryRunDoesNotPersist(t *testing.T) {
	h := New(NewDemo(), "", true, "../../web")
	expect(t, request(h, "POST", "/api/resources/roles?namespace=default&dryRun=true", role, ""), 200)
	w := request(h, "GET", "/api/resources/roles", "", "")
	var list unstructured.UnstructuredList
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 0 {
		t.Fatal("dry-run persisted an object")
	}
}

func TestRejectInvalidResources(t *testing.T) {
	h := New(NewDemo(), "", true, "../../web")
	tests := []struct {
		method, path, body string
		status             int
	}{
		{"POST", "/api/resources/roles", role, 400},
		{"POST", "/api/resources/roles?namespace=staging", role, 400},
		{"POST", "/api/resources/clusterroles?namespace=default", role, 400},
		{"POST", "/api/resources/roles?namespace=default", strings.Replace(role, `"Role"`, `"ClusterRole"`, 1), 400},
		{"POST", "/api/resources/roles?namespace=default", strings.Replace(role, `"verbs":["get","list"]`, `"verbs":[]`, 1), 400},
		{"PUT", "/api/resources/roles/reader?namespace=default", role, 400},
		{"DELETE", "/api/resources/roles/reader?namespace=default", "", 400},
		{"POST", "/api/resources/namespaces", `{}`, 405},
		{"DELETE", "/api/resources/namespaces/default", "", 405},
		{"GET", "/api/resources/secrets", "", 404},
		{"POST", "/api/resources/roles?namespace=default", "kind: Role\nkind: Role", 400},
	}
	for _, tc := range tests {
		t.Run(tc.method+tc.path+tc.body, func(t *testing.T) { expect(t, request(h, tc.method, tc.path, tc.body, ""), tc.status) })
	}
}

func TestAllResourceTypesAndImmutableBinding(t *testing.T) {
	h := New(NewDemo(), "", true, "../../web")
	clusterRole := strings.ReplaceAll(strings.ReplaceAll(role, `"kind":"Role"`, `"kind":"ClusterRole"`), `,"namespace":"default"`, "")
	expect(t, request(h, "POST", "/api/resources/clusterroles", clusterRole, ""), 201)
	for _, kind := range []string{"RoleBinding", "ClusterRoleBinding"} {
		ns, query, resourceName := `,"namespace":"default"`, "?namespace=default", "rolebindings"
		if kind == "ClusterRoleBinding" {
			ns, query, resourceName = "", "", "clusterrolebindings"
		}
		binding := `{"apiVersion":"rbac.authorization.k8s.io/v1","kind":"` + kind + `","metadata":{"name":"binding"` + ns + `},"roleRef":{"apiGroup":"rbac.authorization.k8s.io","kind":"ClusterRole","name":"reader"},"subjects":[{"kind":"ServiceAccount","name":"app","namespace":"default"}]}`
		w := request(h, "POST", "/api/resources/"+resourceName+query, binding, "")
		expect(t, w, 201)
		body := strings.Replace(w.Body.String(), `"name":"reader"`, `"name":"other"`, 1)
		expect(t, request(h, "PUT", "/api/resources/"+resourceName+"/binding"+query, body, ""), 400)
	}
	expect(t, request(h, "POST", "/api/resources/serviceaccounts?namespace=default", "apiVersion: v1\nkind: ServiceAccount\nmetadata:\n  name: app\n  namespace: default\nautomountServiceAccountToken: false\n", ""), 201)
}

func TestKubernetesRequests(t *testing.T) {
	var calls int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/apis/rbac.authorization.k8s.io/v1/namespaces/default/roles" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		if r.Method == "POST" {
			if r.URL.Query().Get("dryRun") != "All" || r.URL.Query().Get("fieldValidation") != "Strict" {
				t.Error("missing Kubernetes dry-run or strict validation")
			}
			w.WriteHeader(201)
			_, _ = w.Write([]byte(role))
			return
		}
		if calls == 2 {
			if r.URL.Query().Get("limit") != "500" {
				t.Error("missing pagination limit")
			}
			_, _ = w.Write([]byte(`{"apiVersion":"rbac.authorization.k8s.io/v1","kind":"RoleList","metadata":{"continue":"next"},"items":[]}`))
		} else {
			if r.URL.Query().Get("continue") != "next" {
				t.Error("missing continuation token")
			}
			_, _ = w.Write([]byte(`{"apiVersion":"rbac.authorization.k8s.io/v1","kind":"RoleList","metadata":{},"items":[]}`))
		}
	}))
	defer upstream.Close()
	client, err := dynamic.NewForConfig(&rest.Config{Host: upstream.URL})
	if err != nil {
		t.Fatal(err)
	}
	h := New(NewKubernetes(client), "", true, "../../web")
	expect(t, request(h, "POST", "/api/resources/roles?namespace=default&dryRun=true", role, ""), 200)
	expect(t, request(h, "GET", "/api/resources/roles?namespace=default", "", ""), 200)
	if calls != 3 {
		t.Fatalf("got %d calls, want 3", calls)
	}
}
