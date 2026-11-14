package countersign

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

func TestDeployManifests(t *testing.T) {
	kustRaw, err := os.ReadFile("kustomization.yaml")
	if err != nil {
		t.Fatalf("kustomization.yaml: %v", err)
	}
	kust := string(kustRaw)

	entries, err := os.ReadDir("deploy")
	if err != nil || len(entries) == 0 {
		t.Fatalf("deploy/: %v", err)
	}
	var deployFiles []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".yaml") {
			deployFiles = append(deployFiles, filepath.ToSlash(filepath.Join("deploy", e.Name())))
		}
	}
	sort.Strings(deployFiles)
	for _, f := range deployFiles {
		if !strings.Contains(kust, "- "+f+"\n") {
			t.Errorf("kustomization.yaml does not list %s", f)
		}
	}

	// Check tab-free YAML, no em dashes, no Secret objects, and namespace cs-workers.
	for _, f := range append([]string{"kustomization.yaml"}, deployFiles...) {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		text := string(b)
		if strings.Contains(text, "\t") {
			t.Errorf("%s contains tab characters", f)
		}
		if strings.Contains(text, "\u2014") {
			t.Errorf("%s contains an em dash", f)
		}
		if strings.Contains(text, "kind: Secret") || strings.Contains(text, "secretKeyRef") {
			t.Errorf("%s references a Secret", f)
		}
	}

	workloadBytes, err := os.ReadFile("deploy/countersign.yaml")
	if err != nil {
		t.Fatal(err)
	}
	workload := string(workloadBytes)

	// Every manifest in deploy/ must run in cs-workers and never in another namespace.
	nsLine := regexp.MustCompile(`(?m)^\s*namespace:\s*(\S+)\s*$`)
	for _, f := range deployFiles {
		b, _ := os.ReadFile(f)
		matches := nsLine.FindAllStringSubmatch(string(b), -1)
		if len(matches) == 0 {
			t.Errorf("%s declares no namespace", f)
		}
		for _, m := range matches {
			if m[1] != "cs-workers" {
				t.Errorf("%s uses namespace %q, want cs-workers", f, m[1])
			}
		}
	}

	// Image must come from the in-cluster Lazarette registry and be pinned by sha256 digest.
	digestImage := regexp.MustCompile(`(?m)^\s*image:\s*lazarette\.clearedstack\.svc\.cluster\.local:5000/countersign@sha256:[0-9a-f]{64}\s*$`)
	if !digestImage.MatchString(workload) {
		t.Error("deploy/countersign.yaml must pin the countersign image by sha256 digest in Lazarette")
	}

	requiredInWorkload := []string{
		"kind: ServiceAccount",
		"kind: PersistentVolumeClaim",
		"kind: Deployment",
		"runAsNonRoot: true",
		"runAsUser: 65532",
		"runAsGroup: 65532",
		"fsGroup: 65532",
		"type: RuntimeDefault",
		"allowPrivilegeEscalation: false",
		"readOnlyRootFilesystem: true",
		`drop: ["ALL"]`,
		"cpu: 10m",
		"memory: 30Mi",
		"key: kubernetes.io/arch",
		"operator: In",
		`values: ["arm64"]`,
		"key: kubernetes.io/hostname",
		"operator: NotIn",
		`values: ["cm3588-plus"]`,
		"name: CONTRACT_VERSION",
		"value: v1",
		"name: MUSTER_URL",
		"value: http://muster.clearedstack.svc.cluster.local:8080",
		"name: COUNTERSIGN_SPOOL",
		"mountPath: /var/spool/cs",
	}
	for _, want := range requiredInWorkload {
		if !strings.Contains(workload, want) {
			t.Errorf("deploy/countersign.yaml is missing %q", want)
		}
	}

	netpolBytes, err := os.ReadFile("deploy/networkpolicy.yaml")
	if err != nil {
		t.Fatal(err)
	}
	netpol := string(netpolBytes)
	requiredInNetpol := []string{
		"kind: NetworkPolicy",
		"name: countersign-default-deny",
		"- Ingress",
		"- Egress",
		"ingress: []",
		"kubernetes.io/metadata.name: clearedstack",
		"app.kubernetes.io/name: muster",
		"port: 8080",
		"kubernetes.io/metadata.name: kube-system",
		"port: 53",
	}
	for _, want := range requiredInNetpol {
		if !strings.Contains(netpol, want) {
			t.Errorf("deploy/networkpolicy.yaml is missing %q", want)
		}
	}
}

func TestKoConfig(t *testing.T) {
	if _, err := os.Stat("Dockerfile"); err == nil {
		t.Error("Dockerfile must not exist; Countersign is built with ko")
	}
	raw, err := os.ReadFile(".ko.yaml")
	if err != nil {
		t.Fatalf(".ko.yaml: %v", err)
	}
	ko := string(raw)
	if strings.Contains(ko, "\t") {
		t.Error(".ko.yaml contains tab characters")
	}
	baseImage := regexp.MustCompile(`(?m)^defaultBaseImage:\s*\S+@sha256:[0-9a-f]{64}\s*$`)
	if !baseImage.MatchString(ko) {
		t.Error(".ko.yaml must pin defaultBaseImage by sha256 digest")
	}
	for _, want := range []string{
		"defaultPlatforms:",
		"- linux/arm64",
		"- id: countersign",
		"main: ./cmd/countersign",
		"- CGO_ENABLED=0",
		"- -trimpath",
		"- -s",
		"- -w",
	} {
		if !strings.Contains(ko, want) {
			t.Errorf(".ko.yaml is missing %q", want)
		}
	}
}

func TestRepoRules(t *testing.T) {
	gomod, err := os.ReadFile("go.mod")
	if err != nil {
		t.Fatalf("go.mod: %v", err)
	}
	modText := string(gomod)
	if !strings.Contains(modText, "module github.com/ClearedStack/countersign\n") {
		t.Errorf("unexpected module path in go.mod:\n%s", modText)
	}
	if strings.Contains(modText, "require") {
		t.Errorf("go.mod must have zero external dependencies:\n%s", modText)
	}
	if _, err := os.Stat("go.sum"); err == nil {
		t.Error("go.sum must not exist when using only the Go standard library")
	}

	err = filepath.WalkDir(".", func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(b), "\u2014") {
			t.Errorf("%s contains an em dash", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
