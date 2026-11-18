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
	"runtime"
	"testing"
	"time"

	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/lburgazzoli/odh-platform-tools/tests/support/cluster"
)

func controllerImage(t *testing.T) string {
	t.Helper()

	engine(t)
	return suiteImage.Reference()
}

func TestControllerImageRunsMockHelp(t *testing.T) {
	g := NewWithT(t)
	reference := controllerImage(t)
	active := engine(t)
	kube := active.Client()
	run := newScenario(t)
	run.createNamespace(t, run.targetNS)

	pull := corev1.PullIfNotPresent
	_, managed := active.(*cluster.Kind)
	if managed {
		pull = corev1.PullNever
	}

	pod := &corev1.Pod{
		Namespace:    run.targetNS,
		GenerateName: "odh-platform-tools-image-",
		Spec: corev1.PodSpec{
			RestartPolicy:                corev1.RestartPolicyNever,
			AutomountServiceAccountToken: new(false),
			SecurityContext: &corev1.PodSecurityContext{
				RunAsNonRoot:   new(true),
				RunAsUser:      new(int64(1001)),
				SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
			},
			Containers: []corev1.Container{{
				Name:            "help",
				Image:           reference,
				ImagePullPolicy: pull,
				Args:            []string{"module", "mock", "--help"},
				SecurityContext: &corev1.SecurityContext{
					AllowPrivilegeEscalation: new(false),
					Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
				},
			}},
		},
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	g.Expect(kube.Create(ctx, pod)).To(Succeed())
	key := client.ObjectKeyFromObject(pod)
	uid := pod.UID
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()

		victim := &corev1.Pod{Name: key.Name, Namespace: key.Namespace}
		if err := kube.Delete(cleanup, victim, client.Preconditions{UID: &uid}); err != nil && !apierrors.IsNotFound(err) {
			t.Errorf("cleaning up image smoke pod %s: %v", key, err)
		}
	})

	var current corev1.Pod
	g.Eventually(ctx, func(check Gomega) {
		check.Expect(kube.Get(ctx, key, &current)).To(Succeed())
		check.Expect(current.Status.Phase).To(Equal(corev1.PodSucceeded))
	}).WithTimeout(2 * time.Minute).WithPolling(500 * time.Millisecond).Should(Succeed())

	typed, err := kubernetes.NewForConfig(active.Config())
	g.Expect(err).NotTo(HaveOccurred())
	log, err := typed.CoreV1().Pods(key.Namespace).GetLogs(key.Name, &corev1.PodLogOptions{Container: "help"}).DoRaw(ctx)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(string(log)).To(SatisfyAll(ContainSubstring("module mock"), ContainSubstring("--platform-configmap")))

	if managed {
		var node corev1.Node
		g.Expect(kube.Get(ctx, client.ObjectKey{Name: current.Spec.NodeName}, &node)).To(Succeed())
		g.Expect(node.Status.NodeInfo.OperatingSystem).To(Equal("linux"))
		g.Expect(node.Status.NodeInfo.Architecture).To(Equal(runtime.GOARCH))
		t.Logf("ran %s in owned Kind node %s on linux/%s", reference, node.Name, runtime.GOARCH)
	}
}
