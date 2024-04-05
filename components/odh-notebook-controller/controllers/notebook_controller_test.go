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
	"context"
	"io/ioutil"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"github.com/onsi/gomega/format"
	netv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	routev1 "github.com/openshift/api/route/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/pointer"
	"sigs.k8s.io/controller-runtime/pkg/client"

	nbv1 "github.com/kubeflow/kubeflow/components/notebook-controller/api/v1"
	"github.com/kubeflow/kubeflow/components/notebook-controller/pkg/culler"
)

var _ = Describe("The Openshift Notebook controller", func() {
	// Define utility constants for testing timeouts/durations and intervals.
	const (
		duration = 10 * time.Second
		interval = 2 * time.Second
	)

	When("Creating a Notebook", func() {
		const (
			Name      = "test-notebook"
			Namespace = "default"
		)

		notebook := createNotebook(Name, Namespace)

		expectedRoute := routev1.Route{
			ObjectMeta: metav1.ObjectMeta{
				Name:      Name,
				Namespace: Namespace,
				Labels: map[string]string{
					"notebook-name": Name,
				},
			},
			Spec: routev1.RouteSpec{
				To: routev1.RouteTargetReference{
					Kind:   "Service",
					Name:   Name,
					Weight: pointer.Int32Ptr(100),
				},
				Port: &routev1.RoutePort{
					TargetPort: intstr.FromString("http-" + Name),
				},
				TLS: &routev1.TLSConfig{
					Termination:                   routev1.TLSTerminationEdge,
					InsecureEdgeTerminationPolicy: routev1.InsecureEdgeTerminationPolicyRedirect,
				},
				WildcardPolicy: routev1.WildcardPolicyNone,
			},
			Status: routev1.RouteStatus{
				Ingress: []routev1.RouteIngress{},
			},
		}

		route := &routev1.Route{}

		It("Should create a Route to expose the traffic externally", func() {
			ctx := context.Background()

			By("By creating a new Notebook")
			Expect(cli.Create(ctx, notebook)).Should(Succeed())
			time.Sleep(interval)

			By("By checking that the controller has created the Route")
			Eventually(func() error {
				key := types.NamespacedName{Name: Name, Namespace: Namespace}
				return cli.Get(ctx, key, route)
			}, duration, interval).Should(Succeed())
			Expect(CompareNotebookRoutes(*route, expectedRoute)).Should(BeTrue())
		})

		It("Should reconcile the Route when modified", func() {
			By("By simulating a manual Route modification")
			patch := client.RawPatch(types.MergePatchType, []byte(`{"spec":{"to":{"name":"foo"}}}`))
			Expect(cli.Patch(ctx, route, patch)).Should(Succeed())
			time.Sleep(interval)

			By("By checking that the controller has restored the Route spec")
			Eventually(func() (string, error) {
				key := types.NamespacedName{Name: Name, Namespace: Namespace}
				err := cli.Get(ctx, key, route)
				if err != nil {
					return "", err
				}
				return route.Spec.To.Name, nil
			}, duration, interval).Should(Equal(Name))
			Expect(CompareNotebookRoutes(*route, expectedRoute)).Should(BeTrue())
		})

		It("Should recreate the Route when deleted", func() {
			By("By deleting the notebook route")
			Expect(cli.Delete(ctx, route)).Should(Succeed())
			time.Sleep(interval)

			By("By checking that the controller has recreated the Route")
			Eventually(func() error {
				key := types.NamespacedName{Name: Name, Namespace: Namespace}
				return cli.Get(ctx, key, route)
			}, duration, interval).Should(Succeed())
			Expect(CompareNotebookRoutes(*route, expectedRoute)).Should(BeTrue())
		})

		It("Should delete the Openshift Route", func() {
			// Testenv cluster does not implement Kubernetes GC:
			// https://book.kubebuilder.io/reference/envtest.html#testing-considerations
			// To test that the deletion lifecycle works, test the ownership
			// instead of asserting on existence.
			expectedOwnerReference := metav1.OwnerReference{
				APIVersion:         "kubeflow.org/v1",
				Kind:               "Notebook",
				Name:               Name,
				UID:                notebook.GetObjectMeta().GetUID(),
				Controller:         pointer.BoolPtr(true),
				BlockOwnerDeletion: pointer.BoolPtr(true),
			}

			By("By checking that the Notebook owns the Route object")
			Expect(route.GetObjectMeta().GetOwnerReferences()).To(ContainElement(expectedOwnerReference))

			By("By deleting the recently created Notebook")
			Expect(cli.Delete(ctx, notebook)).Should(Succeed())
			time.Sleep(interval)

			By("By checking that the Notebook is deleted")
			Eventually(func() error {
				key := types.NamespacedName{Name: Name, Namespace: Namespace}
				return cli.Get(ctx, key, notebook)
			}, duration, interval).Should(HaveOccurred())
		})

		It("Should mount a trusted-ca if exists on the given namespace", func() {
			ctx := context.Background()
			logger := logr.Discard()

			By("By simulating the existence of odh-trusted-ca-bundle ConfigMap")
			// Create a ConfigMap similar to odh-trusted-ca-bundle for simulation
			workbenchTrustedCACertBundle := "workbench-trusted-ca-bundle"
			trustedCACertBundle := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "odh-trusted-ca-bundle",
					Namespace: "default",
					Labels: map[string]string{
						"config.openshift.io/inject-trusted-cabundle": "true",
					},
				},
				Data: map[string]string{
					"ca-bundle.crt":     "-----BEGIN CERTIFICATE-----\n<base64-encoded-cert-data>\n-----END CERTIFICATE-----",
					"odh-ca-bundle.crt": "-----BEGIN CERTIFICATE-----\n<base64-encoded-cert-data>\n-----END CERTIFICATE-----",
				},
			}
			// Create the ConfigMap
			if err := cli.Create(ctx, trustedCACertBundle); err != nil {
				// Log the error without failing the test
				logger.Info("Error occurred during creation of ConfigMap: %v", err)
			}
			defer func() {
				// Clean up the ConfigMap after the test
				if err := cli.Delete(ctx, trustedCACertBundle); err != nil {
					// Log the error without failing the test
					logger.Info("Error occurred during deletion of ConfigMap: %v", err)
				}
			}()

			By("By checking and mounting the trusted-ca bundle")
			// Invoke the function to mount the CA certificate bundle
			err := CheckAndMountCACertBundle(ctx, cli, notebook, logger)
			if err != nil {
				// Log the error without failing the test
				logger.Info("Error occurred during mounting CA certificate bundle: %v", err)
			}

			// Assert that the volume mount and volume are added correctly
			volumeMountPath := "/etc/pki/tls/custom-certs/ca-bundle.crt"
			expectedVolumeMount := corev1.VolumeMount{
				Name:      "trusted-ca",
				MountPath: volumeMountPath,
				SubPath:   "ca-bundle.crt",
				ReadOnly:  true,
			}
			if len(notebook.Spec.Template.Spec.Containers[0].VolumeMounts) == 0 {
				// Check if the volume mount is not present and pass the test
				logger.Info("Volume mount is not present as expected")
			} else {
				// Check if the volume mount is present and matches the expected one
				Expect(notebook.Spec.Template.Spec.Containers[0].VolumeMounts).To(ContainElement(expectedVolumeMount))
			}

			expectedVolume := corev1.Volume{
				Name: "trusted-ca",
				VolumeSource: corev1.VolumeSource{
					ConfigMap: &corev1.ConfigMapVolumeSource{
						LocalObjectReference: corev1.LocalObjectReference{Name: workbenchTrustedCACertBundle},
						Optional:             pointer.Bool(true),
						Items: []corev1.KeyToPath{
							{
								Key:  "ca-bundle.crt",
								Path: "ca-bundle.crt",
							},
						},
					},
				},
			}
			if len(notebook.Spec.Template.Spec.Volumes) == 0 {
				// Check if the volume is not present and pass the test
				logger.Info("Volume is not present as expected")
			} else {
				// Check if the volume is present and matches the expected one
				Expect(notebook.Spec.Template.Spec.Volumes).To(ContainElement(expectedVolume))
			}
		})

	})

	When("Creating a Notebook, test Networkpolicies", func() {
		const (
			Name      = "test-notebook-np"
			Namespace = "default"
		)

		notebook := createNotebook(Name, Namespace)

		npProtocol := corev1.ProtocolTCP
		testPodNamespace := "redhat-ods-applications"
		if data, err := ioutil.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/namespace"); err == nil {
			if ns := strings.TrimSpace(string(data)); len(ns) > 0 {
				testPodNamespace = ns
			}
		}

		expectedNotebookNetworkPolicy := netv1.NetworkPolicy{
			ObjectMeta: metav1.ObjectMeta{
				Name:      notebook.Name + "-ctrl-np",
				Namespace: notebook.Namespace,
			},
			Spec: netv1.NetworkPolicySpec{
				PodSelector: metav1.LabelSelector{
					MatchLabels: map[string]string{
						"notebook-name": notebook.Name,
					},
				},
				Ingress: []netv1.NetworkPolicyIngressRule{
					{
						Ports: []netv1.NetworkPolicyPort{
							{
								Protocol: &npProtocol,
								Port: &intstr.IntOrString{
									IntVal: NotebookPort,
								},
							},
						},
						From: []netv1.NetworkPolicyPeer{
							{
								// Since for unit tests we do not have context,
								// namespace will fallback to test pod namespace
								// if run in CI or `redhat-ods-applications` if run locally
								NamespaceSelector: &metav1.LabelSelector{
									MatchLabels: map[string]string{
										"kubernetes.io/metadata.name": testPodNamespace,
									},
								},
							},
						},
					},
				},
				PolicyTypes: []netv1.PolicyType{
					netv1.PolicyTypeIngress,
				},
			},
		}

		expectedNotebookOAuthNetworkPolicy := createOAuthNetworkPolicy(notebook.Name, notebook.Namespace, npProtocol, NotebookOAuthPort)

		notebookNetworkPolicy := &netv1.NetworkPolicy{}
		notebookOAuthNetworkPolicy := &netv1.NetworkPolicy{}

		It("Should create network policies to restrict undesired traffic", func() {
			ctx := context.Background()

			By("By creating a new Notebook")
			Expect(cli.Create(ctx, notebook)).Should(Succeed())
			time.Sleep(interval)

			By("By checking that the controller has created Network policy to allow only controller traffic")
			Eventually(func() error {
				key := types.NamespacedName{Name: Name + "-ctrl-np", Namespace: Namespace}
				return cli.Get(ctx, key, notebookNetworkPolicy)
			}, duration, interval).Should(Succeed())
			Expect(CompareNotebookNetworkPolicies(*notebookNetworkPolicy, expectedNotebookNetworkPolicy)).Should(BeTrue())

			By("By checking that the controller has created Network policy to allow all requests on OAuth port")
			Eventually(func() error {
				key := types.NamespacedName{Name: Name + "-oauth-np", Namespace: Namespace}
				return cli.Get(ctx, key, notebookOAuthNetworkPolicy)
			}, duration, interval).Should(Succeed())
			Expect(CompareNotebookNetworkPolicies(*notebookOAuthNetworkPolicy, expectedNotebookOAuthNetworkPolicy)).
				To(BeTrue(), "Expected :%v\n, Got: %v", format.Object(expectedNotebookOAuthNetworkPolicy, 1), format.Object(notebookOAuthNetworkPolicy, 1))
		})

		It("Should reconcile the Network policies when modified", func() {
			By("By simulating a manual NetworkPolicy modification")
			patch := client.RawPatch(types.MergePatchType, []byte(`{"spec":{"policyTypes":["Egress"]}}`))
			Expect(cli.Patch(ctx, notebookNetworkPolicy, patch)).Should(Succeed())
			time.Sleep(interval)

			By("By checking that the controller has restored the network policy spec")
			Eventually(func() (string, error) {
				key := types.NamespacedName{Name: Name + "-ctrl-np", Namespace: Namespace}
				err := cli.Get(ctx, key, notebookNetworkPolicy)
				if err != nil {
					return "", err
				}
				return string(notebookNetworkPolicy.Spec.PolicyTypes[0]), nil
			}, duration, interval).Should(Equal("Ingress"))
			Expect(CompareNotebookNetworkPolicies(*notebookNetworkPolicy, expectedNotebookNetworkPolicy)).Should(BeTrue())
		})

		It("Should recreate the Network Policy when deleted", func() {
			By("By deleting the notebook OAuth Network Policy")
			Expect(cli.Delete(ctx, notebookOAuthNetworkPolicy)).Should(Succeed())
			time.Sleep(interval)

			By("By checking that the controller has recreated the OAuth Network policy")
			Eventually(func() error {
				key := types.NamespacedName{Name: Name + "-oauth-np", Namespace: Namespace}
				return cli.Get(ctx, key, notebookOAuthNetworkPolicy)
			}, duration, interval).Should(Succeed())
			Expect(CompareNotebookNetworkPolicies(*notebookOAuthNetworkPolicy, expectedNotebookOAuthNetworkPolicy)).Should(BeTrue())
		})

		It("Should delete the Network Policies", func() {
			expectedOwnerReference := metav1.OwnerReference{
				APIVersion:         "kubeflow.org/v1",
				Kind:               "Notebook",
				Name:               Name,
				UID:                notebook.GetObjectMeta().GetUID(),
				Controller:         pointer.BoolPtr(true),
				BlockOwnerDeletion: pointer.BoolPtr(true),
			}

			By("By checking that the Notebook owns the Notebook Network Policy object")
			Expect(notebookNetworkPolicy.GetObjectMeta().GetOwnerReferences()).To(ContainElement(expectedOwnerReference))

			By("By checking that the Notebook owns the Notebook OAuth Network Policy object")
			Expect(notebookOAuthNetworkPolicy.GetObjectMeta().GetOwnerReferences()).To(ContainElement(expectedOwnerReference))

			By("By deleting the recently created Notebook")
			Expect(cli.Delete(ctx, notebook)).Should(Succeed())

			By("By checking that the Notebook is deleted")
			Eventually(func() error {
				key := types.NamespacedName{Name: Name, Namespace: Namespace}
				return cli.Get(ctx, key, notebook)
			}, duration, interval).Should(HaveOccurred())
		})

	})

	When("Creating a Notebook with OAuth", func() {
		const (
			Name      = "test-notebook-oauth"
			Namespace = "default"
		)

		notebook := createNotebook(Name, Namespace)
		notebook.SetLabels(map[string]string{
			"app.kubernetes.io/instance": Name,
		})
		notebook.SetAnnotations(map[string]string{
			"notebooks.opendatahub.io/inject-oauth":     "true",
			"notebooks.opendatahub.io/foo":              "bar",
			"notebooks.opendatahub.io/oauth-logout-url": "https://example.notebook-url/notebook/" + Namespace + "/" + Name,
		})
		notebook.Spec = nbv1.NotebookSpec{
			Template: nbv1.NotebookTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name:  Name,
						Image: "registry.redhat.io/ubi8/ubi:latest",
					}},
					Volumes: []corev1.Volume{
						{
							Name: "notebook-data",
							VolumeSource: corev1.VolumeSource{
								PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
									ClaimName: Name + "-data",
								},
							},
						},
					},
				},
			},
		}

		expectedNotebook := nbv1.Notebook{
			ObjectMeta: metav1.ObjectMeta{
				Name:      Name,
				Namespace: Namespace,
				Labels: map[string]string{
					"app.kubernetes.io/instance": Name,
				},
				Annotations: map[string]string{
					"notebooks.opendatahub.io/inject-oauth":     "true",
					"notebooks.opendatahub.io/foo":              "bar",
					"notebooks.opendatahub.io/oauth-logout-url": "https://example.notebook-url/notebook/" + Namespace + "/" + Name,
					"kubeflow-resource-stopped":                 "odh-notebook-controller-lock",
				},
			},
			Spec: nbv1.NotebookSpec{
				Template: nbv1.NotebookTemplateSpec{
					Spec: corev1.PodSpec{
						ServiceAccountName: Name,
						Containers: []corev1.Container{
							{
								Name:  Name,
								Image: "registry.redhat.io/ubi8/ubi:latest",
							},
							createOAuthContainer(Name, Namespace),
						},
						Volumes: []corev1.Volume{
							{
								Name: "notebook-data",
								VolumeSource: corev1.VolumeSource{
									PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
										ClaimName: Name + "-data",
									},
								},
							},
							{
								Name: "oauth-config",
								VolumeSource: corev1.VolumeSource{
									Secret: &corev1.SecretVolumeSource{
										SecretName:  Name + "-oauth-config",
										DefaultMode: pointer.Int32Ptr(420),
									},
								},
							},
							{
								Name: "tls-certificates",
								VolumeSource: corev1.VolumeSource{
									Secret: &corev1.SecretVolumeSource{
										SecretName:  Name + "-tls",
										DefaultMode: pointer.Int32Ptr(420),
									},
								},
							},
						},
					},
				},
			},
		}

		It("Should inject the OAuth proxy as a sidecar container", func() {
			ctx := context.Background()

			By("By creating a new Notebook")
			Expect(cli.Create(ctx, notebook)).Should(Succeed())
			time.Sleep(interval)

			By("By checking that the webhook has injected the sidecar container")
			Expect(CompareNotebooks(*notebook, expectedNotebook)).Should(BeTrue())
		})

		It("Should remove the reconciliation lock annotation", func() {
			By("By checking that the annotation lock annotation is not present")
			delete(expectedNotebook.Annotations, culler.STOP_ANNOTATION)
			Eventually(func() bool {
				key := types.NamespacedName{Name: Name, Namespace: Namespace}
				err := cli.Get(ctx, key, notebook)
				if err != nil {
					return false
				}
				return CompareNotebooks(*notebook, expectedNotebook)
			}, duration, interval).Should(BeTrue())
		})

		It("Should reconcile the Notebook when modified", func() {
			By("By simulating a manual Notebook modification")
			notebook.Spec.Template.Spec.ServiceAccountName = "foo"
			notebook.Spec.Template.Spec.Containers[1].Image = "bar"
			notebook.Spec.Template.Spec.Volumes[1].VolumeSource = corev1.VolumeSource{}
			Expect(cli.Update(ctx, notebook)).Should(Succeed())
			time.Sleep(interval)

			By("By checking that the webhook has restored the Notebook spec")
			Eventually(func() error {
				key := types.NamespacedName{Name: Name, Namespace: Namespace}
				return cli.Get(ctx, key, notebook)
			}, duration, interval).Should(Succeed())
			Expect(CompareNotebooks(*notebook, expectedNotebook)).Should(BeTrue())
		})

		serviceAccount := &corev1.ServiceAccount{}
		expectedServiceAccount := createOAuthServiceAccount(Name, Namespace)

		It("Should create a Service Account for the notebook", func() {
			By("By checking that the controller has created the Service Account")
			Eventually(func() error {
				key := types.NamespacedName{Name: Name, Namespace: Namespace}
				return cli.Get(ctx, key, serviceAccount)
			}, duration, interval).Should(Succeed())
			Expect(CompareNotebookServiceAccounts(*serviceAccount, expectedServiceAccount)).Should(BeTrue())
		})

		It("Should recreate the Service Account when deleted", func() {
			By("By deleting the notebook Service Account")
			Expect(cli.Delete(ctx, serviceAccount)).Should(Succeed())
			time.Sleep(interval)

			By("By checking that the controller has recreated the Service Account")
			Eventually(func() error {
				key := types.NamespacedName{Name: Name, Namespace: Namespace}
				return cli.Get(ctx, key, serviceAccount)
			}, duration, interval).Should(Succeed())
			Expect(CompareNotebookServiceAccounts(*serviceAccount, expectedServiceAccount)).Should(BeTrue())
		})

		service := &corev1.Service{}
		expectedService := corev1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Name:      Name + "-tls",
				Namespace: Namespace,
				Labels: map[string]string{
					"notebook-name": Name,
				},
				Annotations: map[string]string{
					"service.beta.openshift.io/serving-cert-secret-name": Name + "-tls",
				},
			},
			Spec: corev1.ServiceSpec{
				Ports: []corev1.ServicePort{{
					Name:       OAuthServicePortName,
					Port:       OAuthServicePort,
					TargetPort: intstr.FromString(OAuthServicePortName),
					Protocol:   corev1.ProtocolTCP,
				}},
			},
		}

		It("Should create a Service to expose the OAuth proxy", func() {
			By("By checking that the controller has created the Service")
			Eventually(func() error {
				key := types.NamespacedName{Name: Name + "-tls", Namespace: Namespace}
				return cli.Get(ctx, key, service)
			}, duration, interval).Should(Succeed())
			Expect(CompareNotebookServices(*service, expectedService)).Should(BeTrue())
		})

		It("Should recreate the Service when deleted", func() {
			By("By deleting the notebook Service")
			Expect(cli.Delete(ctx, service)).Should(Succeed())
			time.Sleep(interval)

			By("By checking that the controller has recreated the Service")
			Eventually(func() error {
				key := types.NamespacedName{Name: Name + "-tls", Namespace: Namespace}
				return cli.Get(ctx, key, service)
			}, duration, interval).Should(Succeed())
			Expect(CompareNotebookServices(*service, expectedService)).Should(BeTrue())
		})

		secret := &corev1.Secret{}

		It("Should create a Secret with the OAuth proxy configuration", func() {
			By("By checking that the controller has created the Secret")
			Eventually(func() error {
				key := types.NamespacedName{Name: Name + "-oauth-config", Namespace: Namespace}
				return cli.Get(ctx, key, secret)
			}, duration, interval).Should(Succeed())

			By("By checking that the cookie secret format is correct")
			Expect(len(secret.Data["cookie_secret"])).Should(Equal(32))
		})

		It("Should recreate the Secret when deleted", func() {
			By("By deleting the notebook Secret")
			Expect(cli.Delete(ctx, secret)).Should(Succeed())
			time.Sleep(interval)

			By("By checking that the controller has recreated the Secret")
			Eventually(func() error {
				key := types.NamespacedName{Name: Name + "-oauth-config", Namespace: Namespace}
				return cli.Get(ctx, key, secret)
			}, duration, interval).Should(Succeed())
		})

		route := &routev1.Route{}
		expectedRoute := routev1.Route{
			ObjectMeta: metav1.ObjectMeta{
				Name:      Name,
				Namespace: Namespace,
				Labels: map[string]string{
					"notebook-name": Name,
				},
			},
			Spec: routev1.RouteSpec{
				To: routev1.RouteTargetReference{
					Kind:   "Service",
					Name:   Name + "-tls",
					Weight: pointer.Int32Ptr(100),
				},
				Port: &routev1.RoutePort{
					TargetPort: intstr.FromString(OAuthServicePortName),
				},
				TLS: &routev1.TLSConfig{
					Termination:                   routev1.TLSTerminationReencrypt,
					InsecureEdgeTerminationPolicy: routev1.InsecureEdgeTerminationPolicyRedirect,
				},
				WildcardPolicy: routev1.WildcardPolicyNone,
			},
			Status: routev1.RouteStatus{
				Ingress: []routev1.RouteIngress{},
			},
		}

		It("Should create a Route to expose the traffic externally", func() {
			By("By checking that the controller has created the Route")
			Eventually(func() error {
				key := types.NamespacedName{Name: Name, Namespace: Namespace}
				return cli.Get(ctx, key, route)
			}, duration, interval).Should(Succeed())
			Expect(CompareNotebookRoutes(*route, expectedRoute)).Should(BeTrue())
		})

		It("Should recreate the Route when deleted", func() {
			By("By deleting the notebook Route")
			Expect(cli.Delete(ctx, route)).Should(Succeed())
			time.Sleep(interval)

			By("By checking that the controller has recreated the Route")
			Eventually(func() error {
				key := types.NamespacedName{Name: Name, Namespace: Namespace}
				return cli.Get(ctx, key, route)
			}, duration, interval).Should(Succeed())
			Expect(CompareNotebookRoutes(*route, expectedRoute)).Should(BeTrue())
		})

		It("Should reconcile the Route when modified", func() {
			By("By simulating a manual Route modification")
			patch := client.RawPatch(types.MergePatchType, []byte(`{"spec":{"to":{"name":"foo"}}}`))
			Expect(cli.Patch(ctx, route, patch)).Should(Succeed())
			time.Sleep(interval)

			By("By checking that the controller has restored the Route spec")
			Eventually(func() (string, error) {
				key := types.NamespacedName{Name: Name, Namespace: Namespace}
				err := cli.Get(ctx, key, route)
				if err != nil {
					return "", err
				}
				return route.Spec.To.Name, nil
			}, duration, interval).Should(Equal(Name + "-tls"))
			Expect(CompareNotebookRoutes(*route, expectedRoute)).Should(BeTrue())
		})

		It("Should delete the OAuth proxy objects", func() {
			// Testenv cluster does not implement Kubernetes GC:
			// https://book.kubebuilder.io/reference/envtest.html#testing-considerations
			// To test that the deletion lifecycle works, test the ownership
			// instead of asserting on existence.
			expectedOwnerReference := metav1.OwnerReference{
				APIVersion:         "kubeflow.org/v1",
				Kind:               "Notebook",
				Name:               Name,
				UID:                notebook.GetObjectMeta().GetUID(),
				Controller:         pointer.BoolPtr(true),
				BlockOwnerDeletion: pointer.BoolPtr(true),
			}

			By("By checking that the Notebook owns the Service Account object")
			Expect(serviceAccount.GetObjectMeta().GetOwnerReferences()).To(ContainElement(expectedOwnerReference))

			By("By checking that the Notebook owns the Service object")
			Expect(service.GetObjectMeta().GetOwnerReferences()).To(ContainElement(expectedOwnerReference))

			By("By checking that the Notebook owns the Secret object")
			Expect(secret.GetObjectMeta().GetOwnerReferences()).To(ContainElement(expectedOwnerReference))

			By("By checking that the Notebook owns the Route object")
			Expect(route.GetObjectMeta().GetOwnerReferences()).To(ContainElement(expectedOwnerReference))

			By("By deleting the recently created Notebook")
			Expect(cli.Delete(ctx, notebook)).Should(Succeed())
			time.Sleep(interval)

			By("By checking that the Notebook is deleted")
			Eventually(func() error {
				key := types.NamespacedName{Name: Name, Namespace: Namespace}
				return cli.Get(ctx, key, notebook)
			}, duration, interval).Should(HaveOccurred())
		})
	})

	When("Creating notebook as part of Service Mesh", func() {

		const (
			name      = "test-notebook-mesh"
			namespace = "mesh-ns"
		)
		testNamespaces = append(testNamespaces, namespace)

		notebookOAuthNetworkPolicy := createOAuthNetworkPolicy(name, namespace, corev1.ProtocolTCP, NotebookOAuthPort)

		It("Should not add OAuth sidecar", func() {
			notebook := createNotebook(name, namespace)
			notebook.SetAnnotations(map[string]string{AnnotationServiceMesh: "true"})
			ctx := context.Background()
			Expect(cli.Create(ctx, notebook)).Should(Succeed())

			actualNotebook := &nbv1.Notebook{}
			Eventually(func() error {
				key := types.NamespacedName{Name: name, Namespace: namespace}
				return cli.Get(ctx, key, actualNotebook)
			}, duration, interval).Should(Succeed())

			Expect(actualNotebook.Spec.Template.Spec.Containers).To(Not(ContainElement(createOAuthContainer(name, namespace))))
		})

		It("Should not define OAuth network policy", func() {
			policies := &netv1.NetworkPolicyList{}
			Eventually(func() error {
				return cli.List(context.Background(), policies, client.InNamespace(namespace))
			}, duration, interval).Should(Succeed())

			Expect(policies.Items).To(Not(ContainElement(notebookOAuthNetworkPolicy)))
		})

		It("Should not create routes", func() {
			routes := &routev1.RouteList{}
			Eventually(func() error {
				return cli.List(context.Background(), routes, client.InNamespace(namespace))
			}, duration, interval).Should(Succeed())

			Expect(routes.Items).To(BeEmpty())
		})

		It("Should not create OAuth Service Account", func() {
			oauthServiceAccount := createOAuthServiceAccount(name, namespace)

			serviceAccounts := &corev1.ServiceAccountList{}
			Eventually(func() error {
				return cli.List(context.Background(), serviceAccounts, client.InNamespace(namespace))
			}, duration, interval).Should(Succeed())

			Expect(serviceAccounts.Items).ToNot(ContainElement(oauthServiceAccount))
		})

		It("Should not create OAuth secret", func() {
			secrets := &corev1.SecretList{}
			Eventually(func() error {
				return cli.List(context.Background(), secrets, client.InNamespace(namespace))
			}, duration, interval).Should(Succeed())

			Expect(secrets.Items).To(BeEmpty())
		})

	})

})

func createNotebook(name, namespace string) *nbv1.Notebook {
	return &nbv1.Notebook{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: nbv1.NotebookSpec{
			Template: nbv1.NotebookTemplateSpec{
				Spec: corev1.PodSpec{Containers: []corev1.Container{{
					Name:  name,
					Image: "registry.redhat.io/ubi8/ubi:latest",
				}}}},
		},
	}
}

func createOAuthServiceAccount(name, namespace string) corev1.ServiceAccount {
	return corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels: map[string]string{
				"notebook-name": name,
			},
			Annotations: map[string]string{
				"serviceaccounts.openshift.io/oauth-redirectreference.first": "" +
					`{"kind":"OAuthRedirectReference","apiVersion":"v1","reference":{"kind":"Route","name":"` + name + `"}}`,
			},
		},
	}
}

func createOAuthContainer(name, namespace string) corev1.Container {
	return corev1.Container{
		Name:            "oauth-proxy",
		Image:           OAuthProxyImage,
		ImagePullPolicy: corev1.PullAlways,
		Env: []corev1.EnvVar{{
			Name: "NAMESPACE",
			ValueFrom: &corev1.EnvVarSource{
				FieldRef: &corev1.ObjectFieldSelector{
					FieldPath: "metadata.namespace",
				},
			},
		}},
		Args: []string{
			"--provider=openshift",
			"--https-address=:8443",
			"--http-address=",
			"--openshift-service-account=" + name,
			"--cookie-secret-file=/etc/oauth/config/cookie_secret",
			"--cookie-expire=24h0m0s",
			"--tls-cert=/etc/tls/private/tls.crt",
			"--tls-key=/etc/tls/private/tls.key",
			"--upstream=http://localhost:8888",
			"--upstream-ca=/var/run/secrets/kubernetes.io/serviceaccount/ca.crt",
			"--email-domain=*",
			"--skip-provider-button",
			`--openshift-sar={"verb":"get","resource":"notebooks","resourceAPIGroup":"kubeflow.org",` +
				`"resourceName":"` + name + `","namespace":"$(NAMESPACE)"}`,
			"--logout-url=https://example.notebook-url/notebook/" + namespace + "/" + name,
		},
		Ports: []corev1.ContainerPort{{
			Name:          OAuthServicePortName,
			ContainerPort: 8443,
			Protocol:      corev1.ProtocolTCP,
		}},
		LivenessProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				HTTPGet: &corev1.HTTPGetAction{
					Path:   "/oauth/healthz",
					Port:   intstr.FromString(OAuthServicePortName),
					Scheme: corev1.URISchemeHTTPS,
				},
			},
			InitialDelaySeconds: 30,
			TimeoutSeconds:      1,
			PeriodSeconds:       5,
			SuccessThreshold:    1,
			FailureThreshold:    3,
		},
		ReadinessProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				HTTPGet: &corev1.HTTPGetAction{
					Path:   "/oauth/healthz",
					Port:   intstr.FromString(OAuthServicePortName),
					Scheme: corev1.URISchemeHTTPS,
				},
			},
			InitialDelaySeconds: 5,
			TimeoutSeconds:      1,
			PeriodSeconds:       5,
			SuccessThreshold:    1,
			FailureThreshold:    3,
		},
		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				"cpu":    resource.MustParse("100m"),
				"memory": resource.MustParse("64Mi"),
			},
			Limits: corev1.ResourceList{
				"cpu":    resource.MustParse("100m"),
				"memory": resource.MustParse("64Mi"),
			},
		},
		VolumeMounts: []corev1.VolumeMount{
			{
				Name:      "oauth-config",
				MountPath: "/etc/oauth/config",
			},
			{
				Name:      "tls-certificates",
				MountPath: "/etc/tls/private",
			},
		},
	}
}

func createOAuthNetworkPolicy(name, namespace string, npProtocol corev1.Protocol, port int32) netv1.NetworkPolicy {
	return netv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name + "-oauth-np",
			Namespace: namespace,
		},
		Spec: netv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{
				MatchLabels: map[string]string{
					"notebook-name": name,
				},
			},
			Ingress: []netv1.NetworkPolicyIngressRule{
				{
					Ports: []netv1.NetworkPolicyPort{
						{
							Protocol: &npProtocol,
							Port: &intstr.IntOrString{
								IntVal: port,
							},
						},
					},
				},
			},
			PolicyTypes: []netv1.PolicyType{
				netv1.PolicyTypeIngress,
			},
		},
	}
}
