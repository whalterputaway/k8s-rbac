package server

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"golang.org/x/time/rate"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/validation"
)

func New(backend Backend, token string, demoMode bool, assets string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { respond(w, 200, map[string]string{"status": "ok"}) })
	mux.HandleFunc("GET /readyz", readiness(backend))
	mux.HandleFunc("GET /api/config", func(w http.ResponseWriter, r *http.Request) {
		respond(w, 200, map[string]bool{"demo": demoMode, "authentication": token != ""})
	})
	api := http.NewServeMux()
	api.HandleFunc("GET /api/resources/{type}", func(w http.ResponseWriter, r *http.Request) {
		res, ns, ok := scope(w, r)
		if !ok {
			return
		}
		list, err := backend.List(r.Context(), res, ns)
		if err != nil {
			fail(w, err)
			return
		}
		respond(w, 200, list)
	})
	write := func(w http.ResponseWriter, r *http.Request) {
		res, ns, ok := scope(w, r)
		if !ok {
			return
		}
		if res.Kind == "Namespace" {
			respond(w, 405, map[string]string{"error": "Namespaces are read-only"})
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		body, err := io.ReadAll(r.Body)
		if err != nil {
			respond(w, 413, map[string]string{"error": "Request body exceeds 1 MiB"})
			return
		}
		data, err := manifestJSON(body)
		if err != nil {
			respond(w, 400, map[string]string{"error": err.Error()})
			return
		}
		obj := &unstructured.Unstructured{}
		if err = obj.UnmarshalJSON(data); err != nil {
			respond(w, 400, map[string]string{"error": "Invalid resource object: " + err.Error()})
			return
		}
		update := r.Method == http.MethodPut
		if err = validate(obj, res, ns, r.PathValue("name"), update); err != nil {
			respond(w, 400, map[string]string{"error": err.Error()})
			return
		}
		dry := r.URL.Query().Get("dryRun") == "true"
		result, err := backend.Write(r.Context(), res, ns, obj, update, dry)
		if err != nil {
			fail(w, err)
			return
		}
		status := http.StatusCreated
		if update || dry {
			status = http.StatusOK
		}
		slog.Info("RBAC write", "kind", res.Kind, "namespace", ns, "name", obj.GetName(), "update", update, "dryRun", dry)
		respond(w, status, result)
	}
	api.HandleFunc("POST /api/resources/{type}", write)
	api.HandleFunc("PUT /api/resources/{type}/{name}", write)
	api.HandleFunc("DELETE /api/resources/{type}/{name}", func(w http.ResponseWriter, r *http.Request) {
		res, ns, ok := scope(w, r)
		if !ok {
			return
		}
		if res.Kind == "Namespace" {
			respond(w, 405, map[string]string{"error": "Namespaces are read-only"})
			return
		}
		if res.Namespaced && ns == "" {
			respond(w, 400, map[string]string{"error": "namespace is required"})
			return
		}
		rv := r.URL.Query().Get("resourceVersion")
		if rv == "" {
			respond(w, 400, map[string]string{"error": "resourceVersion is required"})
			return
		}
		if err := backend.Delete(r.Context(), res, ns, r.PathValue("name"), rv); err != nil {
			fail(w, err)
			return
		}
		slog.Info("RBAC delete", "kind", res.Kind, "namespace", ns, "name", r.PathValue("name"))
		w.WriteHeader(http.StatusNoContent)
	})
	authFailures := rate.NewLimiter(5, 10)
	requests := rate.NewLimiter(20, 40)
	mux.Handle("/api/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !sameOrigin(r) {
			respond(w, 403, map[string]string{"error": "Cross-origin API requests are not allowed"})
			return
		}
		if !demoMode && ValidateAdminToken(token) != nil {
			respond(w, 503, map[string]string{"error": "Authentication is not configured"})
			return
		}
		if token != "" {
			want := sha256.Sum256([]byte(token))
			got := sha256.Sum256([]byte(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")))
			if len(r.Header.Values("Authorization")) != 1 || !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") || subtle.ConstantTimeCompare(want[:], got[:]) != 1 {
				if !authFailures.Allow() {
					w.Header().Set("Retry-After", "1")
					respond(w, 429, map[string]string{"error": "Too many authentication failures"})
					return
				}
				respond(w, 401, map[string]string{"error": "Invalid administrator token"})
				return
			}
		}
		if !requests.Allow() {
			w.Header().Set("Retry-After", "1")
			respond(w, 429, map[string]string{"error": "Too many API requests"})
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		r = r.WithContext(ctx)
		api.ServeHTTP(w, r)
	}))
	mux.Handle("/", staticFiles(assets))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		start := time.Now()
		mux.ServeHTTP(w, r)
		slog.Debug("HTTP request", "method", r.Method, "path", r.URL.Path, "elapsed", time.Since(start))
	})
}

func scope(w http.ResponseWriter, r *http.Request) (resource, string, bool) {
	if err := validateQuery(r); err != nil {
		respond(w, 400, map[string]string{"error": err.Error()})
		return resource{}, "", false
	}
	res, ok := resources[r.PathValue("type")]
	if !ok {
		respond(w, 404, map[string]string{"error": "Unknown resource type"})
		return resource{}, "", false
	}
	ns := r.URL.Query().Get("namespace")
	if ns != "" && (!res.Namespaced || len(validation.IsDNS1123Label(ns)) > 0) {
		respond(w, 400, map[string]string{"error": "Invalid namespace for this resource"})
		return resource{}, "", false
	}
	return res, ns, true
}

func validate(obj *unstructured.Unstructured, res resource, ns, name string, update bool) error {
	if obj.GetKind() != res.Kind || obj.GetAPIVersion() != res.GVR.GroupVersion().String() {
		return fmt.Errorf("expected %s %s", res.GVR.GroupVersion(), res.Kind)
	}
	if obj.GetName() == "" || strings.ContainsAny(obj.GetName(), "/%") || obj.GetName() == "." || obj.GetName() == ".." {
		return errors.New("metadata.name must be a valid resource name")
	}
	if res.Namespaced && ns == "" {
		return errors.New("namespace is required")
	}
	if obj.GetNamespace() != ns {
		return errors.New("metadata.namespace must match the selected namespace")
	}
	if update && (name != obj.GetName() || obj.GetResourceVersion() == "") {
		return errors.New("updates require a matching name and metadata.resourceVersion")
	}
	if !update && obj.GetResourceVersion() != "" {
		return errors.New("new resources must not set metadata.resourceVersion")
	}
	if res.Kind == "Role" || res.Kind == "ClusterRole" {
		rules, found, err := unstructured.NestedSlice(obj.Object, "rules")
		if err != nil {
			return errors.New("rules must be an array")
		}
		if !found && obj.Object["aggregationRule"] == nil {
			return errors.New("rules are required")
		}
		for _, raw := range rules {
			rule, ok := raw.(map[string]interface{})
			if !ok {
				return errors.New("each rule must be an object")
			}
			verbs, _, err := unstructured.NestedStringSlice(rule, "verbs")
			if err != nil || len(verbs) == 0 {
				return errors.New("each rule requires verbs")
			}
			urls, _, err := unstructured.NestedStringSlice(rule, "nonResourceURLs")
			if err != nil {
				return err
			}
			if len(urls) > 0 {
				if res.Namespaced || rule["resources"] != nil || rule["apiGroups"] != nil || rule["resourceNames"] != nil {
					return errors.New("nonResourceURLs require a ClusterRole rule without resource fields")
				}
			} else {
				items, _, err := unstructured.NestedStringSlice(rule, "resources")
				if err != nil || len(items) == 0 {
					return errors.New("each resource rule requires resources")
				}
				groups, _, err := unstructured.NestedStringSlice(rule, "apiGroups")
				if err != nil || len(groups) == 0 {
					return errors.New("each resource rule requires apiGroups; use an empty string for core resources")
				}
			}
		}
	}
	if res.Kind == "RoleBinding" || res.Kind == "ClusterRoleBinding" {
		ref, _, err := unstructured.NestedStringMap(obj.Object, "roleRef")
		if err != nil || ref["name"] == "" || ref["apiGroup"] != "rbac.authorization.k8s.io" || (ref["kind"] != "ClusterRole" && (ref["kind"] != "Role" || !res.Namespaced)) {
			return errors.New("invalid roleRef")
		}
		subjects, _, err := unstructured.NestedSlice(obj.Object, "subjects")
		if err != nil {
			return errors.New("subjects must be an array")
		}
		for _, raw := range subjects {
			s, ok := raw.(map[string]interface{})
			if !ok {
				return errors.New("each subject must be an object")
			}
			subjectName, _ := s["name"].(string)
			if subjectName == "" {
				return errors.New("each subject requires a name")
			}
			switch s["kind"] {
			case "ServiceAccount":
				if s["apiGroup"] != nil && s["apiGroup"] != "" {
					return errors.New("ServiceAccount apiGroup must be empty")
				}
				subjectNS, _ := s["namespace"].(string)
				if subjectNS == "" && !res.Namespaced {
					return errors.New("ServiceAccount subjects require a namespace in ClusterRoleBindings")
				}
			case "User", "Group":
				if s["apiGroup"] != "rbac.authorization.k8s.io" {
					return errors.New("User and Group subjects require the RBAC apiGroup")
				}
			default:
				return errors.New("subject kind must be User, Group or ServiceAccount")
			}
		}
	}
	delete(obj.Object, "status")
	unstructured.RemoveNestedField(obj.Object, "metadata", "managedFields")
	return nil
}

func fail(w http.ResponseWriter, err error) {
	code := 502
	message := "Kubernetes request failed"
	var status apierrors.APIStatus
	if errors.As(err, &status) && status.Status().Code >= 400 {
		code = int(status.Status().Code)
		message = err.Error()
	}
	slog.Warn("Kubernetes request failed", "status", code, "error", err)
	respond(w, code, map[string]string{"error": message})
}

func respond(w http.ResponseWriter, status int, value interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		slog.Debug("Response write failed", "error", err)
	}
}
