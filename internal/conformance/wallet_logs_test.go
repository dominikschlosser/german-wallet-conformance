package conformance

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWalletLogIsolatedBetweenTests(t *testing.T) {
	container := t.TempDir()
	cache := filepath.Join(container, "Library", "Caches")
	if err := os.MkdirAll(cache, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(cache, "eudi-ios-wallet-logs.txt")
	if err := os.WriteFile(path, []byte("previous test\n"), 0600); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "xcrun"), []byte("#!/bin/bash\nif [ \"$2\" = get_app_container ] || [ \"$2\" = getenv ]; then printf '%s\\n' \"$APP_CONTAINER\"; fi\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	t.Setenv("APP_CONTAINER", container)
	d := Driver{UDID: "device", BundleID: "wallet"}
	if err := d.Prepare(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("current test\n"), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := d.Log(context.Background())
	if err != nil || string(got) != "current test\n" {
		t.Fatalf("log = %q, %v", got, err)
	}
	if err := d.Prepare(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, err = d.Log(context.Background())
	if err != nil || len(got) != 0 {
		t.Fatalf("log not reset: %q, %v", got, err)
	}
}

func TestWalletLogRejectsAnotherSimulatorContainer(t *testing.T) {
	source := t.TempDir()
	cache := filepath.Join(source, "Library", "Caches")
	if err := os.MkdirAll(cache, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(cache, "eudi-ios-wallet-logs.txt")
	if err := os.WriteFile(path, []byte("other simulator's log\n"), 0600); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	script := "#!/bin/bash\ncase \"$2\" in\nget_app_container) echo \"$SOURCE_CONTAINER\" ;;\ngetenv) echo \"$DEVICE_HOME\" ;;\nesac\n"
	if err := os.WriteFile(filepath.Join(bin, "xcrun"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	t.Setenv("SOURCE_CONTAINER", source)
	t.Setenv("DEVICE_HOME", bin)
	d := Driver{UDID: "clone", BundleID: "wallet"}
	if err := d.Prepare(context.Background()); err == nil || !strings.Contains(err.Error(), "does not belong") {
		t.Fatalf("accepted source container: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "other simulator's log\n" {
		t.Fatalf("changed source log: %q, %v", data, err)
	}
}

func TestWalletLogSavedForFailedModule(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/runner":
			io.WriteString(w, `{"id":"failed-test"}`)
		case "/api/info/failed-test":
			io.WriteString(w, `{"status":"FINISHED","result":"FAILED"}`)
		default:
			t.Errorf("unexpected request %s", r.URL)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	dir := t.TempDir()
	r := NewRunner(NewAPI(server.URL, ""), &testWallet{}, log.New(io.Discard, "", 0))
	info, err := r.RunModule(context.Background(), "plan", Module{Name: "test"}, dir)
	if err != nil || info.Verdict() != "FAILED" {
		t.Fatalf("%+v, %v", info, err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "failed-test.txt"))
	if err != nil || string(got) != "app log\n" {
		t.Fatalf("captured log = %q, %v", got, err)
	}
}

func TestFailureLogExport(t *testing.T) {
	root := t.TempDir()
	logs := filepath.Join(root, "wallet-logs")
	if err := os.Mkdir(logs, 0700); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"failed", "passed"} {
		if err := os.WriteFile(filepath.Join(logs, id+".txt"), []byte(id+" app log\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	plans := []ReportPlan{{Ref: PlanRef{Slug: "vci-haip-mdoc-byval", ID: "plan"}, Name: "oid4vci-1_0-wallet-haip-test-plan", Modules: []ReportModule{
		{ID: "failed", Name: "batch", Verdict: "FAILED", Variant: Variant{"vci_credential_issuance_mode": "deferred", "vci_credential_encryption": "plain"}},
		{ID: "passed", Name: "happy", Verdict: "PASSED"},
	}}}
	suiteLog := []byte(`{"testInfo":{"testId":"failed"},"results":[{"msg":"protocol check failed\nDetails: \"issuer\"","result":"FAILURE","sequence":9007199254740993,"fraction":1e-9}]}`)
	archivePath := filepath.Join(root, "vci-haip-mdoc-byval-plan.zip")
	archive, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	z := zip.NewWriter(archive)
	entry, err := z.Create("test-log-batch-failed.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entry.Write(suiteLog); err != nil {
		t.Fatal(err)
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	page := filepath.Join(root, "docs", "ios-wallet-logs.md")
	if err := ExportFailureLogs(plans, root, page); err != nil {
		t.Fatal(err)
	}
	text, err := os.ReadFile(page)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"oid4vci-1_0-wallet-haip-test-plan", "vci-haip-mdoc-byval", "batch", "deferred", "plain", "test-results/failed.md"} {
		if !strings.Contains(string(text), want) {
			t.Errorf("missing %q in %s", want, text)
		}
	}
	if strings.Contains(string(text), "passed.txt") {
		t.Fatal("exported a passed test")
	}
	got, err := os.ReadFile(filepath.Join(root, "docs", "test-results", "failed-wallet.txt"))
	if err != nil || string(got) != "failed app log\n" {
		t.Fatalf("exported log = %q, %v", got, err)
	}
	got, err = os.ReadFile(filepath.Join(root, "docs", "test-results", "failed-suite.json"))
	if err != nil {
		t.Fatalf("exported suite log = %q, %v", got, err)
	}
	if !bytes.Contains(got, []byte("\n  \"testInfo\": {\n    \"testId\": \"failed\"\n  }")) || !bytes.HasSuffix(got, []byte("}\n")) {
		t.Fatalf("suite log is not indented with a final newline: %s", got)
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, got); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(compact.Bytes(), suiteLog) {
		t.Fatalf("formatting changed suite data: %s", got)
	}
	got, err = os.ReadFile(filepath.Join(root, "docs", "test-results", "failed.md"))
	if err != nil || !strings.Contains(string(got), "failed-wallet.txt") || !strings.Contains(string(got), "failed-suite.json") {
		t.Fatalf("missing paired logs: %s, %v", got, err)
	}
	if err := os.Rename(archivePath, archivePath+".saved"); err != nil {
		t.Fatal(err)
	}
	if err := ExportFailureLogs(plans, root, page); err == nil {
		t.Fatal("accepted missing suite export")
	}
	if err := os.Rename(archivePath+".saved", archivePath); err != nil {
		t.Fatal(err)
	}
	plans[0].Modules[0].ID = "wrong-instance"
	if err := os.WriteFile(filepath.Join(logs, "wrong-instance.txt"), []byte("app log\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := ExportFailureLogs(plans, root, page); err == nil || !strings.Contains(err.Error(), "missing suite log") {
		t.Fatalf("accepted wrong suite instance: %v", err)
	}
	plans[0].Modules[0].ID = "failed"
	if err := os.Remove(filepath.Join(logs, "failed.txt")); err != nil {
		t.Fatal(err)
	}
	if err := ExportFailureLogs(plans, root, page); err == nil {
		t.Fatal("accepted missing failed test log")
	}
	if err := os.WriteFile(filepath.Join(logs, "failed.txt"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := ExportFailureLogs(plans, root, page); err == nil {
		t.Fatal("accepted empty failed test log")
	}
}
