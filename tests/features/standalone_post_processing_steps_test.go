package features

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/eval-hub/eval-hub/pkg/mlflowclient"
	"github.com/eval-hub/eval-hub/pkg/ociclient"
	"github.com/google/uuid"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubernetesfake "k8s.io/client-go/kubernetes/fake"
)

const (
	postProcessingFixtureRoot = "post_processing"
	postProcessingGitRoot     = "tests/git-testdata/post_processing"
	postProcessingS3Root      = "post-processing-e2e"
	postProcessingMLflowRoot  = "post-processing-e2e"
	postProcessingOCIRoot     = "post-processing-e2e"
)

var postProcessingArtifactPaths = map[string]string{
	"clear":           "clear_results.json",
	"deepeval":        "results_summary.json",
	"guidellm":        "benchmarks.json",
	"inspect":         "logs/inspect-sample.json",
	"lighteval":       "lighteval_results.json",
	"mteb":            "mteb_results.json",
	"nemo_guardrails": "aggregate-metrics/metrics.json",
	"ragas":           "results.jsonl",
	"ruler":           "summary.csv",
	"swebench":        "summary.json",
}

const postProcessingCalibrationFile = "calibration.jsonl"

const postProcessingResultsManifest = `{"primary_score":{"metric":"accuracy"},"estimand":"mean"}`

const hfUploadScript = `import io, os, sys
from huggingface_hub import HfApi
repo, revision, path = sys.argv[1:]
api = HfApi(token=os.environ["HF_TOKEN"])
api.create_repo(repo_id=repo, repo_type="dataset", private=False, exist_ok=True)
payload = io.BytesIO(sys.stdin.buffer.read())
api.upload_file(path_or_fileobj=payload, path_in_repo=path, repo_id=repo, repo_type="dataset", revision=revision, commit_message="Seed EvalHub post-processing FVT fixtures")
`

func (tc *scenarioConfig) iPrepareStandalonePostProcessingFixtures(framework, resultsSource, calibrationSource string) error {
	if os.Getenv("SERVER_URL") == "" {
		return tc.logError(fmt.Errorf("standalone post-processing E2E requires SERVER_URL to target the cluster API"))
	}
	frameworkSlug := postProcessingFrameworkSlug(framework)
	artifactPath, ok := postProcessingArtifactPaths[frameworkSlug]
	if !ok {
		return tc.logError(fmt.Errorf("no post-processing fixture artifact path for framework %q", framework))
	}
	resultsPayload, err := postProcessingResultsPayload(artifactPath)
	if err != nil {
		return tc.logError(err)
	}
	calibrationPayload, err := os.ReadFile(filepath.Join("tests", "features", "test_data", "post_processing", postProcessingCalibrationFile))
	if err != nil {
		return tc.logError(fmt.Errorf("read calibration fixture: %w", err))
	}
	resultsManifest := []byte(postProcessingResultsManifest)

	resultsSource = strings.ToLower(strings.TrimSpace(resultsSource))
	calibrationSource = strings.ToLower(strings.TrimSpace(calibrationSource))
	fixtureID := uuid.NewString()
	fixtureDir := path.Join(postProcessingFixtureRoot, fixtureID)

	var k8sClient *fvtK8sClient
	getK8sClient := func() (*fvtK8sClient, error) {
		if k8sClient != nil {
			return k8sClient, nil
		}
		client, clientErr := newFVTK8sClient()
		if clientErr != nil {
			return nil, clientErr
		}
		k8sClient = client
		return k8sClient, nil
	}

	seedPVC := resultsSource == "pvc" || calibrationSource == "pvc"

	resultsRef, err := tc.preparePostProcessingSource(
		resultsSource, "results", frameworkSlug, fixtureID, artifactPath, resultsPayload, getK8sClient,
	)
	if err != nil {
		return tc.logError(fmt.Errorf("prepare %s results fixture for %s: %w", resultsSource, framework, err))
	}
	calibrationRef, err := tc.preparePostProcessingSource(
		calibrationSource, "calibration", frameworkSlug, fixtureID, postProcessingCalibrationFile, calibrationPayload, getK8sClient,
	)
	if err != nil {
		return tc.logError(fmt.Errorf("prepare %s calibration fixture for %s: %w", calibrationSource, framework, err))
	}
	if seedPVC {
		client, clientErr := getK8sClient()
		if clientErr != nil {
			return tc.logError(clientErr)
		}
		claim := postProcessingPVCClaim()
		if err := seedPostProcessingPVC(client, postProcessingNamespace(tc), claim, fixtureDir, frameworkSlug, artifactPath, resultsPayload, resultsManifest, calibrationPayload); err != nil {
			return tc.logError(fmt.Errorf("seed post-processing PVC %q: %w", claim, err))
		}
		if resultsSource == "pvc" {
			resultsRef["pvc"] = map[string]string{
				"claim_name": claim,
				"sub_path":   path.Join(fixtureDir, "results", frameworkSlug),
			}
		}
		if calibrationSource == "pvc" {
			calibrationRef["pvc"] = map[string]string{
				"claim_name": claim,
				"sub_path":   path.Join(fixtureDir, "calibration", postProcessingCalibrationFile),
			}
		}
	}

	resultsJSON, err := json.Marshal(resultsRef)
	if err != nil {
		return tc.logError(fmt.Errorf("encode results_data_ref: %w", err))
	}
	calibrationJSON, err := json.Marshal(calibrationRef)
	if err != nil {
		return tc.logError(fmt.Errorf("encode calibration_data_ref: %w", err))
	}
	name := fmt.Sprintf("ci-%s-%s-%s-%s", frameworkSlug, resultsSource, calibrationSource, fixtureID[:8])
	tc.saveValue("post_processing_name", name)
	tc.saveValue("post_processing_results_ref", string(resultsJSON))
	tc.saveValue("post_processing_calibration_ref", string(calibrationJSON))
	tc.saveValue("post_processing_fixture_id", fixtureID)
	return nil
}

func (tc *scenarioConfig) iSubmitPreparedStandalonePostProcessingRequest() error {
	name, err := tc.getValue("value:post_processing_name")
	if err != nil {
		return err
	}
	resultsRef, err := tc.getValue("value:post_processing_results_ref")
	if err != nil {
		return err
	}
	calibrationRef, err := tc.getValue("value:post_processing_calibration_ref")
	if err != nil {
		return err
	}
	var resultsDataRef map[string]any
	if err := json.Unmarshal([]byte(resultsRef), &resultsDataRef); err != nil {
		return tc.logError(fmt.Errorf("decode results_data_ref: %w", err))
	}
	var calibrationDataRef map[string]any
	if err := json.Unmarshal([]byte(calibrationRef), &calibrationDataRef); err != nil {
		return tc.logError(fmt.Errorf("decode calibration_data_ref: %w", err))
	}
	calibrationDataRef["data_config"] = map[string]any{
		"format": "jsonl",
		"columns": map[string]string{
			"sample_id":  "sample_id",
			"label":      "label",
			"prediction": "prediction",
		},
	}
	body, err := json.Marshal(map[string]any{
		"name": name,
		"operations": map[string]any{
			"confidence_interval": map[string]any{
				"results_data_ref":     resultsDataRef,
				"primary_score":        map[string]string{"metric": "accuracy"},
				"calibration_data_ref": []map[string]any{calibrationDataRef},
				"significance_level":   0.05,
			},
		},
	})
	if err != nil {
		return tc.logError(fmt.Errorf("encode standalone post-processing request: %w", err))
	}
	return tc.iSendARequestToWithBody(http.MethodPost, "/api/v1/evaluations/post-processing", string(body))
}

func (tc *scenarioConfig) preparePostProcessingSource(
	source, role, frameworkSlug, fixtureID, artifactPath string,
	payload []byte,
	getK8sClient func() (*fvtK8sClient, error),
) (map[string]any, error) {
	ref := make(map[string]any)
	if role == "results" && source == "pvc" || role == "calibration" && source == "pvc" {
		return ref, nil
	}
	switch source {
	case "s3":
		client, err := getK8sClient()
		if err != nil {
			return nil, err
		}
		secretName := postProcessingS3Secret()
		bucket := postProcessingS3Bucket()
		key := path.Join(postProcessingS3Root, fixtureID, role, frameworkSlug)
		if role == "calibration" {
			key = path.Join(postProcessingS3Root, fixtureID, role, artifactPath)
		}
		if role == "results" {
			if err := uploadPostProcessingS3Object(client, postProcessingNamespace(tc), secretName, bucket, path.Join(key, artifactPath), payload); err != nil {
				return nil, err
			}
			if err := uploadPostProcessingS3Object(client, postProcessingNamespace(tc), secretName, bucket, path.Join(key, "manifest.json"), []byte(postProcessingResultsManifest)); err != nil {
				return nil, err
			}
		} else if err := uploadPostProcessingS3Object(client, postProcessingNamespace(tc), secretName, bucket, key, payload); err != nil {
			return nil, err
		}
		ref["s3"] = map[string]string{"bucket": bucket, "key": key, "secret_ref": secretName}
	case "git":
		gitURL, revision := postProcessingGitURLAndRevision()
		gitRoot := postProcessingGitRoot
		if role == "results" {
			gitRoot = path.Join(gitRoot, "results", frameworkSlug)
		} else {
			gitRoot = path.Join(gitRoot, "calibration")
		}
		if role == "calibration" {
			gitRoot = path.Join(gitRoot, artifactPath)
		}
		ref["git"] = map[string]string{"url": gitURL, "ref": revision, "sub_path": gitRoot}
	case "hf":
		repoID := postProcessingHFRepoID()
		revision := postProcessingHFRevision()
		hfPath := path.Join(postProcessingFixtureRoot, fixtureID, "calibration", artifactPath)
		hfDir := path.Join(postProcessingFixtureRoot, fixtureID, "results", frameworkSlug)
		if role == "results" {
			hfPath = path.Join(hfDir, artifactPath)
		}
		client, err := getK8sClient()
		if err != nil {
			return nil, err
		}
		token, err := postProcessingHFToken(client, postProcessingNamespace(tc))
		if err != nil {
			return nil, err
		}
		if err := uploadPostProcessingHFFile(token, repoID, revision, hfPath, payload); err != nil {
			return nil, err
		}
		subPath := hfPath
		if role == "results" {
			if err := uploadPostProcessingHFFile(token, repoID, revision, path.Join(hfDir, "manifest.json"), []byte(postProcessingResultsManifest)); err != nil {
				return nil, err
			}
			subPath = hfDir
		}
		ref["hf"] = map[string]string{"repo_id": repoID, "revision": revision, "sub_path": subPath}
	case "mlflow":
		if role != "results" {
			return nil, fmt.Errorf("MLflow is not a supported calibration source")
		}
		client, err := tc.mlflowClient()
		if err != nil {
			return nil, err
		}
		experiment, err := client.GetOrCreateExperiment(&mlflowclient.CreateExperimentRequest{Name: "evalhub-standalone-post-processing-e2e"})
		if err != nil {
			return nil, fmt.Errorf("get or create MLflow fixture experiment: %w", err)
		}
		run, err := client.CreateRun(&mlflowclient.CreateRunRequest{
			ExperimentID: experiment.Experiment.ExperimentID,
			RunName:      "standalone-post-processing-" + fixtureID,
		})
		if err != nil {
			return nil, fmt.Errorf("create MLflow fixture run: %w", err)
		}
		artifactDir := path.Join(postProcessingMLflowRoot, fixtureID, role, frameworkSlug)
		if role == "calibration" {
			artifactDir = path.Join(postProcessingMLflowRoot, fixtureID, role)
		}
		artifactFile := path.Join(artifactDir, artifactPath)
		if err := uploadPostProcessingMLflowArtifact(client, experiment.Experiment.ExperimentID, run.Run.Info.RunID, artifactFile, payload); err != nil {
			return nil, err
		}
		if role == "results" {
			if err := uploadPostProcessingMLflowArtifact(client, experiment.Experiment.ExperimentID, run.Run.Info.RunID, path.Join(artifactDir, "manifest.json"), []byte(postProcessingResultsManifest)); err != nil {
				return nil, err
			}
		}
		ref["mlflow"] = map[string]string{"run_id": run.Run.Info.RunID, "artifact_path": artifactDir}
	case "oci":
		if role != "results" {
			return nil, fmt.Errorf("OCI is not a supported calibration source")
		}
		client, err := getK8sClient()
		if err != nil {
			return nil, err
		}
		registry, repository, secret, creds, err := postProcessingOCIConfig(client, postProcessingNamespace(tc))
		if err != nil {
			return nil, err
		}
		tag := "ppi-" + strings.ReplaceAll(fixtureID, "-", "")
		artifactDir := path.Join(postProcessingOCIRoot, fixtureID, "results", frameworkSlug)
		archive, err := postProcessingOCIArchive(map[string][]byte{
			path.Join(artifactDir, artifactPath):    payload,
			path.Join(artifactDir, "manifest.json"): []byte(postProcessingResultsManifest),
		})
		if err != nil {
			return nil, err
		}
		ociClient, err := ociclient.NewClient(registry, repository, creds, &http.Client{Timeout: 3 * time.Minute})
		if err != nil {
			return nil, err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		err = ociClient.PushDataArchive(ctx, tag, archive)
		cancel()
		if err != nil {
			return nil, fmt.Errorf("push OCI fixture artifact: %w", err)
		}
		ref["oci"] = map[string]any{
			"coordinates":   map[string]string{"oci_host": registry, "oci_repository": repository, "oci_tag": tag},
			"artifact_path": artifactDir,
			"k8s":           map[string]string{"connection": secret.Name},
		}
	default:
		return nil, fmt.Errorf("unsupported post-processing source %q", source)
	}
	return ref, nil
}

func postProcessingResultsPayload(artifactPath string) ([]byte, error) {
	var fixtureName string
	switch strings.ToLower(filepath.Ext(artifactPath)) {
	case ".jsonl":
		fixtureName = "results.jsonl"
	case ".csv":
		fixtureName = "summary.csv"
	default:
		fixtureName = "results.json"
	}
	data, err := os.ReadFile(filepath.Join("tests", "features", "test_data", "post_processing", fixtureName))
	if err != nil {
		return nil, fmt.Errorf("read results fixture %q: %w", fixtureName, err)
	}
	return data, nil
}

func postProcessingFrameworkSlug(framework string) string {
	return strings.NewReplacer(" ", "_", "-", "", ".", "").Replace(strings.ToLower(strings.TrimSpace(framework)))
}

func postProcessingNamespace(tc *scenarioConfig) string {
	if namespace := strings.TrimSpace(os.Getenv("X_TENANT")); namespace != "" {
		return namespace
	}
	if namespace := strings.TrimSpace(tc.reqHeaders["X-Tenant"]); namespace != "" {
		return namespace
	}
	return "test-tenant"
}

func postProcessingS3Secret() string {
	return envOr("TEST_POST_PROCESSING_S3_SECRET", "inspect-test-data-s3")
}

func postProcessingS3Bucket() string {
	return envOr("TEST_POST_PROCESSING_S3_BUCKET", envOr("TEST_DATA_S3_BUCKET", "mlpipeline"))
}

func postProcessingPVCClaim() string {
	return envOr("TEST_POST_PROCESSING_PVC_CLAIM", envOr("TEST_DATA_PVC_CLAIM_NAME", "ppi-e2e-calibration"))
}

func postProcessingGitURLAndRevision() (string, string) {
	gitURL := os.Getenv("TEST_POST_PROCESSING_GIT_URL")
	if gitURL == "" {
		gitURL = envOr("TEST_DATA_GIT_URL", "https://github.com/eval-hub/eval-hub.git")
	}
	revision := os.Getenv("TEST_POST_PROCESSING_GIT_REF")
	if revision == "" {
		revision = os.Getenv("GITHUB_HEAD_REF")
	}
	if revision == "" {
		revision = os.Getenv("GITHUB_REF_NAME")
	}
	if revision == "" {
		if output, err := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD").Output(); err == nil {
			revision = strings.TrimSpace(string(output))
		}
	}
	if revision == "" {
		revision = envOr("TEST_DATA_GIT_REF", "main")
	}
	return gitURL, revision
}

func postProcessingHFRepoID() string {
	return envOr("TEST_POST_PROCESSING_HF_REPO_ID", "eval-hub-test/evalhub-post-processing-e2e")
}

func postProcessingHFRevision() string {
	return envOr("TEST_POST_PROCESSING_HF_REVISION", "main")
}

func postProcessingOCIConfig(client *fvtK8sClient, namespace string) (registry, repository string, secret *corev1.Secret, creds ociclient.Credentials, err error) {
	registry = envOr("TEST_POST_PROCESSING_OCI_REGISTRY", os.Getenv("OCI_REGISTRY"))
	repository = envOr("TEST_POST_PROCESSING_OCI_REPOSITORY", envOr("OCI_REPOSITORY", "rh-ee-nbs/nbs-dev"))
	secretName := envOr("TEST_POST_PROCESSING_OCI_SECRET", os.Getenv("OCI_SECRET_NAME"))
	candidateNames := postProcessingOCICredentialSecretNames(namespace)
	if secretName != "" {
		candidateNames = []string{secretName}
	}
	for _, candidateName := range candidateNames {
		candidate, getErr := client.clientset.CoreV1().Secrets(namespace).Get(context.Background(), candidateName, metav1.GetOptions{})
		if getErr != nil {
			if secretName != "" {
				err = fmt.Errorf("read OCI credential Secret %q in tenant namespace %q: %w", candidateName, namespace, getErr)
				return
			}
			continue
		}
		if candidate.Type != corev1.SecretTypeDockerConfigJson {
			continue
		}
		dockerConfig, configErr := ociclient.DockerConfigJSONFromSecret(candidate.Data)
		if configErr != nil {
			continue
		}
		if registry == "" {
			registry = preferredPostProcessingOCIRegistry(dockerConfig)
		}
		if registry == "" {
			continue
		}
		candidateCreds, parseErr := ociclient.ParseDockerConfigJSON(dockerConfig, registry)
		if parseErr != nil {
			continue
		}
		secret, creds = candidate, candidateCreds
		break
	}
	if secret == nil {
		if secretName != "" {
			err = fmt.Errorf("OCI credential Secret %q in tenant namespace %q has no usable Docker credentials for registry %q", secretName, namespace, registry)
		} else {
			err = fmt.Errorf("no usable OCI credential Secret found in tenant namespace %q (expected a quay.io Secret such as oci-credentials in sagar or my-oci-credentials in prabhu)", namespace)
		}
		return
	}
	if repository == "" {
		err = fmt.Errorf("OCI repository is required; set TEST_POST_PROCESSING_OCI_REPOSITORY or OCI_REPOSITORY")
	}
	return
}

func postProcessingOCICredentialSecretNames(namespace string) []string {
	if namespace == "sagar" {
		return []string{"oci-credentials", "oci-registry-credentials", "sagar-oci-secret"}
	}
	if namespace == "prabhu" {
		return []string{"my-oci-credentials", "oci-registry-credentials", "sagar-oci-secret"}
	}
	return []string{"oci-credentials", "my-oci-credentials", "oci-registry-credentials", "sagar-oci-secret"}
}

func preferredPostProcessingOCIRegistry(dockerConfig []byte) string {
	var config struct {
		Auths map[string]json.RawMessage `json:"auths"`
	}
	if json.Unmarshal(dockerConfig, &config) != nil {
		return ""
	}
	hosts := make([]string, 0, len(config.Auths))
	for host := range config.Auths {
		hosts = append(hosts, host)
	}
	sort.Strings(hosts)
	for _, host := range hosts {
		if strings.Contains(strings.ToLower(host), "quay.io") {
			return host
		}
	}
	if len(hosts) > 0 {
		return hosts[0]
	}
	return ""
}

func envOr(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func uploadPostProcessingS3Object(client *fvtK8sClient, namespace, secretName, bucket, key string, payload []byte) error {
	secret, err := client.clientset.CoreV1().Secrets(namespace).Get(context.Background(), secretName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("read S3 credentials Secret %q: %w", secretName, err)
	}
	value := func(name string) string { return strings.TrimSpace(string(secret.Data[name])) }
	accessKey, secretKey, region := value("AWS_ACCESS_KEY_ID"), value("AWS_SECRET_ACCESS_KEY"), value("AWS_DEFAULT_REGION")
	endpoint := envOr("TEST_POST_PROCESSING_S3_ENDPOINT", value("AWS_S3_ENDPOINT"))
	if accessKey == "" || secretKey == "" || region == "" || endpoint == "" {
		return fmt.Errorf("S3 Secret %q must contain AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY, AWS_DEFAULT_REGION, and AWS_S3_ENDPOINT", secretName)
	}
	cfg, err := awsconfig.LoadDefaultConfig(context.Background(),
		awsconfig.WithRegion(region),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(accessKey, secretKey, value("AWS_SESSION_TOKEN"))),
	)
	if err != nil {
		return fmt.Errorf("configure S3 fixture uploader: %w", err)
	}
	s3Client := s3.NewFromConfig(cfg, func(options *s3.Options) {
		options.BaseEndpoint = aws.String(endpoint)
		options.UsePathStyle = true
	})
	_, err = s3Client.PutObject(context.Background(), &s3.PutObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
		Body:   bytes.NewReader(payload),
	})
	if err != nil {
		return fmt.Errorf("upload S3 fixture to bucket %q key %q: %w", bucket, key, err)
	}
	return nil
}

func uploadPostProcessingMLflowArtifact(client *mlflowclient.Client, experimentID, runID, artifactPath string, payload []byte) error {
	fullPath := path.Join(experimentID, runID, "artifacts", artifactPath)
	contentType := http.DetectContentType(payload)
	if _, err := client.UploadArtifact(fullPath, bytes.NewReader(payload), contentType); err != nil {
		return fmt.Errorf("upload MLflow fixture artifact %q: %w", artifactPath, err)
	}
	return nil
}

func postProcessingOCIArchive(files map[string][]byte) ([]byte, error) {
	var output bytes.Buffer
	gzipWriter := gzip.NewWriter(&output)
	tarWriter := tar.NewWriter(gzipWriter)
	filenames := make([]string, 0, len(files))
	for filename := range files {
		filenames = append(filenames, filename)
	}
	sort.Strings(filenames)
	for _, filename := range filenames {
		payload := files[filename]
		if err := tarWriter.WriteHeader(&tar.Header{
			Name:     filename,
			Mode:     0o644,
			Size:     int64(len(payload)),
			Typeflag: tar.TypeReg,
		}); err != nil {
			return nil, fmt.Errorf("write OCI fixture archive header: %w", err)
		}
		if _, err := tarWriter.Write(payload); err != nil {
			return nil, fmt.Errorf("write OCI fixture archive file %q: %w", filename, err)
		}
	}
	if err := tarWriter.Close(); err != nil {
		return nil, fmt.Errorf("close OCI fixture tar: %w", err)
	}
	if err := gzipWriter.Close(); err != nil {
		return nil, fmt.Errorf("close OCI fixture gzip: %w", err)
	}
	return output.Bytes(), nil
}

func postProcessingHFToken(client *fvtK8sClient, namespace string) (string, error) {
	if token := strings.TrimSpace(os.Getenv("HF_TOKEN")); token != "" {
		return token, nil
	}
	secretName := envOr("TEST_POST_PROCESSING_HF_TOKEN_SECRET", "huggingface-credentials")
	secret, err := client.clientset.CoreV1().Secrets(namespace).Get(context.Background(), secretName, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("read Hugging Face upload credential Secret %q: %w", secretName, err)
	}
	for _, key := range []string{"token", "HF_TOKEN"} {
		if token := strings.TrimSpace(string(secret.Data[key])); token != "" {
			return token, nil
		}
	}
	return "", fmt.Errorf("Hugging Face Secret %q needs a token or HF_TOKEN entry", secretName)
}

func uploadPostProcessingHFFile(token, repoID, revision, repoPath string, payload []byte) error {
	python := envOr("TEST_POST_PROCESSING_PYTHON", "python3")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, python, "-c", hfUploadScript, repoID, revision, repoPath)
	cmd.Env = replaceEnvironmentValue(os.Environ(), "HF_TOKEN", token)
	cmd.Stdin = bytes.NewReader(payload)
	output, err := cmd.CombinedOutput()
	if err != nil {
		detail := strings.TrimSpace(strings.ReplaceAll(string(output), token, "[redacted]"))
		return fmt.Errorf("upload Hugging Face fixture %q failed: %w: %s", repoPath, err, detail)
	}
	return nil
}

func seedPostProcessingPVC(client *fvtK8sClient, namespace, claim, fixtureDir, frameworkSlug, resultPath string, results, manifest, calibration []byte) error {
	nameSuffix := strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	configMapName := "post-processing-fixtures-" + nameSuffix
	jobName := "post-processing-fixtures-" + nameSuffix
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	configMaps := client.clientset.CoreV1().ConfigMaps(namespace)
	_, err := configMaps.Create(ctx, &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: configMapName},
		Data: map[string]string{
			"results":     string(results),
			"manifest":    string(manifest),
			"calibration": string(calibration),
		},
	}, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("create fixture ConfigMap: %w", err)
	}
	cleanConfigMap := func() { _ = configMaps.Delete(context.Background(), configMapName, metav1.DeleteOptions{}) }
	resultDir := path.Join("/data", fixtureDir, "results", frameworkSlug)
	resultTarget := path.Join(resultDir, resultPath)
	manifestTarget := path.Join(resultDir, "manifest.json")
	calibrationTarget := path.Join("/data", fixtureDir, "calibration", postProcessingCalibrationFile)
	command := fmt.Sprintf("set -eu; mkdir -p %q %q; cp /fixtures/results %q; cp /fixtures/manifest %q; cp /fixtures/calibration %q", path.Dir(resultTarget), path.Dir(calibrationTarget), resultTarget, manifestTarget, calibrationTarget)
	backoff := int32(0)
	activeDeadline := int64(180)
	_, err = client.clientset.BatchV1().Jobs(namespace).Create(ctx, &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: jobName},
		Spec: batchv1.JobSpec{
			BackoffLimit:          &backoff,
			ActiveDeadlineSeconds: &activeDeadline,
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					RestartPolicy:      corev1.RestartPolicyNever,
					ServiceAccountName: "default",
					Containers: []corev1.Container{{
						Name:    "seed-fixtures",
						Image:   envOr("TEST_POST_PROCESSING_SEEDER_IMAGE", "busybox:1.36.1"),
						Command: []string{"sh", "-c", command},
						VolumeMounts: []corev1.VolumeMount{
							{Name: "fixtures", MountPath: "/fixtures", ReadOnly: true},
							{Name: "data", MountPath: "/data"},
						},
					}},
					Volumes: []corev1.Volume{
						{Name: "fixtures", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: configMapName}}}},
						{Name: "data", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: claim}}},
					},
				},
			},
		},
	}, metav1.CreateOptions{})
	if err != nil {
		cleanConfigMap()
		return fmt.Errorf("create PVC fixture seeder Job: %w", err)
	}
	jobs := client.clientset.BatchV1().Jobs(namespace)
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		job, getErr := jobs.Get(ctx, jobName, metav1.GetOptions{})
		if getErr != nil {
			return fmt.Errorf("get PVC fixture seeder Job %q: %w", jobName, getErr)
		}
		if job.Status.Succeeded > 0 {
			propagation := metav1.DeletePropagationBackground
			_ = jobs.Delete(context.Background(), jobName, metav1.DeleteOptions{PropagationPolicy: &propagation})
			cleanConfigMap()
			return nil
		}
		if job.Status.Failed > 0 {
			return fmt.Errorf("PVC fixture seeder Job %q failed", jobName)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("timed out waiting for PVC fixture seeder Job %q", jobName)
		case <-ticker.C:
		}
	}
}

func replaceEnvironmentValue(environment []string, name, value string) []string {
	prefix := name + "="
	filtered := make([]string, 0, len(environment)+1)
	for _, entry := range environment {
		if !strings.HasPrefix(entry, prefix) {
			filtered = append(filtered, entry)
		}
	}
	return append(filtered, prefix+value)
}

func TestPostProcessingOCIConfigUsesTenantCredentialSecret(t *testing.T) {
	t.Setenv("TEST_POST_PROCESSING_OCI_REGISTRY", "")
	t.Setenv("OCI_REGISTRY", "")
	t.Setenv("TEST_POST_PROCESSING_OCI_REPOSITORY", "")
	t.Setenv("OCI_REPOSITORY", "")
	t.Setenv("TEST_POST_PROCESSING_OCI_SECRET", "")
	t.Setenv("OCI_SECRET_NAME", "")

	for _, testCase := range []struct {
		namespace  string
		secretName string
	}{
		{namespace: "sagar", secretName: "oci-credentials"},
		{namespace: "prabhu", secretName: "my-oci-credentials"},
	} {
		t.Run(testCase.namespace, func(t *testing.T) {
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: testCase.secretName, Namespace: testCase.namespace},
				Type:       corev1.SecretTypeDockerConfigJson,
				Data: map[string][]byte{
					".dockerconfigjson": []byte(`{"auths":{"https://quay.io":{"username":"fixture-user","password":"fixture-password"}}}`),
				},
			}
			client := &fvtK8sClient{clientset: kubernetesfake.NewSimpleClientset(secret)}

			registry, repository, gotSecret, creds, err := postProcessingOCIConfig(client, testCase.namespace)
			if err != nil {
				t.Fatalf("postProcessingOCIConfig() error = %v", err)
			}
			if registry != "https://quay.io" {
				t.Errorf("registry = %q, want https://quay.io", registry)
			}
			if repository != "rh-ee-nbs/nbs-dev" {
				t.Errorf("repository = %q, want rh-ee-nbs/nbs-dev", repository)
			}
			if gotSecret.Name != testCase.secretName {
				t.Errorf("secret = %q, want %q", gotSecret.Name, testCase.secretName)
			}
			if creds.Username != "fixture-user" || creds.Password != "fixture-password" {
				t.Errorf("credentials did not come from the tenant Secret")
			}
		})
	}
}

func TestPostProcessingOCIArchiveContainsFiles(t *testing.T) {
	files := map[string][]byte{
		"results/manifest.json": []byte(postProcessingResultsManifest),
		"results/rows.jsonl":    []byte(`{"sample_id":"eval-001","prediction":0.65}`),
	}
	archive, err := postProcessingOCIArchive(files)
	if err != nil {
		t.Fatalf("postProcessingOCIArchive() error = %v", err)
	}
	gzipReader, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		t.Fatalf("open gzip archive: %v", err)
	}
	defer func() {
		if err := gzipReader.Close(); err != nil {
			t.Errorf("close gzip archive: %v", err)
		}
	}()

	tarReader := tar.NewReader(gzipReader)
	found := make(map[string]string)
	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("read tar entry: %v", err)
		}
		content, err := io.ReadAll(tarReader)
		if err != nil {
			t.Fatalf("read tar file %q: %v", header.Name, err)
		}
		found[header.Name] = string(content)
	}
	for name, expected := range files {
		if found[name] != string(expected) {
			t.Errorf("archive file %q = %q, want %q", name, found[name], expected)
		}
	}
}
