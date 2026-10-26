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

package crd

import (
	"context"
	"sync"
	"testing"
	"time"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	crclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	. "github.com/onsi/gomega"
)

// trueConditions builds condition sets whose named types all report
// True.
func trueConditions(
	types ...apiextensionsv1.CustomResourceDefinitionConditionType,
) []apiextensionsv1.CustomResourceDefinitionCondition {
	conditions := make([]apiextensionsv1.CustomResourceDefinitionCondition, 0, len(types))
	for _, conditionType := range types {
		conditions = append(conditions, apiextensionsv1.CustomResourceDefinitionCondition{
			Type:   conditionType,
			Status: apiextensionsv1.ConditionTrue,
		})
	}

	return conditions
}

// crdScript is the scripted CRD read behavior behind the wait's
// client: frames of condition sets served in order with the last
// frame sticky, and a call counter. A name without frames falls
// through to the underlying fake client — a stored absence surfaces
// as a real NotFound. The scripted reads honor the invocation
// context, like a real client.
type crdScript struct {
	mu     sync.Mutex
	calls  map[string]int
	frames map[string][][]apiextensionsv1.CustomResourceDefinitionCondition
}

// newCRDScriptClient builds the scripted client.
func newCRDScriptClient(
	t *testing.T,
	frames map[string][][]apiextensionsv1.CustomResourceDefinitionCondition,
) (crclient.Client, *crdScript) {
	t.Helper()

	script := &crdScript{
		calls:  map[string]int{},
		frames: frames,
	}

	scheme := runtime.NewScheme()
	if err := apiextensionsv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}

	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(
				ctx context.Context,
				c crclient.WithWatch,
				key crclient.ObjectKey,
				obj crclient.Object,
				opts ...crclient.GetOption,
			) error {
				if err := ctx.Err(); err != nil {
					return err
				}

				nameFrames, scripted := script.frames[key.Name]
				if !scripted {
					return c.Get(ctx, key, obj, opts...)
				}

				script.mu.Lock()
				script.calls[key.Name]++
				call := script.calls[key.Name]
				script.mu.Unlock()

				if call > len(nameFrames) {
					call = len(nameFrames)
				}

				crd, typed := obj.(*apiextensionsv1.CustomResourceDefinition)
				if !typed {
					return c.Get(ctx, key, obj, opts...)
				}

				crd.Status.Conditions = nameFrames[call-1]

				return nil
			},
		}).
		Build()

	return client, script
}

// requestCount returns how many reads the name received.
func (s *crdScript) requestCount(name string) int {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.calls[name]
}

// TestWaitForCRDToBeEstablishedPolls pins the polling: a CRD that is
// not established on the first read is retried until both conditions
// hold, without inventing success from a partial state.
func TestWaitForCRDToBeEstablishedPolls(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	client, script := newCRDScriptClient(t, map[string][][]apiextensionsv1.CustomResourceDefinitionCondition{
		"widgets.example.com": {
			trueConditions(apiextensionsv1.NamesAccepted),
			trueConditions(apiextensionsv1.Established, apiextensionsv1.NamesAccepted),
		},
	})

	g.Expect(WaitForCRDToBeEstablished(
		t.Context(), client, "widgets.example.com", WithPollInterval(time.Millisecond),
	)).To(Succeed())
	g.Expect(script.requestCount("widgets.example.com")).To(BeNumerically(">=", 2))
}

// TestWaitForCRDToBeEstablishedRequiresBothConditions pins that
// establishment alone is not success: NamesAccepted must hold too.
func TestWaitForCRDToBeEstablishedRequiresBothConditions(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	client, script := newCRDScriptClient(t, map[string][][]apiextensionsv1.CustomResourceDefinitionCondition{
		"widgets.example.com": {
			trueConditions(apiextensionsv1.Established),
			trueConditions(apiextensionsv1.Established, apiextensionsv1.NamesAccepted),
		},
	})

	g.Expect(WaitForCRDToBeEstablished(
		t.Context(), client, "widgets.example.com", WithPollInterval(time.Millisecond),
	)).To(Succeed())
	g.Expect(script.requestCount("widgets.example.com")).To(BeNumerically(">=", 2))
}

// TestWaitForCRDToBeEstablishedHonorsPollInterval pins the option
// resolution through behavior: a wait whose second observation
// succeeds takes at least one configured interval, stays well inside
// the default when the interval is tightened, and non-positive
// intervals and nil options alongside the real one change nothing —
// a zero or negative interval would collapse the delay, a fall-back
// to the default would exceed the bound.
func TestWaitForCRDToBeEstablishedHonorsPollInterval(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	client, _ := newCRDScriptClient(t, map[string][][]apiextensionsv1.CustomResourceDefinitionCondition{
		"widgets.example.com": {
			trueConditions(apiextensionsv1.NamesAccepted),
			trueConditions(apiextensionsv1.Established, apiextensionsv1.NamesAccepted),
		},
	})

	start := time.Now()
	g.Expect(WaitForCRDToBeEstablished(
		t.Context(), client, "widgets.example.com",
		WithPollInterval(50*time.Millisecond),
		WithPollInterval(0),
		WithPollInterval(-time.Second),
		WaitOption(nil),
	)).To(Succeed())
	elapsed := time.Since(start)

	g.Expect(elapsed).To(BeNumerically(">=", 50*time.Millisecond))
	g.Expect(elapsed).To(BeNumerically("<", DefaultPollInterval))
}

// TestWaitForCRDToBeEstablishedPreservesNotFoundIdentity pins that a
// missing CRD surfaces as an apiextensions NotFound, not a generic
// poll timeout.
func TestWaitForCRDToBeEstablishedPreservesNotFoundIdentity(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	client, _ := newCRDScriptClient(t, map[string][][]apiextensionsv1.CustomResourceDefinitionCondition{})

	err := WaitForCRDToBeEstablished(t.Context(), client, "widgets.example.com")
	g.Expect(err).To(HaveOccurred())
	g.Expect(apierrors.IsNotFound(err)).To(BeTrue())
}

// TestWaitForCRDToBeEstablishedHonorsCancellation pins that a
// canceled context stops the wait with its own identity.
func TestWaitForCRDToBeEstablishedHonorsCancellation(t *testing.T) {
	t.Parallel()

	g := NewWithT(t)

	client, _ := newCRDScriptClient(t, map[string][][]apiextensionsv1.CustomResourceDefinitionCondition{
		"widgets.example.com": {
			trueConditions(apiextensionsv1.NamesAccepted),
		},
	})

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	g.Expect(WaitForCRDToBeEstablished(ctx, client, "widgets.example.com")).To(MatchError(context.Canceled))
}
