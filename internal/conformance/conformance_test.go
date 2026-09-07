package conformance

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"log"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestDefaultCoverage(t *testing.T) {
	all := Scenarios("wallet-test-sdjwt", "", "")
	if len(all) != 42 {
		t.Fatalf("full inventory changed: %d", len(all))
	}
	var refs []PlanRef
	for _, s := range all {
		refs = append(refs, PlanRef{s.Slug, s.Slug})
	}
	selected := Scenarios("wallet-test-sdjwt", DefaultSelection, "")
	plans := SelectPlans(refs, DefaultSelection)
	if len(selected) != 38 || len(plans) != 38 {
		t.Fatalf("default coverage: %d scenarios, %d report plans", len(selected), len(plans))
	}
	counts := map[string]int{}
	for i, s := range selected {
		if plans[i].Slug != s.Slug {
			t.Fatalf("runner/report mismatch at %d", i)
		}
		counts[s.Name]++
		if s.Kind == "vp" && (!s.HAIP || s.Variant["response_mode"] != "direct_post.jwt") {
			t.Fatalf("unexpected presentation variant: %+v", s)
		}
	}
	if counts["oid4vci-1_0-wallet-test-plan"] != 32 || counts["oid4vci-1_0-wallet-haip-test-plan"] != 4 || counts["oid4vp-1final-wallet-haip-test-plan"] != 2 {
		t.Fatalf("unexpected coverage: %v", counts)
	}
	if len(SelectPlans(refs, "vp-final-")) != 4 || len(SelectPlans(refs, "")) != 42 {
		t.Fatal("explicit selection must retain access to supplementary plans")
	}
}

func TestWalletInitiatedExcluded(t *testing.T) {
	refs := []PlanRef{{"vci-haip-sdjwt-wallet-initiated", "sdjwt"}, {"vci-haip-mdoc-wallet-initiated", "mdoc"}, {"vci-haip-sdjwt-byval", "issuer"}}
	for _, only := range []string{"", DefaultSelection, "wallet-initiated"} {
		for _, s := range Scenarios("wallet-test-sdjwt", only, "") {
			if s.Variant["vci_authorization_code_flow_variant"] == "wallet_initiated" {
				t.Fatalf("unsupported scenario selected: %s", s.Slug)
			}
		}
		plans := SelectPlans(refs, only)
		want := 1
		if only == "wallet-initiated" {
			want = 0
		}
		if len(plans) != want || (want == 1 && plans[0].ID != "issuer") {
			t.Fatalf("selection %q includes unsupported saved results: %+v", only, plans)
		}
	}
}

func TestReportKeepsInterruptedFailuresAndUnfinishedPasses(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/plan/p":
			io.WriteString(w, `{"planName":"wallet","modules":[{"testModule":"failed","instances":["old","f"]},{"testModule":"running","instances":["r"]},{"testModule":"untouched"}]}`)
		case "/api/info/f":
			io.WriteString(w, `{"status":"INTERRUPTED","result":"FAILED"}`)
		case "/api/info/r":
			io.WriteString(w, `{"status":"RUNNING","result":"PASSED"}`)
		case "/api/log/f":
			io.WriteString(w, `[{"result":"FAILURE","msg":"bad | proof <value>"}]`)
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	report, err := ReadReport(context.Background(), NewAPI(server.URL, ""), []PlanRef{{"vp-final-mdoc", "p"}}, true)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	WriteReport(&out, server.URL, report, true)
	for _, want := range []string{"1 test plans, 1 variant runs", "| failed | FAILED | bad &#124; proof &lt;value>", "| running | RUNNING |", "| untouched | NOT RUN |"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q in %s", want, out.String())
		}
	}
}
func TestBackendTLSProxyPreservesExchange(t *testing.T) {
	payload := []byte{0, 1, 255, 7}
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if r.Method != "POST" || r.URL.RequestURI() != "/v1/wp?x=a%2Fb" || !bytes.Equal(b, payload) {
			t.Errorf("unexpected request %s %s %v", r.Method, r.URL, b)
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(201)
		w.Write(b)
	}))
	defer backend.Close()
	target, _ := url.Parse(backend.URL)
	proxy := httptest.NewTLSServer(BackendProxy(target))
	defer proxy.Close()
	response, err := proxy.Client().Post(proxy.URL+"/v1/wp?x=a%2Fb", "application/octet-stream", bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != 201 || !bytes.Equal(body, payload) {
		t.Fatalf("response %d %v", response.StatusCode, body)
	}
}

type testWallet struct {
	prepared    int
	submitted   int
	screenshots int
	submitDelay time.Duration
}

func (w *testWallet) Prepare(context.Context) error       { w.prepared++; return nil }
func (w *testWallet) Log(context.Context) ([]byte, error) { return []byte("app log\n"), nil }

func (w *testWallet) Submit(ctx context.Context, _, _, _ string) (string, error) {
	w.submitted++
	return "", pause(ctx, w.submitDelay)
}
func (w *testWallet) Screenshot(context.Context) ([]byte, error) {
	w.screenshots++
	return []byte("actual-screen"), nil
}

func TestShardResumeKeepsEachVariantOnItsAssignedWorker(t *testing.T) {
	root := t.TempDir()
	results := filepath.Join(root, "results")
	if err := os.MkdirAll(results, 0700); err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	z := zip.NewWriter(&archive)
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	plans := map[string]Plan{}
	var previous strings.Builder
	for _, s := range Scenarios("wallet-test-sdjwt", "", "") {
		id := strings.ReplaceAll(s.Slug, "-", "")
		path := filepath.Join(results, s.Slug+"-config.json")
		if err := WriteConfig(path, Config{Alias: "issuer"}); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&previous, "Running plan '%s' with configuration file '%s'\nhttps://localhost/plan-detail.html?plan=%s\n", s.Name, path, id)
		plans[id] = Plan{ID: id, Name: s.Name, Variant: s.Variant, Modules: []Module{{Name: "test", Instances: []string{"done"}}}}
	}
	resume := filepath.Join(root, "runner.log")
	if err := os.WriteFile(resume, []byte(previous.String()), 0600); err != nil {
		t.Fatal(err)
	}
	owners := map[string]int{}
	visits := map[string]int{}
	currentShard := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch {
		case strings.HasPrefix(req.URL.Path, "/api/plan/export/"):
			w.Write(archive.Bytes())
		case strings.HasPrefix(req.URL.Path, "/api/plan/"):
			id := strings.TrimPrefix(req.URL.Path, "/api/plan/")
			if owner, seen := owners[id]; seen && owner != currentShard {
				t.Errorf("%s moved from worker %d to %d", id, owner, currentShard)
			}
			owners[id] = currentShard
			visits[id]++
			json.NewEncoder(w).Encode(plans[id])
		case req.URL.Path == "/api/info/done":
			io.WriteString(w, `{"status":"FINISHED","result":"PASSED"}`)
		default:
			t.Errorf("completed tests must be preserved: %s %s", req.Method, req.URL)
			http.NotFound(w, req)
		}
	}))
	defer server.Close()
	wallet := &testWallet{}
	runner := NewRunner(NewAPI(server.URL, ""), wallet, log.New(io.Discard, "", 0))
	for _, only := range []string{"", "preauth-byval-immediate-plain"} {
		for currentShard = 0; currentShard < 5; currentShard++ {
			err := runner.Run(context.Background(), RunOptions{Config: ConfigOptions{SDJWTConfiguration: "wallet-test-sdjwt", VCIAlias: "issuer"}, ResultsDir: results, ResumeLog: resume, Only: only, ShardIndex: currentShard, ShardCount: 5})
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	for id := range plans {
		want := 1
		if strings.Contains(id, "preauthbyvalimmediateplain") {
			want = 2
		}
		if visits[id] != want {
			t.Errorf("%s visited %d times, want %d", id, visits[id], want)
		}
	}
	if wallet.prepared != 0 || wallet.submitted != 0 {
		t.Fatal("resume repeated a completed exchange")
	}
}

func TestRunnerDrivesOnceUploadsAndKeepsTerminalFailure(t *testing.T) {
	wallet := &testWallet{}
	poll := 0
	uploads := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/runner":
			if r.Method != "POST" {
				t.Error(r.Method)
			}
			if wallet.prepared != 1 {
				t.Error("wallet must stop before creating the next suite test")
			}
			io.WriteString(w, `{"id":"m"}`)
		case "/api/info/m":
			poll++
			if poll < 3 {
				io.WriteString(w, `{"status":"WAITING"}`)
			} else {
				io.WriteString(w, `{"status":"INTERRUPTED","result":"FAILED"}`)
			}
		case "/api/log/m":
			io.WriteString(w, `[{"redirect_to":"openid4vp://?request_uri=https%3A%2F%2Flocalhost%2Frequest"},{"upload":"evidence"}]`)
		case "/api/log/m/images/evidence":
			if wallet.submitted != 1 {
				t.Error("capture the wallet response after opening the request")
			}
			uploads++
			body, _ := io.ReadAll(r.Body)
			if string(body) != string(imageDataURL([]byte("actual-screen"))) {
				t.Error("wrong screenshot")
			}
			w.WriteHeader(204)
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	runner := NewRunner(NewAPI(server.URL, ""), wallet, log.New(io.Discard, "", 0))
	runner.PollInterval = time.Millisecond
	info, err := runner.RunModule(context.Background(), "p", Module{Name: "test"}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if info.Verdict() != "FAILED" || wallet.submitted != 1 || wallet.screenshots != 1 || uploads != 1 {
		t.Fatalf("%+v wallet=%+v uploads=%d", info, wallet, uploads)
	}
}
func TestDeferredModuleWaitsForWalletRetrieval(t *testing.T) {
	wallet := &testWallet{submitDelay: 200 * time.Millisecond}
	polls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "POST /api/runner":
			io.WriteString(w, `{"id":"m"}`)
		case "GET /api/info/m":
			polls++
			if polls >= 4 {
				io.WriteString(w, `{"status":"FINISHED","result":"PASSED"}`)
			} else {
				io.WriteString(w, `{"status":"WAITING"}`)
			}
		case "GET /api/log/m":
			io.WriteString(w, `[{"credential_offer_redirect_url":"openid-credential-offer://?credential_offer=%7B%22credential_issuer%22%3A%22https%3A%2F%2Flocalhost%22%7D"}]`)
		default:
			t.Errorf("deferred issuance must reach the wallet and await retrieval: %s %s", r.Method, r.URL)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	runner := NewRunner(NewAPI(server.URL, ""), wallet, log.New(io.Discard, "", 0))
	runner.PollInterval = time.Millisecond
	runner.ModuleTimeout = 100 * time.Millisecond
	module := Module{Name: "deferred", Variant: Variant{"vci_credential_issuance_mode": "deferred"}}
	info, err := runner.RunModule(context.Background(), "p", module, t.TempDir())
	if err != nil || info.Verdict() != "PASSED" || wallet.submitted != 1 || polls < 4 {
		t.Fatalf("info=%+v err=%v wallet=%+v polls=%d", info, err, wallet, polls)
	}
}
func TestMatrixAndSelectors(t *testing.T) {
	all := Scenarios("wallet-test-sdjwt", "", "")
	if len(all) != 42 {
		t.Fatal(len(all))
	}
	groups := map[string]int{}
	for _, s := range all {
		groups[s.Name]++
	}
	if len(groups) != 4 {
		t.Fatal(groups)
	}
	vp := Scenarios("wallet-test-sdjwt", "vp-final", "")
	if len(vp) != 4 {
		t.Fatal(vp)
	}
	for _, s := range vp {
		if !ModuleApplies(s, Module{Name: "oid4vp-1final-wallet-negative-test-required-non-matching-credential"}) {
			t.Fatal("omitted required negative test")
		}
	}
	for _, invalid := range []string{"0", "43", "1:0", "1:2:3", "bad"} {
		if _, err := ParseSelectors(invalid, len(all)); err == nil {
			t.Errorf("accepted %s", invalid)
		}
	}
	refs := PlansOf("Running plan 'x' with configuration file '/tmp/results/vci-final-mdoc-config.json'\nhttps://localhost/plan-detail.html?plan=abc\n")
	if len(refs) != 1 || refs[0].Slug != "vci-final-mdoc" {
		t.Fatal(refs)
	}
}

func TestResumePreservesCompletedModule(t *testing.T) {
	for _, status := range []string{"FINISHED", "INTERRUPTED"} {
		t.Run(status, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" || r.URL.Path != "/api/info/previous" {
					t.Errorf("resume must not create or drive a completed module: %s %s", r.Method, r.URL)
					http.NotFound(w, r)
					return
				}
				json.NewEncoder(w).Encode(ModuleInfo{ID: "previous", Status: status, Result: "FAILED"})
			}))
			defer server.Close()
			wallet := &testWallet{}
			runner := NewRunner(NewAPI(server.URL, ""), wallet, log.New(io.Discard, "", 0))
			info, err := runner.ResumeModule(context.Background(), Module{Instances: []string{"previous"}})
			if err != nil || info.ID != "previous" || info.Verdict() != "FAILED" || wallet.submitted != 0 {
				t.Fatalf("info=%+v err=%v wallet=%+v", info, err, wallet)
			}
		})
	}
}

func TestLargeScreenshotFitsSuiteUploadLimit(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 640, 640))
	random := rand.New(rand.NewPCG(1, 2))
	for i := range img.Pix {
		img.Pix[i] = byte(random.Uint32())
	}
	for i := 3; i < len(img.Pix); i += 4 {
		img.Pix[i] = 255
	}
	var raw bytes.Buffer
	if err := png.Encode(&raw, img); err != nil {
		t.Fatal(err)
	}
	if raw.Len() <= 500*1024 {
		t.Fatal("fixture must exceed the real suite limit")
	}
	data, err := suiteImage(raw.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if len(data) > 500*1024 || !bytes.HasPrefix(imageDataURL(data), []byte("data:image/jpeg;base64,")) {
		t.Fatal("invalid upload")
	}
	decoded, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil || decoded.Bounds() != img.Bounds() {
		t.Fatalf("image bounds changed: %v", err)
	}
}

func TestResumeCreatesOnlyTheMissingModuleInTheSamePlan(t *testing.T) {
	root := t.TempDir()
	results := filepath.Join(root, "results")
	if err := os.Mkdir(results, 0700); err != nil {
		t.Fatal(err)
	}
	scenario := Scenarios("wallet-test-sdjwt", "vci-haip-sdjwt-byval", "")[0]
	path := filepath.Join(results, scenario.Slug+"-config.json")
	if err := WriteConfig(path, Config{Alias: "issuer"}); err != nil {
		t.Fatal(err)
	}
	resume := filepath.Join(root, "previous.log")
	if err := os.WriteFile(resume, []byte(fmt.Sprintf("Running plan '%s' with configuration file '%s'\nhttps://localhost/plan-detail.html?plan=existing\n", scenario.Name, path)), 0600); err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	z := zip.NewWriter(&archive)
	f, _ := z.Create("result.json")
	io.WriteString(f, `{}`)
	z.Close()
	created := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /api/plan/existing":
			json.NewEncoder(w).Encode(Plan{Name: scenario.Name, Variant: scenario.Variant, Modules: []Module{{Name: "completed", Instances: []string{"old"}}, {Name: "missing"}}})
		case "GET /api/info/old":
			io.WriteString(w, `{"status":"INTERRUPTED","result":"FAILED"}`)
		case "POST /api/runner":
			created++
			if r.URL.Query().Get("plan") != "existing" || r.URL.Query().Get("test") != "missing" {
				t.Error("wrong module was created")
			}
			io.WriteString(w, `{"id":"new"}`)
		case "GET /api/info/new":
			io.WriteString(w, `{"status":"FINISHED","result":"PASSED"}`)
		case "GET /api/plan/export/existing":
			w.Write(archive.Bytes())
		default:
			t.Errorf("unexpected API call: %s %s", r.Method, r.URL)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	err := NewRunner(NewAPI(server.URL, ""), &testWallet{}, log.New(io.Discard, "", 0)).Run(context.Background(), RunOptions{Config: ConfigOptions{VCIAlias: "issuer", SDJWTConfiguration: "wallet-test-sdjwt"}, ResultsDir: results, Only: scenario.Slug, ResumeLog: resume})
	if err != ErrConformanceFailures || created != 1 {
		t.Fatalf("err=%v created=%d", err, created)
	}
	if _, err := os.Stat(filepath.Join(results, scenario.Slug+"-existing.zip")); err != nil {
		t.Fatal(err)
	}
}

func TestSuiteTemplatesProduceValidPresentationEndpoints(t *testing.T) {
	suite, err := filepath.Abs("../../.build/suite")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(suite, "scripts/certs-keys")); err != nil {
		t.Skip("requires release-v5.2.4 suite checkout")
	}
	for _, s := range Scenarios("wallet-test-sdjwt", "vp-", "") {
		cfg, err := BuildConfig(s, ConfigOptions{SuiteDir: suite, SuiteURL: "https://localhost:8443", SuiteHost: "localhost"})
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Server.AuthorizationEndpoint != "https://localhost:8443/test/a/oidf-vp-test-wallet/authorize" {
			t.Fatal(cfg.Server.AuthorizationEndpoint)
		}
		if s.Credential == "sdjwt" && cfg.Client.DCQL.Credentials[0].Meta.VCTs[0] != "urn:eudi:pid:1" {
			t.Fatal("wrong SD-JWT type")
		}
		if s.HAIP && !strings.Contains(cfg.Credential.TrustAnchor, "BEGIN CERTIFICATE") {
			t.Fatal("missing issuer trust root")
		}
	}
}

func TestAPIReadRetriesTimeoutButNeverRepeatsWrites(t *testing.T) {
	for _, method := range []string{"GET", "POST"} {
		t.Run(method, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := calls.Add(1)
				if n == 1 {
					<-r.Context().Done()
					return
				}
				io.WriteString(w, `{"status":"FINISHED"}`)
			}))
			defer server.Close()
			api := NewAPI(server.URL, "")
			api.Client.Timeout = 20 * time.Millisecond
			_, err := api.Request(context.Background(), method, "api/info/test", "", nil)
			if method == "GET" {
				if err != nil || calls.Load() != 2 {
					t.Fatalf("read should recover: calls=%d err=%v", calls.Load(), err)
				}
			} else if err == nil || calls.Load() != 1 {
				t.Fatalf("write must not repeat: calls=%d err=%v", calls.Load(), err)
			}
		})
	}
}

func TestAPIReadRetriesAreBoundedAndPreservePermanentErrors(t *testing.T) {
	for _, test := range []struct{ status, wantCalls int }{{404, 1}, {503, 3}} {
		t.Run(fmt.Sprint(test.status), func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(test.status) }))
			defer server.Close()
			_, err := NewAPI(server.URL, "").Request(context.Background(), "GET", "api/info/test", "", nil)
			if err == nil || int(calls.Load()) != test.wantCalls {
				t.Fatalf("calls=%d error=%v", calls.Load(), err)
			}
		})
	}
}

func TestDeferredRerunRetainsImmediateInstances(t *testing.T) {
	root := filepath.Join(t.TempDir(), "results")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	scenario := Scenarios("wallet-test-sdjwt", "vci-haip-sdjwt-byval", "")[0]
	path := filepath.Join(root, scenario.Slug+"-config.json")
	if err := WriteConfig(path, Config{Alias: "issuer"}); err != nil {
		t.Fatal(err)
	}
	resume := filepath.Join(root, "previous.log")
	if err := os.WriteFile(resume, []byte(fmt.Sprintf("Running plan '%s' with configuration file '%s'\nhttps://localhost/plan-detail.html?plan=existing\n", scenario.Name, path)), 0600); err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	z := zip.NewWriter(&archive)
	f, _ := z.Create("result.json")
	io.WriteString(f, `{}`)
	z.Close()
	created := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /api/plan/existing":
			json.NewEncoder(w).Encode(Plan{Name: scenario.Name, Variant: scenario.Variant, Modules: []Module{
				{Name: "issuance", Variant: Variant{"vci_credential_issuance_mode": "immediate"}, Instances: []string{"immediate-result"}},
				{Name: "issuance", Variant: Variant{"vci_credential_issuance_mode": "deferred"}, Instances: []string{"deferred-result"}},
			}})
		case "POST /api/runner":
			created++
			var variant Variant
			if err := json.Unmarshal([]byte(r.URL.Query().Get("variant")), &variant); err != nil {
				t.Error(err)
			}
			if r.URL.Query().Get("plan") != "existing" || variant["vci_credential_issuance_mode"] != "deferred" {
				t.Error("repeated non-deferred test or created a different plan")
			}
			io.WriteString(w, `{"id":"new-deferred"}`)
		case "GET /api/info/new-deferred":
			io.WriteString(w, `{"status":"FINISHED","result":"PASSED"}`)
		case "GET /api/info/deferred-result":
			io.WriteString(w, `{"status":"INTERRUPTED","result":"REVIEW"}`)
		case "GET /api/plan/export/existing":
			w.Write(archive.Bytes())
		default:
			t.Errorf("unexpected call: %s %s", r.Method, r.URL)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	err := NewRunner(NewAPI(server.URL, ""), &testWallet{}, log.New(io.Discard, "", 0)).Run(context.Background(), RunOptions{Config: ConfigOptions{VCIAlias: "issuer", SDJWTConfiguration: "wallet-test-sdjwt"}, ResultsDir: root, Only: scenario.Slug, ResumeLog: resume, RerunDeferred: true})
	if err != nil || created != 1 {
		t.Fatalf("err=%v created=%d", err, created)
	}
}

func TestReviewScreenshotsSaveSuiteImages(t *testing.T) {
	var pngData bytes.Buffer
	if err := png.Encode(&pngData, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/api/log/manual-review/images" {
			t.Errorf("unexpected request %s", r.URL)
			http.NotFound(w, r)
			return
		}
		json.NewEncoder(w).Encode([]map[string]string{{"img": string(imageDataURL(pngData.Bytes())), "msg": "Wallet should explain the rejected request"}})
	}))
	defer server.Close()
	out := t.TempDir()
	err := saveReviewScreenshots(context.Background(), NewAPI(server.URL, ""), out, []reviewScreenshot{{ID: "manual-review", Variant: "vp-final-mdoc-hash", Module: "oid4vp-1final-wallet-negative-test-missing-nonce"}})
	if err != nil {
		t.Fatal(err)
	}
	actual, err := os.ReadFile(filepath.Join(out, "review", "vp-final-mdoc-hash-negative-test-missing-nonce.png"))
	if err != nil || !bytes.Equal(actual, pngData.Bytes()) {
		t.Fatalf("image changed: %v", err)
	}
}
