package benchmark

import (
	"testing"

	olmcrds "github.com/operator-framework/api/crds"
	opsv1alpha1 "github.com/operator-framework/api/pkg/operators/v1alpha1"
	v1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gexec"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

var (
	_cfg     *rest.Config
	_client  client.Client
	_testEnv *envtest.Environment
	_scheme  *runtime.Scheme
)

// To run these tests with an external cluster
// set the following environment variables:
// USE_EXISTING_CLUSTER=true
// KUBECONFIG=<path_to_kube.config>.
// The external cluster must have authentication
// enabled on the API server.
func TestSuite(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "controllers suite")
}

var _ = BeforeSuite(func() {
	By("Registering schemes")

	_scheme = runtime.NewScheme()
	Expect(clientgoscheme.AddToScheme(_scheme)).Should(Succeed())
	Expect(opsv1alpha1.AddToScheme(_scheme)).Should(Succeed())

	By("Starting test environment")

	_testEnv = &envtest.Environment{
		Scheme: _scheme,
	}

	var err error

	_cfg, err = _testEnv.Start()
	Expect(err).ToNot(HaveOccurred())
	Expect(_cfg).ToNot(BeNil())

	DeferCleanup(cleanup(_testEnv))

	By("Installing CRD's")

	_, err = envtest.InstallCRDs(_cfg, envtest.CRDInstallOptions{
		CRDs: []*v1.CustomResourceDefinition{
			olmcrds.ClusterServiceVersion(),
		},
		Scheme: _scheme,
	})
	Expect(err).ToNot(HaveOccurred())

	By("Initializing k8s client")

	_client, err = client.New(_cfg, client.Options{
		Scheme: _scheme,
	})

	Expect(err).ToNot(HaveOccurred())
})

func cleanup(env *envtest.Environment) func() {
	return func() {
		By("Stopping the test environment")

		Expect(env.Stop()).Should(Succeed())

		By("Cleaning up test artifacts")

		gexec.CleanupBuildArtifacts()
	}
}

func usingExistingCluster() bool {
	return _testEnv.UseExistingCluster != nil && *_testEnv.UseExistingCluster
}
