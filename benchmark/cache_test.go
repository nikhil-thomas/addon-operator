package benchmark

import (
	"context"
	"errors"
	"fmt"
	"runtime"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gmeasure"
	opsv1alpha1 "github.com/operator-framework/api/pkg/operators/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var _ = Describe("cache", Ordered, func() {
	var (
		ctx          context.Context
		cancel       func()
		namespace    string
		namespaceGen = nameGenerator("cache-benchmark")
	)

	BeforeEach(func() {
		ctx, cancel = context.WithCancel(context.Background())

		namespace = namespaceGen()
		ns := newNamespace(namespace)

		Expect(_client.Create(ctx, ns)).To(Succeed())
		Eventually(_client.Get(ctx, client.ObjectKeyFromObject(ns), ns)).Should(Succeed())

		DeferCleanup(func() {
			defer cancel()

			Expect(_client.Delete(ctx, ns)).To(Succeed())
		})
	})

	Context("with CSV transformation", func() {
		It("should use less memory", func() {
			newCache := prepareCache()

			cache, err := newCache(_cfg, cache.Options{
				Scheme: _scheme,
			})
			Expect(err).ToNot(HaveOccurred())

			go cache.Start(ctx)

			exp := gmeasure.NewExperiment("cache with CSV transformation")
			AddReportEntry(exp.Name, exp)

			exp.SampleValue("memory-usage", func(idx int) float64 {
				csv := newCSV(fmt.Sprintf("test-csv-%d", idx), namespace)
				Expect(_client.Create(context.Background(), csv)).To(Succeed())
				Eventually(_client.Get(ctx, client.ObjectKeyFromObject(csv), csv)).Should(Succeed())

				Expect(cache.List(ctx, &opsv1alpha1.ClusterServiceVersionList{})).To(Succeed())

				var m runtime.MemStats
				runtime.ReadMemStats(&m)

				return float64(m.Alloc) / 1024 / 1024
			}, gmeasure.SamplingConfig{
				N: 30000,
			})
		})
	})

	Context("without CSV transformation", func() {
		It("should use more memory", func() {
			cache, err := cache.New(_cfg, cache.Options{
				Scheme: _scheme,
			})
			Expect(err).ToNot(HaveOccurred())

			go cache.Start(ctx)

			exp := gmeasure.NewExperiment("cache without CSV transformation")
			AddReportEntry(exp.Name, exp)

			exp.SampleValue("memory-usage", func(idx int) float64 {
				csv := newCSV(fmt.Sprintf("test-csv-%d", idx), namespace)
				Expect(_client.Create(context.Background(), csv)).To(Succeed())
				Eventually(_client.Get(ctx, client.ObjectKeyFromObject(csv), csv)).Should(Succeed())

				Expect(cache.List(ctx, &opsv1alpha1.ClusterServiceVersionList{})).To(Succeed())

				var m runtime.MemStats
				runtime.ReadMemStats(&m)

				return float64(m.Alloc) / 1024
			}, gmeasure.SamplingConfig{
				N: 10000,
			})
		})
	})
})

func newCSV(name, namespace string) *opsv1alpha1.ClusterServiceVersion {
	return &opsv1alpha1.ClusterServiceVersion{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: opsv1alpha1.ClusterServiceVersionSpec{
			DisplayName: name,
			Description: "description",
			InstallStrategy: opsv1alpha1.NamedInstallStrategy{
				StrategyName: opsv1alpha1.InstallStrategyNameDeployment,
				StrategySpec: opsv1alpha1.StrategyDetailsDeployment{
					DeploymentSpecs: []opsv1alpha1.StrategyDeploymentSpec{
						{
							Name: "test-deployment",
							Spec: appsv1.DeploymentSpec{
								Selector: &metav1.LabelSelector{
									MatchLabels: map[string]string{
										"app": "test",
									},
								},
								Template: corev1.PodTemplateSpec{
									ObjectMeta: metav1.ObjectMeta{
										Name:      "test",
										Namespace: "test-namespace",
									},
									Spec: corev1.PodSpec{
										Containers: []corev1.Container{
											{
												Name:    "test",
												Image:   "test-image",
												Command: []string{"/test"},
											},
										},
									},
								},
							},
							Label: labels.Set{
								"app": "test",
							},
						},
					},
				},
			},
		},
		Status: opsv1alpha1.ClusterServiceVersionStatus{
			Phase: opsv1alpha1.CSVPhaseSucceeded,
		},
	}
}

func prepareCache() cache.NewCacheFunc {
	opts := cache.Options{
		TransformByObject: cache.TransformByObject{
			&opsv1alpha1.ClusterServiceVersion{}: stripUnusedCSVFields,
		},
	}

	return cache.BuilderWithOptions(opts)
}

var errInvalidObject = errors.New("invalid object")

func stripUnusedCSVFields(obj interface{}) (interface{}, error) {
	csv, ok := obj.(*opsv1alpha1.ClusterServiceVersion)
	if !ok {
		return nil, fmt.Errorf("casting %T as 'ClusterServiceVersion': %w", obj, errInvalidObject)
	}

	return &opsv1alpha1.ClusterServiceVersion{
		TypeMeta: csv.TypeMeta,
		ObjectMeta: metav1.ObjectMeta{
			Name:      csv.Name,
			Namespace: csv.Namespace,
		},
		Status: opsv1alpha1.ClusterServiceVersionStatus{
			Phase: csv.Status.Phase,
		},
	}, nil
}

func nameGenerator(pfx string) func() string {
	var idx int

	return func() string {
		defer func() { idx++ }()

		return fmt.Sprintf("%s-%d", pfx, idx)
	}
}

func newNamespace(name string) *corev1.Namespace {
	return &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
	}
}
