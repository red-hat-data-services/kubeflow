/*
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

package controllers

import (
	"time"

	nbv1 "github.com/kubeflow/kubeflow/components/notebook-controller/api/v1"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrs "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var _ = Describe("The Openshift Notebook validating webhook", func() {
	const Namespace = "default"

	updateNotebookWithRetry := func(name string, updateFn func(*nbv1.Notebook)) error {
		var lastErr error
		for i := 0; i < 10; i++ {
			notebook := &nbv1.Notebook{}
			if err := cli.Get(ctx, types.NamespacedName{Name: name, Namespace: Namespace}, notebook); err != nil {
				return err
			}
			updateFn(notebook)
			lastErr = cli.Update(ctx, notebook)
			if lastErr == nil {
				return nil
			}
			if !apierrs.IsConflict(lastErr) {
				return lastErr
			}
			time.Sleep(100 * time.Millisecond)
		}
		return lastErr
	}

	When("Creating or updating a Notebook with dangerous pod spec fields", func() {
		BeforeEach(func() {
			err := cli.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: Namespace}}, &client.CreateOptions{})
			if err != nil && !apierrs.IsAlreadyExists(err) {
				Expect(err).ToNot(HaveOccurred())
			}
		})

		newNotebook := func(name string, mutate func(*corev1.PodSpec)) *nbv1.Notebook {
			podSpec := corev1.PodSpec{
				Containers: []corev1.Container{{
					Name:  name,
					Image: "test-image:latest",
				}},
			}
			if mutate != nil {
				mutate(&podSpec)
			}
			return &nbv1.Notebook{
				ObjectMeta: metav1.ObjectMeta{
					Name:      name,
					Namespace: Namespace,
				},
				Spec: nbv1.NotebookSpec{
					Template: nbv1.NotebookTemplateSpec{
						Spec: podSpec,
					},
				},
			}
		}

		It("Should deny create requests with hostNetwork", func() {
			err := cli.Create(ctx, newNotebook("test-dangerous-create-hostnetwork", func(podSpec *corev1.PodSpec) {
				podSpec.HostNetwork = true
			}))
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("hostNetwork"))
		})

		It("Should deny create requests with hostPath volumes", func() {
			err := cli.Create(ctx, newNotebook("test-dangerous-create-hostpath", func(podSpec *corev1.PodSpec) {
				podSpec.Volumes = []corev1.Volume{{
					Name: "host",
					VolumeSource: corev1.VolumeSource{
						HostPath: &corev1.HostPathVolumeSource{Path: "/etc"},
					},
				}}
			}))
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("hostPath volume"))
		})

		It("Should deny create requests with privileged containers", func() {
			err := cli.Create(ctx, newNotebook("test-dangerous-create-privileged", func(podSpec *corev1.PodSpec) {
				podSpec.Containers[0].SecurityContext = &corev1.SecurityContext{Privileged: ptr.To(true)}
			}))
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("privileged container"))
		})

		It("Should deny create requests with arbitrary service accounts", func() {
			err := cli.Create(ctx, newNotebook("test-dangerous-create-serviceaccount", func(podSpec *corev1.PodSpec) {
				podSpec.ServiceAccountName = "admin"
			}))
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("serviceAccountName"))
		})

		It("Should deny update requests that introduce dangerous pod spec fields", func() {
			name := "test-dangerous-update"
			notebook := newNotebook(name, nil)
			Expect(cli.Create(ctx, notebook)).To(Succeed())

			err := updateNotebookWithRetry(name, func(nb *nbv1.Notebook) {
				nb.Spec.Template.Spec.HostNetwork = true
			})
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("hostNetwork"))

			Eventually(func() error {
				nb := &nbv1.Notebook{}
				if err := cli.Get(ctx, types.NamespacedName{Name: name, Namespace: Namespace}, nb); err != nil {
					if apierrs.IsNotFound(err) {
						return nil
					}
					return err
				}
				return cli.Delete(ctx, nb)
			}, 10*time.Second).Should(Succeed())
		})
	})
})
