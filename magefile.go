//go:build mage
// +build mage

package main

import (
	"context"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	gourl "net/url"
	"os"
	"os/exec"
	"path"
	goruntime "runtime"
	"strings"
	"time"

	"archive/tar"
	"compress/gzip"
	"github.com/blang/semver/v4"
	"github.com/go-logr/logr"
	"github.com/go-logr/stdr"
	"github.com/magefile/mage/mg"
	"github.com/magefile/mage/sh"
	"github.com/mt-sre/devkube/dev"
	"github.com/mt-sre/devkube/magedeps"
	olmversion "github.com/operator-framework/api/pkg/lib/version"
	operatorsv1alpha1 "github.com/operator-framework/api/pkg/operators/v1alpha1"

	appsv1 "k8s.io/api/apps/v1"
	"sigs.k8s.io/yaml"
)

const (
	module          = "github.com/openshift/addon-operator"
	defaultImageOrg = "quay.io/app-sre"
)

// Directories
var (
	// Working directory of the project.
	workDir string
	// Dependency directory.
	depsDir  magedeps.DependencyDirectory
	cacheDir string

	logger           logr.Logger
	containerRuntime string
)

func init() {
	var err error
	// Directories
	workDir, err = os.Getwd()
	if err != nil {
		panic(fmt.Errorf("getting work dir: %w", err))
	}
	cacheDir = path.Join(workDir + "/" + ".cache")
	depsDir = magedeps.DependencyDirectory(path.Join(workDir, ".deps"))
	os.Setenv("PATH", depsDir.Bin()+":"+os.Getenv("PATH"))

	logger = stdr.New(nil)
}

// dependency for all targets requiring a container runtime
func setupContainerRuntime() {
	containerRuntime = os.Getenv("CONTAINER_RUNTIME")
	if len(containerRuntime) == 0 || containerRuntime == "auto" {
		cr, err := dev.DetectContainerRuntime()
		if err != nil {
			panic(err)
		}
		containerRuntime = string(cr)
		logger.Info("detected container-runtime", "container-runtime", containerRuntime)
	}
}

// Prepare a new release of the Addon Operator.
func Prepare_Release() error {
	versionBytes, err := ioutil.ReadFile(path.Join(workDir, "VERSION"))
	if err != nil {
		return fmt.Errorf("reading VERSION file: %w", err)
	}

	version = strings.TrimSpace(strings.TrimLeft(string(versionBytes), "v"))
	semverVersion, err := semver.New(version)
	if err != nil {
		return fmt.Errorf("parse semver: %w", err)
	}

	// read CSV
	csvTemplate, err := ioutil.ReadFile(path.Join(workDir, "config/olm/addon-operator.csv.tpl.yaml"))
	if err != nil {
		return fmt.Errorf("reading CSV template: %w", err)
	}

	var csv operatorsv1alpha1.ClusterServiceVersion
	if err := yaml.Unmarshal(csvTemplate, &csv); err != nil {
		return err
	}

	// Update for new release
	csv.Annotations["olm.skipRange"] = ">=0.0.1 <" + version
	csv.Name = "addon-operator.v" + version
	csv.Spec.Version = olmversion.OperatorVersion{Version: *semverVersion}

	// write updated template
	csvBytes, err := yaml.Marshal(csv)
	if err != nil {
		return err
	}
	if err := ioutil.WriteFile("config/olm/addon-operator.csv.tpl.yaml",
		csvBytes, os.ModePerm); err != nil {
		return err
	}

	// run generators to re-template config/openshift/manifests/*
	if err := sh.RunV("make", "openshift-ci-test-build"); err != nil {
		return fmt.Errorf("rebuilding config/openshift/: %w", err)
	}
	return nil
}

//New_Prepare_Release prepares the latest release of the Addon Operator
func New_Prepare_Release() error {
	versionBytes, err := ioutil.ReadFile(path.Join(workDir, "VERSION"))
	if err != nil {
		return fmt.Errorf("reading VERSION file: %w", err)
	}

	version = strings.TrimSpace(strings.TrimLeft(string(versionBytes), "v"))

	// generate operator bundle
	if err := sh.RunV("make", "openshift-ci-test-build_new"); err != nil {
		return fmt.Errorf("rebuilding config/openshift/: %w", err)
	}
	return setSkipRange(version)
}

func setSkipRange(version string) error {
	// read CSV
	csvFilePath := "config/openshift_new/release-artifacts/bundle/manifests/addon-operator.clusterserviceversion.yaml"
	csvFile, err := ioutil.ReadFile(path.Join(workDir, csvFilePath))
	if err != nil {
		return fmt.Errorf("reading CSV file: %w", err)
	}

	var csv operatorsv1alpha1.ClusterServiceVersion
	if err := yaml.Unmarshal(csvFile, &csv); err != nil {
		return err
	}

	// Update for new release
	csv.Annotations["olm.skipRange"] = ">=0.0.1 <" + version
	// csv metadata.name and spec.version are set by 'operator-sdk generate bundle' command

	csvBytes, err := yaml.Marshal(csv)
	if err != nil {
		return err
	}
	if err := ioutil.WriteFile(csvFilePath,
		csvBytes, os.ModePerm); err != nil {
		return err
	}
	return nil
}

// Building
// --------
type Build mg.Namespace

// Build Tags
var (
	branch        string
	shortCommitID string
	version       string
	buildDate     string

	ldFlags string

	imageOrg string
)

// init build variables
func (Build) init() error {
	// Build flags
	branchCmd := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
	branchBytes, err := branchCmd.Output()
	if err != nil {
		panic(fmt.Errorf("getting git branch: %w", err))
	}
	branch = strings.TrimSpace(string(branchBytes))

	shortCommitIDCmd := exec.Command("git", "rev-parse", "--short", "HEAD")
	shortCommitIDBytes, err := shortCommitIDCmd.Output()
	if err != nil {
		panic(fmt.Errorf("getting git short commit id"))
	}
	shortCommitID = strings.TrimSpace(string(shortCommitIDBytes))

	version = strings.TrimSpace(os.Getenv("VERSION"))
	if len(version) == 0 {
		version = shortCommitID
	}

	buildDate = fmt.Sprint(time.Now().UTC().Unix())
	ldFlags = fmt.Sprintf(`-X %s/internal/version.Version=%s`+
		`-X %s/internal/version.Branch=%s`+
		`-X %s/internal/version.Commit=%s`+
		`-X %s/internal/version.BuildDate=%s`,
		module, version,
		module, branch,
		module, shortCommitID,
		module, buildDate,
	)

	imageOrg = os.Getenv("IMAGE_ORG")
	if len(imageOrg) == 0 {
		imageOrg = defaultImageOrg
	}

	return nil
}

// Builds binaries from /cmd directory.
func (Build) cmd(cmd, goos, goarch string) error {
	mg.Deps(Build.init)

	env := map[string]string{
		"GOFLAGS":     "",
		"CGO_ENABLED": "0",
		"LDFLAGS":     ldFlags,
	}

	bin := path.Join("bin", cmd)
	if len(goos) != 0 && len(goarch) != 0 {
		// change bin path to point to a sudirectory when cross compiling
		bin = path.Join("bin", goos+"_"+goarch, cmd)
		env["GOOS"] = goos
		env["GOARCH"] = goarch
	}

	if err := sh.RunWithV(
		env,
		"go", "build", "-v", "-o", bin, "./cmd/"+cmd+"/main.go",
	); err != nil {
		return fmt.Errorf("compiling cmd/%s: %w", cmd, err)
	}
	return nil
}

// Default build target for CI/CD
func (Build) All() {
	mg.Deps(
		mg.F(Build.cmd, "addon-operator-manager", "linux", "amd64"),
		mg.F(Build.cmd, "addon-operator-webhook", "linux", "amd64"),
		mg.F(Build.cmd, "api-mock", "linux", "amd64"),
		mg.F(Build.cmd, "mage", "", ""),
	)
}

func (Build) BuildImages() {
	mg.Deps(
		mg.F(Build.ImageBuild, "addon-operator-manager"),
		mg.F(Build.ImageBuild, "addon-operator-webhook"),
		mg.F(Build.ImageBuild, "api-mock"),
		mg.F(Build.ImageBuild, "addon-operator-index"), // also pushes bundle
	)
}

func (Build) PushImages() {
	mg.Deps(
		mg.F(Build.imagePush, "addon-operator-manager"),
		mg.F(Build.imagePush, "addon-operator-webhook"),
		mg.F(Build.imagePush, "addon-operator-index"), // also pushes bundle
	)
}

// Builds the docgen internal tool
func (Build) Docgen() {
	mg.Deps(mg.F(Build.cmd, "docgen", "", ""))
}

func (b Build) ImageBuild(cmd string) error {
	mg.SerialDeps(setupContainerRuntime)

	// clean/prepare cache directory
	imageCacheDir := path.Join(cacheDir, "image", cmd)
	if err := os.RemoveAll(imageCacheDir); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("deleting image cache: %w", err)
	}
	if err := os.Remove(imageCacheDir + ".tar"); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("deleting image cache: %w", err)
	}
	if err := os.MkdirAll(imageCacheDir, os.ModePerm); err != nil {
		return fmt.Errorf("create image cache dir: %w", err)
	}

	switch cmd {
	case "addon-operator-index":
		return b.buildOLMIndexImage(imageCacheDir)

	case "addon-operator-bundle":
		return b.buildOLMBundleImage(imageCacheDir)

	default:
		mg.Deps(
			mg.F(Build.cmd, cmd, "linux", "amd64"),
		)
		return b.buildGenericImage(cmd, imageCacheDir)
	}
}

// generic image build function, when the image just relies on
// a static binary build from cmd/*
func (Build) buildGenericImage(cmd, imageCacheDir string) error {
	imageTag := imageURL(cmd)
	for _, command := range [][]string{
		// Copy files for build environment
		{"cp", "-a",
			"bin/linux_amd64/" + cmd,
			imageCacheDir + "/" + cmd},
		{"cp", "-a",
			"config/docker/" + cmd + ".Dockerfile",
			imageCacheDir + "/Dockerfile"},

		// Build image!
		{containerRuntime, "build", "-t", imageTag, imageCacheDir},
		{containerRuntime, "image", "save",
			"-o", imageCacheDir + ".tar", imageTag},
	} {
		if err := sh.Run(command[0], command[1:]...); err != nil {
			return fmt.Errorf("running %q: %w", strings.Join(command, " "), err)
		}
	}
	return nil
}

func (b Build) buildOLMIndexImage(imageCacheDir string) error {
	mg.Deps(
		Dependency.Opm,
		mg.F(Build.imagePush, "addon-operator-bundle"),
	)

	if err := sh.RunV("opm", "index", "add",
		"--container-tool", containerRuntime,
		"--bundles", imageURL("addon-operator-bundle"),
		"--tag", imageURL("addon-operator-index")); err != nil {
		return fmt.Errorf("runnign opm: %w", err)
	}
	return nil
}

func (b Build) buildOLMBundleImage(imageCacheDir string) error {
	mg.Deps(
		Build.init,
		Build.TemplateAddonOperatorCSV,
	)

	imageTag := imageURL("addon-operator-bundle")
	manifestsDir := path.Join(imageCacheDir, "manifests")
	metadataDir := path.Join(imageCacheDir, "metadata")
	for _, command := range [][]string{
		{"mkdir", "-p", manifestsDir},
		{"mkdir", "-p", metadataDir},

		// Copy files for build environment
		{"cp", "-a",
			"config/docker/addon-operator-bundle.Dockerfile",
			imageCacheDir + "/Dockerfile"},

		{"cp", "-a", "config/olm/addon-operator.csv.yaml", manifestsDir},
		{"cp", "-a", "config/olm/metrics.service.yaml", manifestsDir},
		{"cp", "-a", "config/olm/addon-operator-servicemonitor.yaml", manifestsDir},
		{"cp", "-a", "config/olm/prometheus-role.yaml", manifestsDir},
		{"cp", "-a", "config/olm/prometheus-rb.yaml", manifestsDir},
		{"cp", "-a", "config/olm/annotations.yaml", metadataDir},

		// copy CRDs
		// The first few lines of the CRD file need to be removed:
		// https://github.com/operator-framework/operator-registry/issues/222
		{"bash", "-c", "tail -n+3 " +
			"config/deploy/addons.managed.openshift.io_addons.yaml " +
			"> " + path.Join(manifestsDir, "addons.yaml")},
		{"bash", "-c", "tail -n+3 " +
			"config/deploy/addons.managed.openshift.io_addonoperators.yaml " +
			"> " + path.Join(manifestsDir, "addonoperators.yaml")},
		{"bash", "-c", "tail -n+3 " +
			"config/deploy/addons.managed.openshift.io_addoninstances.yaml " +
			"> " + path.Join(manifestsDir, "addoninstances.yaml")},

		// Build image!
		{containerRuntime, "build", "-t", imageTag, imageCacheDir},
		{containerRuntime, "image", "save",
			"-o", imageCacheDir + ".tar", imageTag},
	} {
		if err := sh.RunV(command[0], command[1:]...); err != nil {
			return err
		}
	}
	return nil
}

func (b Build) TemplateAddonOperatorCSV() error {
	// convert unstructured.Unstructured to CSV
	csvTemplate, err := ioutil.ReadFile(path.Join(workDir, "config/olm/addon-operator.csv.tpl.yaml"))
	if err != nil {
		return fmt.Errorf("reading CSV template: %w", err)
	}

	var csv operatorsv1alpha1.ClusterServiceVersion
	if err := yaml.Unmarshal(csvTemplate, &csv); err != nil {
		return err
	}

	// replace images
	for i := range csv.Spec.
		InstallStrategy.StrategySpec.DeploymentSpecs {
		deploy := &csv.Spec.
			InstallStrategy.StrategySpec.DeploymentSpecs[i]

		switch deploy.Name {
		case "addon-operator-manager":
			for i := range deploy.Spec.
				Template.Spec.Containers {
				container := &deploy.Spec.Template.Spec.Containers[i]
				switch container.Name {
				case "manager":
					container.Image = imageURL("addon-operator-manager")
				}
			}

		case "addon-operator-webhook":
			for i := range deploy.Spec.
				Template.Spec.Containers {
				container := &deploy.Spec.Template.Spec.Containers[i]
				switch container.Name {
				case "webhook":
					container.Image = imageURL("addon-operator-webhook")
				}
			}
		}
	}
	csv.Annotations["containerImage"] = imageURL("addon-operator-manager")

	// write
	csvBytes, err := yaml.Marshal(csv)
	if err != nil {
		return err
	}
	if err := ioutil.WriteFile("config/olm/addon-operator.csv.yaml",
		csvBytes, os.ModePerm); err != nil {
		return err
	}

	return nil
}

func (Build) imagePush(imageName string) error {
	mg.SerialDeps(
		mg.F(Build.ImageBuild, imageName),
	)

	// Login to container registry when running on AppSRE Jenkins.
	if _, ok := os.LookupEnv("JENKINS_HOME"); ok {
		log.Println("running in Jenkins, calling container runtime login")
		if err := sh.Run(containerRuntime,
			"login", "-u="+os.Getenv("QUAY_USER"),
			"-p="+os.Getenv("QUAY_TOKEN"), "quay.io"); err != nil {
			return fmt.Errorf("registry login: %w", err)
		}
	}

	if err := sh.Run(containerRuntime, "push", imageURL(imageName)); err != nil {
		return fmt.Errorf("pushing image: %w", err)
	}

	return nil
}

func imageURL(name string) string {
	envvar := strings.ReplaceAll(strings.ToUpper(name), "-", "_") + "_IMAGE"
	if url := os.Getenv(envvar); len(url) != 0 {
		return url
	}
	return imageOrg + "/" + name + ":" + version
}

// Code Generators
// ---------------
type Generate mg.Namespace

func (Generate) All() {
	mg.Deps(
		Generate.code,
		Generate.docs,
	)
}

func (Generate) code() error {
	mg.Deps(Dependency.ControllerGen)

	manifestsCmd := exec.Command("controller-gen",
		"crd:crdVersions=v1", "rbac:roleName=addon-operator-manager",
		"paths=./...", "output:crd:artifacts:config=../config/deploy")
	manifestsCmd.Dir = workDir + "/apis"
	if err := manifestsCmd.Run(); err != nil {
		return fmt.Errorf("generating kubernetes manifests: %w", err)
	}

	// code gen
	codeCmd := exec.Command("controller-gen", "object", "paths=./...")
	codeCmd.Dir = workDir + "/apis"
	if err := codeCmd.Run(); err != nil {
		return fmt.Errorf("generating deep copy methods: %w", err)
	}

	// patching generated code to stay go 1.16 output compliant
	// https://golang.org/doc/go1.17#gofmt
	// @TODO: remove this when we move to go 1.17"
	// otherwise our ci will fail because of changed files"
	// this removes the line '//go:build !ignore_autogenerated'"
	findArgs := []string{".", "-name", "zz_generated.deepcopy.go", "-exec",
		"sed", "-i", `/\/\/go:build !ignore_autogenerated/d`, "{}", ";"}

	// The `-i` flag works a bit differenly on MacOS (I don't know why.)
	// See - https://stackoverflow.com/a/19457213
	if goruntime.GOOS == "darwin" {
		findArgs = []string{".", "-name", "zz_generated.deepcopy.go", "-exec",
			"sed", "-i", "", "-e", `/\/\/go:build !ignore_autogenerated/d`, "{}", ";"}
	}
	if err := sh.Run("find", findArgs...); err != nil {
		return fmt.Errorf("removing go:build annotation: %w", err)
	}

	return nil
}

func (Generate) docs() error {
	mg.Deps(Build.Docgen)

	return sh.Run("./hack/docgen.sh")
}

// Testing and Linting
// -------------------
type Test mg.Namespace

func (Test) Lint() error {
	mg.Deps(
		Dependency.GolangciLint,
		Generate.All,
	)

	for _, cmd := range [][]string{
		{"go", "fmt", "./..."},
		{"bash", "./hack/validate-directory-clean.sh"},
		{"golangci-lint", "run", "./...", "--deadline=15m"},
	} {
		if err := sh.RunV(cmd[0], cmd[1:]...); err != nil {
			return fmt.Errorf("running %q: %w", strings.Join(cmd, " "), err)
		}
	}
	return nil
}

// Runs unittests.
func (Test) Unit() error {
	return sh.RunWithV(map[string]string{
		// needed to enable race detector -race
		"CGO_ENABLED": "1",
	}, "go", "test", "-cover", "-v", "-race", "./internal/...", "./cmd/...")
}

func (Test) Integration() error {
	return sh.Run("go", "test", "-v",
		"-count=1", // will force a new run, instead of using the cache
		"-timeout=20m", "./integration/...")
}

// Target to run within OpenShift CI, where the Addon Operator and webhook is already deployed via the framework.
// This target will additionally deploy the API Mock before starting the integration test suite.
func (t Test) IntegrationCI(ctx context.Context) error {
	cluster, err := dev.NewCluster(path.Join(cacheDir, "ci"),
		dev.WithKubeconfigPath(os.Getenv("KUBECONFIG")))
	if err != nil {
		return fmt.Errorf("creating cluster client: %w", err)
	}

	ctx = dev.ContextWithLogger(ctx, logger)

	var dev Dev
	if err := dev.deployAPIMock(ctx, cluster); err != nil {
		return fmt.Errorf("deploy API mock: %w", err)
	}

	os.Setenv("ENABLE_WEBHOOK", "true")
	os.Setenv("ENABLE_API_MOCK", "true")

	return t.Integration()
}

func (Test) IntegrationShort() error {
	return sh.Run("go", "test", "-v",
		"-count=1", // will force a new run, instead of using the cache
		"-short",
		"-timeout=20m", "./integration/...")
}

// Dependencies
// ------------

type toolName string
type platform string
type releaseBinaryURLs map[platform]string

// Dependency Versions

const (
	toolControllerGen toolName = "controller-gen"
	toolKind                   = "kind"
	toolYq                     = "yq"
	toolGoImports              = "go-imports"
	toolGolangciLint           = "golangci-lint"
	toolHelm                   = "helm"
	toolOpm                    = "opm"
	toolKustomize              = "kustomize"
	toolOperatorSDK            = "operator-sdk"

	platformLinuxAMD64  platform = "linux/amd64"
	platformDarwinAMD64          = "darwin/amd64"

	olmVersion string = "0.20.0"
)

type dependency struct {
	name    toolName
	version string
}

type goGettableDependency struct {
	dependency
	url string
}

type releaseBinaryDependency struct {
	dependency
	platformURLs releaseBinaryURLs
}

func (rbd *releaseBinaryDependency) url() string {
	osArch := platform(osAndArch())
	url, ok := rbd.platformURLs[osArch]
	if !ok {
		panic(fmt.Errorf("not supported download url found for tool: %q, platform: %q", rbd.name, osArch))
	}
	return url
}

func osAndArch() string {
	osArch := strings.Builder{}
	osArch.WriteString(goruntime.GOOS)
	osArch.WriteString("/")
	osArch.WriteString(goruntime.GOARCH)
	return osArch.String()
}

var goGettableDepencencies = map[toolName]goGettableDependency{
	toolControllerGen: {
		dependency{
			name:    toolControllerGen,
			version: "0.6.2",
		},
		"sigs.k8s.io/controller-tools/cmd/controller-gen",
	},
	toolKind: {
		dependency{
			name:    toolKind,
			version: "0.11.1",
		},
		"sigs.k8s.io/kind",
	},
	toolYq: {
		dependency{
			name:    toolYq,
			version: "4.12.0",
		},
		"github.com/mikefarah/yq/v4",
	},
	toolGoImports: {
		dependency{
			name:    toolGoImports,
			version: ".1.5",
		},
		"golang.org/x/tools/cmd/goimports",
	},
	toolGolangciLint: {
		dependency{
			name:    toolGolangciLint,
			version: "1.46.2",
		},
		"github.com/golangci/golangci-lint/cmd/golangci-lint",
	},
	toolHelm: {
		dependency{
			name:    toolHelm,
			version: "3.7.2",
		},
		"helm.sh/helm/v3/cmd/helm",
	},
}

var releaseBinaryDependencies = map[toolName]releaseBinaryDependency{
	toolOpm: releaseBinaryDependency{
		dependency{
			name:    toolOpm,
			version: "1.24.0",
		},
		releaseBinaryURLs{
			platformLinuxAMD64:  "https://github.com/operator-framework/operator-registry/releases/download/v%s/linux-amd64-opm",
			platformDarwinAMD64: "https://github.com/operator-framework/operator-registry/releases/download/v%s/darwin-amd64-opm",
		},
	},
	toolKustomize: releaseBinaryDependency{
		dependency{
			name:    toolKustomize,
			version: "4.5.7",
		},
		releaseBinaryURLs{
			platformLinuxAMD64:  "https://github.com/kubernetes-sigs/kustomize/releases/download/kustomize%2Fv%s/kustomize_v%s_linux_amd64.tar.gz",
			platformDarwinAMD64: "https://github.com/kubernetes-sigs/kustomize/releases/download/kustomize%2Fv%s/kustomize_v%s_darwin_amd64.tar.gz",
		},
	},
	toolOperatorSDK: releaseBinaryDependency{
		dependency{
			name:    toolOperatorSDK,
			version: "1.23.0",
		},
		releaseBinaryURLs{
			platformLinuxAMD64:  "https://github.com/operator-framework/operator-sdk/releases/download/v%s/operator-sdk_linux_amd64",
			platformDarwinAMD64: "https://github.com/operator-framework/operator-sdk/releases/download/v%s/operator-sdk_darwin_amd64",
		},
	},
}

type Dependency mg.Namespace

func (d Dependency) All() {
	mg.Deps(
		Dependency.Kind,
		Dependency.ControllerGen,
		Dependency.YQ,
		Dependency.Goimports,
		Dependency.GolangciLint,
		Dependency.Helm,
		Dependency.Opm,
	)
}

// Ensure Kind dependency - Kubernetes in Docker (or Podman)
func (d Dependency) Kind() error {
	toolConfig := goGettableDepencencies[toolKind]
	return depsDir.GoInstall(
		string(toolConfig.name),
		toolConfig.url, toolConfig.version)
}

// Ensure controller-gen - kubebuilder code and manifest generator.
func (d Dependency) ControllerGen() error {
	toolConfig := goGettableDepencencies[toolControllerGen]
	return depsDir.GoInstall(
		string(toolConfig.name),
		toolConfig.url, toolConfig.version)
}

// Ensure yq - jq but for Yaml, written in Go.
func (d Dependency) YQ() error {
	toolConfig := goGettableDepencencies[toolYq]
	return depsDir.GoInstall(
		string(toolConfig.name),
		toolConfig.url, toolConfig.version)
}

func (d Dependency) Goimports() error {
	toolConfig := goGettableDepencencies[toolGoImports]
	return depsDir.GoInstall(
		string(toolConfig.name),
		toolConfig.url, toolConfig.version)
}

func (d Dependency) GolangciLint() error {
	toolConfig := goGettableDepencencies[toolGolangciLint]
	return depsDir.GoInstall(
		string(toolConfig.name),
		toolConfig.url, toolConfig.version)
}

func (d Dependency) Helm() error {
	toolConfig := goGettableDepencencies[toolHelm]
	return depsDir.GoInstall(
		string(toolConfig.name),
		toolConfig.url, toolConfig.version)
}

func (d Dependency) Opm() error {
	toolConfig := releaseBinaryDependencies[toolOpm]
	return downloadReleaseBinary(
		string(toolConfig.name),
		toolConfig.version,
		toolConfig.url(),
	)
}

func (d Dependency) Kustomize() error {
	toolConfig := releaseBinaryDependencies[toolKustomize]
	return downloadReleaseBinary(
		string(toolConfig.name),
		toolConfig.version,
		toolConfig.url(),
	)
}

func (d Dependency) OperatorSDK() error {
	toolConfig := releaseBinaryDependencies[toolOperatorSDK]
	return downloadReleaseBinary(
		string(toolConfig.name),
		toolConfig.version,
		toolConfig.url(),
	)
}

func downloadReleaseBinary(toolName, toolVersion, url string) error {
	// TODO: move this into devkube library, to ensure the depsDir is present, even if you just call "NeedsRebuild"
	if err := os.MkdirAll(depsDir.Bin(), os.ModePerm); err != nil {
		return fmt.Errorf("create dependency dir: %w", err)
	}

	needsRebuild, err := depsDir.NeedsRebuild(toolName, toolVersion)
	if err != nil {
		return err
	}
	if !needsRebuild {
		return nil
	}

	// Tempdir
	tempDir, err := os.MkdirTemp(cacheDir, "")
	if err != nil {
		return fmt.Errorf("temp dir: %w", err)
	}
	defer os.RemoveAll(tempDir)

	// Download
	urlObj, err := gourl.Parse(strings.ReplaceAll(url, "%s", toolVersion))
	if err != nil {
		return err
	}
	u := urlObj.Path
	resourceName := u[strings.LastIndex(u, "/")+1:]
	tempToolBin := path.Join(tempDir, resourceName)
	if err := sh.RunV(
		"curl", "-L", "--fail",
		"-o", tempToolBin,
		urlObj.String(),
	); err != nil {
		return fmt.Errorf("downloading %q: %w", toolName, err)
	}

	err = setupTool(tempToolBin, toolName)
	return nil
}

func setupTool(src, toolName string) error {
	resourceName := src[strings.LastIndex(src, "/")+1:]
	toolDestination := path.Join(depsDir.Bin(), toolName)

	if strings.HasSuffix(resourceName, ".tar.gz") {
		if err := extractArchive(src, toolDestination); err != nil {
			return err
		}
	} else {
		// Move
		if err := os.Rename(src, toolDestination); err != nil {
			return fmt.Errorf("move %s: %w", toolName, err)
		}
	}
	if err := os.Chmod(toolDestination, 0755); err != nil {
		return fmt.Errorf("make %s executable: %w", toolName, err)
	}
	return nil
}

func extractArchive(src, dst string) error {
	toolName := dst[strings.LastIndex(dst, "/")+1:]
	file, err := os.Open(src)
	if err != nil {
		return err
	}
	fileReader, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	defer fileReader.Close()
	tarBallReader := tar.NewReader(fileReader)
	for {
		header, err := tarBallReader.Next()
		if err != nil {
			if err == io.EOF {
				break
			}
			return err
		}
		if header.Typeflag == tar.TypeReg && header.Name == toolName {
			writer, err := os.Create(dst)
			if err != nil {
				return err
			}

			io.Copy(writer, tarBallReader)
			err = os.Chmod(dst, os.FileMode(header.Mode))
			if err != nil {
				return err
			}
			writer.Close()
		}
	}
	return fmt.Errorf("tool %q not found in archive", toolName)
}

// Development
// --------
type Dev mg.Namespace

var (
	devEnvironment *dev.Environment
)

func (d Dev) Setup(ctx context.Context) error {
	if err := d.init(); err != nil {
		return err
	}

	if err := devEnvironment.Init(ctx); err != nil {
		return fmt.Errorf("initializing dev environment: %w", err)
	}
	return nil
}

func (d Dev) Teardown(ctx context.Context) error {
	if err := d.init(); err != nil {
		return err
	}

	if err := devEnvironment.Destroy(ctx); err != nil {
		return fmt.Errorf("tearing down dev environment: %w", err)
	}
	return nil
}

// Setup local dev environment with the addon operator installed and run the integration test suite.
func (d Dev) Integration(ctx context.Context) error {
	mg.SerialDeps(
		Dev.Deploy,
	)

	os.Setenv("KUBECONFIG", devEnvironment.Cluster.Kubeconfig())
	os.Setenv("ENABLE_WEBHOOK", "true")
	os.Setenv("ENABLE_API_MOCK", "true")

	mg.SerialDeps(Test.Integration)
	return nil
}

func (d Dev) LoadImage(ctx context.Context, image string) error {
	mg.Deps(
		mg.F(Build.ImageBuild, image),
	)

	imageTar := path.Join(cacheDir, "image", image+".tar")
	if err := devEnvironment.LoadImageFromTar(ctx, imageTar); err != nil {
		return fmt.Errorf("load image from tar: %w", err)
	}
	return nil
}

// Deploy the Addon Operator, Mock API Server and Addon Operator webhooks (if env ENABLE_WEBHOOK=true) is set.
// All components are deployed via static manifests.
func (d Dev) Deploy(ctx context.Context) error {
	mg.Deps(
		Dev.Setup, // setup is a pre-requesite and needs to run before we can load images.
	)
	mg.Deps(
		mg.F(Dev.LoadImage, "api-mock"),
		mg.F(Dev.LoadImage, "addon-operator-manager"),
		mg.F(Dev.LoadImage, "addon-operator-webhook"),
	)

	if err := d.deploy(ctx, devEnvironment.Cluster); err != nil {
		return fmt.Errorf("deploying: %w", err)
	}
	return nil
}

// Deploy all addon operator components to a cluster.
func (d Dev) deploy(
	ctx context.Context, cluster *dev.Cluster,
) error {
	if err := d.deployAPIMock(ctx, cluster); err != nil {
		return err
	}

	if err := d.deployAddonOperatorManager(ctx, cluster); err != nil {
		return err
	}

	if enableWebhooks, ok := os.LookupEnv("ENABLE_WEBHOOK"); ok &&
		enableWebhooks == "true" {
		if err := d.deployAddonOperatorWebhook(ctx, cluster); err != nil {
			return err
		}
	}
	return nil
}

// deploy the API Mock server from local files.
func (d Dev) deployAPIMock(ctx context.Context, cluster *dev.Cluster) error {
	objs, err := dev.LoadKubernetesObjectsFromFile(
		"config/deploy/api-mock/deployment.yaml.tpl")
	if err != nil {
		return fmt.Errorf("loading api-mock deployment.yaml.tpl: %w", err)
	}

	// Replace image
	apiMockDeployment := &appsv1.Deployment{}
	if err := cluster.Scheme.Convert(
		&objs[0], apiMockDeployment, nil); err != nil {
		return fmt.Errorf("converting to Deployment: %w", err)
	}
	apiMockImage := os.Getenv("API_MOCK_IMAGE")
	if len(apiMockImage) == 0 {
		apiMockImage = imageURL("api-mock")
	}
	for i := range apiMockDeployment.Spec.Template.Spec.Containers {
		container := &apiMockDeployment.Spec.Template.Spec.Containers[i]

		switch container.Name {
		case "manager":
			container.Image = apiMockImage
		}
	}

	ctx = dev.ContextWithLogger(ctx, logger)

	// Deploy
	if err := cluster.CreateAndWaitFromFiles(ctx, []string{
		// TODO: replace with CreateAndWaitFromFolders when deployment.yaml is gone.
		"config/deploy/api-mock/00-namespace.yaml",
		"config/deploy/api-mock/api-mock.yaml",
	}); err != nil {
		return fmt.Errorf("deploy addon-operator-manager dependencies: %w", err)
	}
	if err := cluster.CreateAndWaitForReadiness(ctx, apiMockDeployment); err != nil {
		return fmt.Errorf("deploy api-mock: %w", err)
	}
	return nil
}

// deploy the Addon Operator Manager from local files.
func (d Dev) deployAddonOperatorManager(ctx context.Context, cluster *dev.Cluster) error {
	objs, err := dev.LoadKubernetesObjectsFromFile(
		"config/deploy/deployment.yaml.tpl")
	if err != nil {
		return fmt.Errorf("loading addon-operator-manager deployment.yaml.tpl: %w", err)
	}

	// Replace image
	addonOperatorDeployment := &appsv1.Deployment{}
	if err := cluster.Scheme.Convert(
		&objs[0], addonOperatorDeployment, nil); err != nil {
		return fmt.Errorf("converting to Deployment: %w", err)
	}
	addonOperatorManagerImage := os.Getenv("ADDON_OPERATOR_MANAGER_IMAGE")
	if len(addonOperatorManagerImage) == 0 {
		addonOperatorManagerImage = imageURL("addon-operator-manager")
	}
	for i := range addonOperatorDeployment.Spec.Template.Spec.Containers {
		container := &addonOperatorDeployment.Spec.Template.Spec.Containers[i]

		switch container.Name {
		case "manager":
			container.Image = addonOperatorManagerImage
		}
	}

	ctx = dev.ContextWithLogger(ctx, logger)

	// Deploy
	if err := cluster.CreateAndWaitFromFiles(ctx, []string{
		// TODO: replace with CreateAndWaitFromFolders when deployment.yaml is gone.
		"config/deploy/00-namespace.yaml",
		"config/deploy/01-metrics-server-tls-secret.yaml",
		"config/deploy/addons.managed.openshift.io_addoninstances.yaml",
		"config/deploy/addons.managed.openshift.io_addonoperators.yaml",
		"config/deploy/addons.managed.openshift.io_addons.yaml",
		"config/deploy/rbac.yaml",
	}); err != nil {
		return fmt.Errorf("deploy addon-operator-manager dependencies: %w", err)
	}
	if err := cluster.CreateAndWaitForReadiness(ctx, addonOperatorDeployment); err != nil {
		return fmt.Errorf("deploy addon-operator-manager: %w", err)
	}
	return nil
}

// Addon Operator Webhook server from local files.
func (d Dev) deployAddonOperatorWebhook(ctx context.Context, cluster *dev.Cluster) error {
	objs, err := dev.LoadKubernetesObjectsFromFile(
		"config/deploy/webhook/deployment.yaml.tpl")
	if err != nil {
		return fmt.Errorf("loading addon-operator-webhook deployment.yaml.tpl: %w", err)
	}

	// Replace image
	addonOperatorWebhookDeployment := &appsv1.Deployment{}
	if err := cluster.Scheme.Convert(
		&objs[0], addonOperatorWebhookDeployment, nil); err != nil {
		return fmt.Errorf("converting to Deployment: %w", err)
	}
	addonOperatorWebhookImage := os.Getenv("ADDON_OPERATOR_WEBHOOK_IMAGE")
	if len(addonOperatorWebhookImage) == 0 {
		addonOperatorWebhookImage = imageURL("addon-operator-webhook")
	}
	for i := range addonOperatorWebhookDeployment.Spec.Template.Spec.Containers {
		container := &addonOperatorWebhookDeployment.Spec.Template.Spec.Containers[i]

		switch container.Name {
		case "webhook":
			container.Image = addonOperatorWebhookImage
		}
	}

	dev.ContextWithLogger(ctx, logger)

	// Deploy
	if err := cluster.CreateAndWaitFromFiles(ctx, []string{
		// TODO: replace with CreateAndWaitFromFolders when deployment.yaml is gone.
		"config/deploy/webhook/00-tls-secret.yaml",
		"config/deploy/webhook/service.yaml",
		"config/deploy/webhook/validatingwebhookconfig.yaml",
	}); err != nil {
		return fmt.Errorf("deploy addon-operator-webhook dependencies: %w", err)
	}
	if err := cluster.CreateAndWaitForReadiness(ctx, addonOperatorWebhookDeployment); err != nil {
		return fmt.Errorf("deploy addon-operator-webhook: %w", err)
	}
	return nil
}

func (d Dev) init() error {
	mg.SerialDeps(
		setupContainerRuntime,
		Dependency.Kind,
	)

	devEnvironment = dev.NewEnvironment(
		"addon-operator-dev",
		path.Join(cacheDir, "dev-env"),
		dev.WithClusterOptions([]dev.ClusterOption{
			dev.WithWaitOptions([]dev.WaitOption{
				dev.WithTimeout(2 * time.Minute),
			}),
		}),
		dev.WithContainerRuntime(containerRuntime),
		dev.WithClusterInitializers{
			dev.ClusterLoadObjectsFromFiles{
				// OCP APIs required by the AddonOperator.
				"config/ocp/cluster-version-operator_01_clusterversion.crd.yaml",
				"config/ocp/config-operator_01_proxy.crd.yaml",
				"config/ocp/cluster-version.yaml",
				"config/ocp/monitoring.coreos.com_servicemonitors.yaml",

				// OpenShift console to interact with OLM.
				"hack/openshift-console.yaml",
			},
			dev.ClusterLoadObjectsFromHttp{
				// Install OLM.
				"https://github.com/operator-framework/operator-lifecycle-manager/releases/download/v" + olmVersion + "/crds.yaml",
				"https://github.com/operator-framework/operator-lifecycle-manager/releases/download/v" + olmVersion + "/olm.yaml",
			},
			dev.ClusterHelmInstall{
				RepoName:    "prometheus-community",
				RepoURL:     "https://prometheus-community.github.io/helm-charts",
				PackageName: "kube-prometheus-stack",
				ReleaseName: "prometheus",
				Namespace:   "monitoring",
				SetVars: []string{
					"grafana.enabled=false",
					"kubeStateMetrics.enabled=false",
					"nodeExporter.enabled=false",
				},
			},
		})
	return nil
}
