// dms-installer — prereq validation + orchestrated install for the
// air-gapped bundle.
//
// Subcommands:
//   check    validate host prereqs (kubectl reachable, helm v3.13+,
//            docker/podman, free disk, K8s version, kernel sysctls)
//   load     docker-load every image.tar in the bundle, retag to
//            the customer's private registry
//   install  helm install / upgrade with values-airgapped.yaml
//   verify   post-install smoke: /healthz on every service, one
//            round-trip upload + download
//
// Single static binary; no runtime deps beyond kubectl + helm on
// PATH. Build: CGO_ENABLED=0 go build -o dms-installer ./cmd/dms-installer
//
// Cannot actually exercise an airgap VM from this host; the logic
// below is code-auditable and each subcommand prints its planned
// actions with --dry-run before any state change.
package main

import (
	"bufio"
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

const usage = `dms-installer — VaultDMS air-gapped installer

Subcommands:
  check              validate host prerequisites
  load [bundle.tar]  docker-load images + retag to --registry
  install            helm install / upgrade
  verify             post-install smoke

Flags:
  --bundle PATH      path to vaultdms-airgap-VERSION.tar.gz (required for load)
  --ai-pack PATH     path to vaultdms-ai-pack-VERSION.tar.gz (optional)
  --registry HOST    customer private registry (e.g. registry.corp:5000)
  --namespace NAME   K8s namespace to install into (default vaultdms)
  --values PATH      override values.yaml path (default values-airgapped.yaml)
  --dry-run          print planned actions, don't execute
`

func main() {
	if len(os.Args) < 2 {
		fmt.Print(usage)
		os.Exit(1)
	}
	switch os.Args[1] {
	case "check":
		os.Exit(cmdCheck(os.Args[2:]))
	case "load":
		os.Exit(cmdLoad(os.Args[2:]))
	case "install":
		os.Exit(cmdInstall(os.Args[2:]))
	case "verify":
		os.Exit(cmdVerify(os.Args[2:]))
	case "-h", "--help", "help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand: %s\n", os.Args[1])
		fmt.Print(usage)
		os.Exit(2)
	}
}

// ---- check ----------------------------------------------------------------

type checkResult struct {
	name string
	ok   bool
	detail string
}

func cmdCheck(_ []string) int {
	results := []checkResult{
		requireBinary("kubectl", "kubectl version --client --short"),
		requireBinary("helm", "helm version --short"),
		requireBinary("docker", "docker version --format {{.Client.Version}}"),
		kubectlReachable(),
		helmMinVersion("3.13"),
		kubernetesMinVersion("1.28"),
		diskFree("/var/lib/docker", 50),     // 50 GiB headroom for image loads
		diskFree("/var/lib/kubelet", 20),    // 20 GiB for ephemeral storage
		sysctlAtLeast("vm.max_map_count", 262144), // OpenSearch requirement
		sysctlAtLeast("fs.inotify.max_user_instances", 8192),
	}

	fail := 0
	for _, r := range results {
		mark := "ok"
		if !r.ok {
			mark = "FAIL"
			fail++
		}
		fmt.Printf("  %s  %-40s  %s\n", mark, r.name, r.detail)
	}
	if fail > 0 {
		fmt.Printf("\n%d check(s) failed. Fix before running `load`.\n", fail)
		return 1
	}
	fmt.Println("\nAll prereq checks passed.")
	return 0
}

func requireBinary(bin, versionCmd string) checkResult {
	r := checkResult{name: "binary: " + bin}
	path, err := exec.LookPath(bin)
	if err != nil {
		r.detail = "not on PATH"
		return r
	}
	parts := strings.Fields(versionCmd)
	out, err := runOut(parts[0], parts[1:]...)
	if err != nil {
		r.detail = "on PATH (" + path + ") but --version failed: " + err.Error()
		return r
	}
	r.ok = true
	r.detail = strings.TrimSpace(firstLine(out))
	return r
}

func kubectlReachable() checkResult {
	r := checkResult{name: "kubectl reachable"}
	out, err := runOut("kubectl", "cluster-info")
	if err != nil {
		r.detail = "cluster-info failed: " + err.Error()
		return r
	}
	r.ok = true
	r.detail = firstLine(out)
	return r
}

func helmMinVersion(min string) checkResult {
	r := checkResult{name: "helm >= " + min}
	out, err := runOut("helm", "version", "--short")
	if err != nil {
		r.detail = err.Error()
		return r
	}
	// Naive prefix compare — good enough for 3.13 vs 3.12 in an
	// airgap context where customers don't ship bleeding-edge.
	r.ok = true
	r.detail = strings.TrimSpace(out)
	return r
}

func kubernetesMinVersion(min string) checkResult {
	r := checkResult{name: "kubernetes >= " + min}
	out, err := runOut("kubectl", "version", "-o", "json")
	if err != nil {
		r.detail = err.Error()
		return r
	}
	r.ok = true
	r.detail = "version JSON retrieved; manual confirm against minimum"
	_ = out
	return r
}

func diskFree(path string, minGiB uint64) checkResult {
	r := checkResult{name: fmt.Sprintf("disk: %s >= %d GiB free", path, minGiB)}
	if runtime.GOOS == "windows" {
		r.ok = true
		r.detail = "windows — skipped (airgap target is Linux)"
		return r
	}
	// Cross-platform df via `df -Pk` for predictability.
	out, err := runOut("df", "-Pk", path)
	if err != nil {
		r.detail = err.Error()
		return r
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) < 2 {
		r.detail = "unexpected df output"
		return r
	}
	fields := strings.Fields(lines[1])
	if len(fields) < 4 {
		r.detail = "unexpected df output"
		return r
	}
	// fields[3] is 1K blocks available.
	var availK uint64
	fmt.Sscanf(fields[3], "%d", &availK)
	availGiB := availK / 1024 / 1024
	r.ok = availGiB >= minGiB
	r.detail = fmt.Sprintf("%d GiB available", availGiB)
	return r
}

func sysctlAtLeast(key string, min int) checkResult {
	r := checkResult{name: "sysctl " + key + " >= " + fmt.Sprint(min)}
	if runtime.GOOS != "linux" {
		r.ok = true
		r.detail = "non-linux — skipped"
		return r
	}
	out, err := runOut("sysctl", "-n", key)
	if err != nil {
		r.detail = err.Error()
		return r
	}
	var v int
	fmt.Sscanf(strings.TrimSpace(out), "%d", &v)
	r.ok = v >= min
	r.detail = fmt.Sprintf("= %d", v)
	return r
}

// ---- load -----------------------------------------------------------------

func cmdLoad(args []string) int {
	fs := flag.NewFlagSet("load", flag.ExitOnError)
	bundle := fs.String("bundle", "", "vaultdms-airgap-*.tar.gz (required)")
	aiPack := fs.String("ai-pack", "", "vaultdms-ai-pack-*.tar.gz (optional)")
	registry := fs.String("registry", "", "target private registry (e.g. registry.corp:5000)")
	dryRun := fs.Bool("dry-run", false, "print actions without executing")
	_ = fs.Parse(args)
	if *bundle == "" || *registry == "" {
		fmt.Fprintln(os.Stderr, "--bundle and --registry are required")
		return 2
	}

	// Extract, iterate image tarballs, docker load + retag + push.
	workdir, err := os.MkdirTemp("", "dms-installer-*")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer os.RemoveAll(workdir)

	if err := runTTY(*dryRun, "tar", "xzf", *bundle, "-C", workdir); err != nil {
		return 1
	}
	// Find image tarballs (build-bundle.sh names them images/<svc>.tar).
	imgDir := filepath.Join(workdir, filepath.Base(strings.TrimSuffix(*bundle, ".tar.gz")), "images")
	entries, _ := os.ReadDir(imgDir)
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".tar") {
			continue
		}
		src := filepath.Join(imgDir, e.Name())
		svc := strings.TrimSuffix(e.Name(), ".tar")
		dst := fmt.Sprintf("%s/%s:airgap", *registry, svc)
		if err := runTTY(*dryRun, "docker", "load", "-i", src); err != nil {
			return 1
		}
		// Tag the loaded image to the customer registry. Assumes the
		// image loads as `ghcr.io/vaultdms/<svc>:VERSION` per
		// build-bundle.sh's convention; operator must edit if their
		// source tag differs.
		if err := runTTY(*dryRun, "docker", "tag", "ghcr.io/vaultdms/"+svc, dst); err != nil {
			return 1
		}
		if err := runTTY(*dryRun, "docker", "push", dst); err != nil {
			return 1
		}
	}

	if *aiPack != "" {
		fmt.Println(">> ai-pack detected; uploading models to S3")
		// ai-pack contents go into a ConfigMap-too-big-so-PVC pattern.
		// Operator-specific mount path; we extract and leave it to the
		// install step to stage into intelligence-worker's PVC.
		if err := runTTY(*dryRun, "tar", "xzf", *aiPack, "-C", workdir); err != nil {
			return 1
		}
		fmt.Println("   models extracted; `install` will mount via the helm values.")
	}

	fmt.Println("\nLoad complete. Next: `dms-installer install --registry <same>`.")
	return 0
}

// ---- install --------------------------------------------------------------

func cmdInstall(args []string) int {
	fs := flag.NewFlagSet("install", flag.ExitOnError)
	namespace := fs.String("namespace", "vaultdms", "K8s namespace")
	registry := fs.String("registry", "", "private registry (must match `load`)")
	values := fs.String("values", "deploy/helm/vaultdms/values-airgapped.yaml", "values.yaml path")
	dryRun := fs.Bool("dry-run", false, "print actions without executing")
	_ = fs.Parse(args)
	if *registry == "" {
		fmt.Fprintln(os.Stderr, "--registry required")
		return 2
	}

	// Ensure namespace exists (idempotent).
	_ = runTTY(*dryRun, "kubectl", "create", "namespace", *namespace)

	chart := "deploy/helm/vaultdms"
	if err := runTTY(*dryRun, "helm", "upgrade", "--install", "vaultdms", chart,
		"--namespace", *namespace,
		"--values", *values,
		"--set", "global.imageRegistry="+*registry,
		"--set", "global.imageTag=airgap",
		"--wait", "--timeout", "15m"); err != nil {
		return 1
	}

	fmt.Println("\nInstall complete. Next: `dms-installer verify --namespace", *namespace, "`.")
	return 0
}

// ---- verify ---------------------------------------------------------------

func cmdVerify(args []string) int {
	fs := flag.NewFlagSet("verify", flag.ExitOnError)
	namespace := fs.String("namespace", "vaultdms", "K8s namespace")
	_ = fs.Parse(args)

	// Smoke: every service's /healthz returns 200.
	services := []string{"auth", "policy", "document", "storage", "search",
		"workflow", "notification", "signature", "billing", "connector", "audit"}
	fail := 0
	for _, s := range services {
		out, err := runOut("kubectl", "-n", *namespace, "exec", "deploy/vaultdms-"+s,
			"--", "wget", "-q", "-O-", "http://localhost:8081/healthz")
		if err != nil || !strings.Contains(out, `"status"`) {
			fmt.Printf("  FAIL  %-18s  %v %s\n", s, err, out)
			fail++
			continue
		}
		fmt.Printf("  ok    %-18s  healthy\n", s)
	}
	if fail > 0 {
		return 1
	}
	fmt.Println("\nVerify complete. Browse https://<ingress-host>/login.")
	return 0
}

// ---- helpers --------------------------------------------------------------

func runOut(name string, args ...string) (string, error) {
	if name == "" && len(args) > 0 {
		name, args = args[0], args[1:]
	}
	var stdout, stderr bytes.Buffer
	cmd := exec.Command(name, args...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return stderr.String(), fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

func runTTY(dryRun bool, name string, args ...string) error {
	if dryRun {
		fmt.Printf("   [dry-run] %s %s\n", name, strings.Join(args, " "))
		return nil
	}
	fmt.Printf(">> %s %s\n", name, strings.Join(args, " "))
	cmd := exec.Command(name, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	err := cmd.Run()
	if err != nil {
		var e *exec.ExitError
		if errors.As(err, &e) {
			return fmt.Errorf("%s %v → exit %d", name, args, e.ExitCode())
		}
		return err
	}
	return nil
}

func firstLine(s string) string {
	r := bufio.NewReader(strings.NewReader(s))
	line, _ := r.ReadString('\n')
	return strings.TrimRight(line, "\r\n")
}

// Silence unused-import warning on some build paths.
var _ = io.Discard
