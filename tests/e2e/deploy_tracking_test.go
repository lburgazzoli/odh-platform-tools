//go:build e2e

/*
Copyright 2026 Luca Burgazzoli

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package e2e

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	configcrd "github.com/lburgazzoli/odh-platform-tools/config/crd"
	k8scrd "github.com/lburgazzoli/odh-platform-tools/pkg/k8s/crd"
	toolapi "github.com/lburgazzoli/odh-platform-tools/pkg/module/api/v1alpha1"
	"github.com/lburgazzoli/odh-platform-tools/tests/support/cluster"
)

type deployTarget struct {
	gvk               schema.GroupVersionKind
	key               crclient.ObjectKey
	ownedByDefinition bool
}

type watchedTarget struct {
	deployTarget
	creation watch.Interface
}

func (s *scenario) deployTargets() []deployTarget {
	roleName, bindingName := "mock-role", "mock-binding"
	if s.example {
		roleName, bindingName = s.clusterRoleName(), s.clusterRoleName()
	}
	return []deployTarget{
		{definitionGVK, crclient.ObjectKey{Name: s.name}, false},
		{crdGVK, crclient.ObjectKey{Name: s.moduleResource() + "." + s.group}, false},
		{namespaceGVK, crclient.ObjectKey{Name: s.auxNS}, false},
		{clusterRoleGVK, crclient.ObjectKey{Name: s.clusterRoleName()}, true},
		{clusterRoleBindingGVK, crclient.ObjectKey{Name: s.clusterRoleName()}, true},
		{roleGVK, crclient.ObjectKey{Namespace: s.runNS, Name: roleName}, true},
		{roleBindingGVK, crclient.ObjectKey{Namespace: s.runNS, Name: bindingName}, true},
		{deploymentGVK, crclient.ObjectKey{Namespace: s.runNS, Name: mockControllerName}, true},
		{configMapGVK, crclient.ObjectKey{Namespace: s.runNS, Name: s.platformConfigName()}, true},
	}
}

// External runs must be able to watch definitions before invoking the CLI.
// The definition CRD is shared infrastructure and stays installed.
func ensureExternalDefinitionAPI(ctx context.Context, kube crclient.Client) error {
	crd := &apiextensionsv1.CustomResourceDefinition{}
	err := kube.Get(ctx, crclient.ObjectKey{Name: toolapi.CRDName}, crd)
	if apierrors.IsNotFound(err) {
		crd = &apiextensionsv1.CustomResourceDefinition{}
		if err := yaml.Unmarshal(configcrd.PlatformModuleDefinitionYAML, crd); err != nil {
			return err
		}
		err = kube.Create(ctx, crd)
		if apierrors.IsAlreadyExists(err) {
			err = nil
		}
	}
	if err != nil {
		return err
	}
	return k8scrd.WaitForCRDToBeEstablished(ctx, kube, toolapi.CRDName)
}

// Watch from the last absence check so that apply cannot supply the ownership
// evidence for an object somebody else created before the apply.
func (s *scenario) watchRunObjects(t *testing.T, ctx context.Context) []watchedTarget {
	t.Helper()

	live := engine(t)
	_, external := live.(*cluster.External)
	if external {
		bootstrapCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		err := ensureExternalDefinitionAPI(bootstrapCtx, live.Client())
		cancel()
		if err != nil {
			t.Fatalf("preparing the definition API for external deploy: %v", err)
		}
	}

	kube, err := crclient.NewWithWatch(live.Config(), crclient.Options{Scheme: live.Client().Scheme()})
	if err != nil {
		t.Fatalf("creating the deploy watch client: %v", err)
	}

	var targets []watchedTarget
	for _, target := range s.deployTargets() {
		tracked := watchedTarget{deployTarget: target}
		list := &unstructured.UnstructuredList{}
		list.SetGroupVersionKind(target.gvk.GroupVersion().WithKind(target.gvk.Kind + "List"))
		options := &crclient.ListOptions{
			Namespace:     target.key.Namespace,
			FieldSelector: fields.OneTermEqualSelector("metadata.name", target.key.Name),
		}
		uid := s.trackedUID(target.gvk, target.key)
		err := kube.List(ctx, list, options)
		switch {
		case !external && target.gvk == definitionGVK && isAbsent(err):
			// The first deploy bootstraps this API in the privately owned Kind cluster.
		case err != nil:
			t.Fatalf("listing %s %s before deploy: %v", target.gvk.Kind, target.key, err)
		case len(list.Items) == 1:
			if uid == "" || uid != list.Items[0].GetUID() {
				t.Fatalf("refusing to deploy over untracked %s %s", target.gvk.Kind, target.key)
			}
		case len(list.Items) == 0 && uid != "":
			t.Fatalf("refusing to recreate missing tracked %s %s (UID %s)", target.gvk.Kind, target.key, uid)
		case len(list.Items) == 0 && list.GetResourceVersion() != "":
			options.Raw = &metav1.ListOptions{ResourceVersion: list.GetResourceVersion()}
			tracked.creation, err = kube.Watch(ctx, list, options)
			if err != nil {
				t.Fatalf("watching %s %s before deploy: %v", target.gvk.Kind, target.key, err)
			}
		default:
			t.Fatalf("unexpected list response for %s %s before deploy", target.gvk.Kind, target.key)
		}
		targets = append(targets, tracked)
	}
	return targets
}

func (s *scenario) ownsDeployedObject(target deployTarget, object metav1.Object, definitionUID types.UID) bool {
	if !target.ownedByDefinition {
		return object.GetAnnotations()[scenarioMarkerAnnotation] == s.marker
	}
	return slices.ContainsFunc(object.GetOwnerReferences(), func(owner metav1.OwnerReference) bool {
		return definitionUID != "" && owner.UID == definitionUID &&
			owner.Name == s.name && owner.Kind == definitionGVK.Kind &&
			owner.APIVersion == definitionGVK.GroupVersion().String()
	})
}

func (s *scenario) verifyCreation(ctx context.Context, target watchedTarget, uid types.UID, definitionUID types.UID) error {
	select {
	case event, ok := <-target.creation.ResultChan():
		if !ok {
			return fmt.Errorf("creation watch closed")
		}
		created, ok := event.Object.(*unstructured.Unstructured)
		if event.Type != watch.Added || !ok || created.GetUID() != uid ||
			!s.ownsDeployedObject(target.deployTarget, created, definitionUID) {
			return fmt.Errorf("first watch event %s did not prove ownership of UID %s", event.Type, uid)
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Track partial deploys and enroll new UIDs only with proof of creation.
func (s *scenario) trackRunObjects(t *testing.T, targets []watchedTarget) {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	kube := engine(t).Client()
	var definitionUID types.UID
	for _, target := range targets {
		object := &unstructured.Unstructured{}
		object.SetGroupVersionKind(target.gvk)
		err := kube.Get(ctx, target.key, object)
		switch {
		case isAbsent(err):
			continue
		case err != nil:
			t.Errorf("tracking %s %s: %v", target.gvk.Kind, target.key, err)
			continue
		case !s.ownsDeployedObject(target.deployTarget, object, definitionUID):
			t.Errorf("refusing to track %s %s without scenario ownership proof", target.gvk.Kind, target.key)
			continue
		}
		if target.creation != nil {
			if err := s.verifyCreation(ctx, target, object.GetUID(), definitionUID); err != nil {
				t.Errorf("refusing to track %s %s: %v", target.gvk.Kind, target.key, err)
				continue
			}
		}
		if s.record(t, target.gvk, object) && target.gvk == definitionGVK {
			definitionUID = object.GetUID()
		}
	}
}
