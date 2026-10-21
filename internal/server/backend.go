package server

import (
	"context"
	"fmt"
	"strconv"
	"sync"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

type resource struct {
	Kind       string
	GVR        schema.GroupVersionResource
	Namespaced bool
}

var resources = map[string]resource{
	"roles":               {"Role", schema.GroupVersionResource{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "roles"}, true},
	"clusterroles":        {"ClusterRole", schema.GroupVersionResource{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "clusterroles"}, false},
	"rolebindings":        {"RoleBinding", schema.GroupVersionResource{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "rolebindings"}, true},
	"clusterrolebindings": {"ClusterRoleBinding", schema.GroupVersionResource{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "clusterrolebindings"}, false},
	"serviceaccounts":     {"ServiceAccount", schema.GroupVersionResource{Version: "v1", Resource: "serviceaccounts"}, true},
	"namespaces":          {"Namespace", schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}, false},
}

type Backend interface {
	List(context.Context, resource, string) (*unstructured.UnstructuredList, error)
	Write(context.Context, resource, string, *unstructured.Unstructured, bool, bool) (*unstructured.Unstructured, error)
	Delete(context.Context, resource, string, string, string) error
}

type kubernetes struct{ client dynamic.Interface }

func NewKubernetes(client dynamic.Interface) Backend { return &kubernetes{client} }

func (k *kubernetes) target(r resource, ns string) dynamic.ResourceInterface {
	if r.Namespaced {
		return k.client.Resource(r.GVR).Namespace(ns)
	}
	return k.client.Resource(r.GVR)
}

func (k *kubernetes) List(ctx context.Context, r resource, ns string) (*unstructured.UnstructuredList, error) {
	result := &unstructured.UnstructuredList{}
	result.SetAPIVersion(r.GVR.GroupVersion().String())
	result.SetKind(r.Kind + "List")
	result.Items = []unstructured.Unstructured{}
	options := metav1.ListOptions{Limit: 500}
	for {
		page, err := k.target(r, ns).List(ctx, options)
		if err != nil {
			return nil, err
		}
		result.Items = append(result.Items, page.Items...)
		if page.GetContinue() == "" {
			return result, nil
		}
		options.Continue = page.GetContinue()
	}
}

func (k *kubernetes) Write(ctx context.Context, r resource, ns string, obj *unstructured.Unstructured, update, dry bool) (*unstructured.Unstructured, error) {
	var dryRun []string
	if dry {
		dryRun = []string{metav1.DryRunAll}
	}
	if update {
		return k.target(r, ns).Update(ctx, obj, metav1.UpdateOptions{DryRun: dryRun, FieldValidation: "Strict"})
	}
	return k.target(r, ns).Create(ctx, obj, metav1.CreateOptions{DryRun: dryRun, FieldValidation: "Strict"})
}

func (k *kubernetes) Delete(ctx context.Context, r resource, ns, name, rv string) error {
	return k.target(r, ns).Delete(ctx, name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{ResourceVersion: &rv}})
}

type demo struct {
	mu      sync.Mutex
	objects map[string]*unstructured.Unstructured
	version int
}

func NewDemo() Backend {
	d := &demo{objects: map[string]*unstructured.Unstructured{}, version: 1}
	for _, ns := range []string{"default", "production", "staging", "kube-system"} {
		o := &unstructured.Unstructured{Object: map[string]interface{}{"apiVersion": "v1", "kind": "Namespace", "metadata": map[string]interface{}{"name": ns, "resourceVersion": "1"}}}
		d.objects["namespaces//"+ns] = o
	}
	return d
}

func (d *demo) List(_ context.Context, r resource, ns string) (*unstructured.UnstructuredList, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	list := &unstructured.UnstructuredList{Items: []unstructured.Unstructured{}}
	list.SetAPIVersion(r.GVR.GroupVersion().String())
	list.SetKind(r.Kind + "List")
	for _, obj := range d.objects {
		if obj.GetKind() == r.Kind && (ns == "" || obj.GetNamespace() == ns) {
			list.Items = append(list.Items, *obj.DeepCopy())
		}
	}
	return list, nil
}

func (d *demo) Write(_ context.Context, r resource, ns string, obj *unstructured.Unstructured, update, dry bool) (*unstructured.Unstructured, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	key := r.GVR.Resource + "/" + ns + "/" + obj.GetName()
	old, exists := d.objects[key]
	gr := r.GVR.GroupResource()
	if update && !exists {
		return nil, apierrors.NewNotFound(gr, obj.GetName())
	}
	if !update && exists {
		return nil, apierrors.NewAlreadyExists(gr, obj.GetName())
	}
	if r.Namespaced && d.objects["namespaces//"+ns] == nil {
		return nil, apierrors.NewNotFound(resources["namespaces"].GVR.GroupResource(), ns)
	}
	if update && old.GetResourceVersion() != obj.GetResourceVersion() {
		return nil, apierrors.NewConflict(gr, obj.GetName(), fmt.Errorf("resource has changed; refresh before saving"))
	}
	if update && (r.Kind == "RoleBinding" || r.Kind == "ClusterRoleBinding") && fmt.Sprint(old.Object["roleRef"]) != fmt.Sprint(obj.Object["roleRef"]) {
		return nil, apierrors.NewBadRequest("roleRef is immutable; create a new binding")
	}
	copy := obj.DeepCopy()
	if !dry {
		d.version++
		copy.SetResourceVersion(strconv.Itoa(d.version))
		d.objects[key] = copy.DeepCopy()
	}
	return copy, nil
}

func (d *demo) Delete(_ context.Context, r resource, ns, name, rv string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	key := r.GVR.Resource + "/" + ns + "/" + name
	obj := d.objects[key]
	if obj == nil {
		return apierrors.NewNotFound(r.GVR.GroupResource(), name)
	}
	if obj.GetResourceVersion() != rv {
		return apierrors.NewConflict(r.GVR.GroupResource(), name, fmt.Errorf("resource has changed; refresh before deleting"))
	}
	delete(d.objects, key)
	return nil
}
