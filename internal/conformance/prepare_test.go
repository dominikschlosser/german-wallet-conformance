package conformance

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWalletLaunchWaitsForSimulatorRegistration(t *testing.T) {
	bin := t.TempDir()
	calls := filepath.Join(bin, "calls")
	script := "#!/bin/bash\nif [ ! -f \"$SIMULATOR_CALLS\" ]; then\n  echo first >\"$SIMULATOR_CALLS\"\n  echo FBSOpenApplicationServiceErrorDomain >&2\n  exit 1\nfi\necho second >>\"$SIMULATOR_CALLS\"\n"
	if err := os.WriteFile(filepath.Join(bin, "xcrun"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	t.Setenv("SIMULATOR_CALLS", calls)
	driver := Driver{UDID: "test-device", BundleID: "test-app", Logger: log.New(io.Discard, "", 0)}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := driver.launch(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(calls)
	if err != nil || string(got) != "first\nsecond\n" {
		t.Fatalf("launch did not recover from pending registration: %s, %v", got, err)
	}
}

func TestWalletPreparationWaitsForTheSuiteBeforeLaunching(t *testing.T) {
	bin := t.TempDir()
	calls := filepath.Join(bin, "calls")
	if err := os.MkdirAll(filepath.Join(bin, "Library", "Caches"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("APP_CONTAINER", bin)
	if err := os.WriteFile(filepath.Join(bin, "xcrun"), []byte("#!/bin/bash\nprintf '%s\\n' \"$*\" >>\"$SIMULATOR_CALLS\"\nif [ \"$2\" = get_app_container ] || [ \"$2\" = getenv ]; then printf '%s\\n' \"$APP_CONTAINER\"; fi\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	t.Setenv("SIMULATOR_CALLS", calls)
	driver := Driver{UDID: "test-device", BundleID: "test-app", onboarded: true}
	if err := driver.Prepare(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(calls)
	if err != nil || string(got) != "simctl terminate test-device test-app\nsimctl terminate test-device com.apple.mobilesafari\nsimctl get_app_container test-device test-app data\nsimctl getenv test-device HOME\n" {
		t.Fatalf("startup must wait until the suite issuer exists: %s, %v", got, err)
	}
	if driver.onboarded {
		t.Fatal("the next interaction must check onboarding after starting the suite")
	}
}

func TestWalletPreparationRejectsSimulatorErrors(t *testing.T) {
	for _, message := range []string{"found nothing to terminate", "Unable to lookup in current state: Shutdown"} {
		t.Run(message, func(t *testing.T) {
			bin := t.TempDir()
			if err := os.MkdirAll(filepath.Join(bin, "Library", "Caches"), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("APP_CONTAINER", bin)
			script := "#!/bin/bash\nif [ \"$2\" = get_app_container ] || [ \"$2\" = getenv ]; then printf '%s\\n' \"$APP_CONTAINER\"; exit 0; fi\necho \"$SIMULATOR_ERROR\" >&2\nexit 3\n"
			if err := os.WriteFile(filepath.Join(bin, "xcrun"), []byte(script), 0755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
			t.Setenv("SIMULATOR_ERROR", message)
			driver := Driver{UDID: "test-device", BundleID: "test-app"}
			err := driver.Prepare(context.Background())
			if (err == nil) != (message == "found nothing to terminate") {
				t.Fatalf("preparation with %q: %v", message, err)
			}
		})
	}
}

func TestWalletWaitsForContentAfterABlankScreen(t *testing.T) {
	bin := t.TempDir()
	script := `#!/bin/bash
if [ ! -f "$SCREEN_SEEN" ]; then
  touch "$SCREEN_SEEN"
  echo '[{"type":"Application","AXLabel":"EUDI Wallet DE"}]'
else
  echo '[{"type":"Application","AXLabel":"EUDI Wallet DE"},{"type":"StaticText","AXLabel":"Something went wrong"}]'
fi
`
	if err := os.WriteFile(filepath.Join(bin, "idb"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	t.Setenv("SCREEN_SEEN", filepath.Join(bin, "seen"))
	flow := DefaultFlow()
	flow.SettleSeconds = 0.000001
	flow.TimeoutSeconds = 3
	driver := Driver{UDID: "test-device", Flow: flow, Logger: log.New(io.Discard, "", 0)}
	message, err := driver.Automate(context.Background(), "presentation", "")
	if err != nil || !strings.Contains(message, "Something went wrong") {
		t.Fatalf("blank screen ended the interaction: message=%q err=%v", message, err)
	}
}

func TestPreparePreservesUpstreamAndRejectsLocalEdits(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(root, "upstream/ios/IDGo.xcodeproj")); err != nil {
		t.Skip("initialize upstream/ios to run build preparation integration test")
	}
	work := filepath.Join(t.TempDir(), "wallet build")
	if err = os.Mkdir(work, 0755); err != nil {
		t.Fatal(err)
	}
	run := func(name string, args ...string) string {
		t.Helper()
		out, err := exec.Command(name, args...).CombinedOutput()
		if err != nil {
			t.Fatalf("%s: %v: %s", name, err, out)
		}
		return string(out)
	}
	run("cp", "-R", filepath.Join(root, "ios-app"), filepath.Join(work, "ios-app"))
	run("git", "init", "--quiet", work)
	run("git", "-C", work, "-c", "protocol.file.allow=always", "submodule", "add", "--quiet", filepath.Join(root, "upstream/ios"), "upstream/ios")
	prepare := filepath.Join(work, "ios-app/prepare.sh")
	run(prepare)
	source := filepath.Join(work, "upstream/ios")
	build := filepath.Join(work, ".build/ios")
	err = filepath.WalkDir(source, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".swift") {
			return nil
		}
		rel, _ := filepath.Rel(source, path)
		original, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		copy, err := os.ReadFile(filepath.Join(build, rel))
		if err != nil {
			return err
		}
		if !bytes.Equal(original, copy) {
			t.Errorf("changed upstream source %s", rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := run("git", "-C", source, "status", "--porcelain"); got != "" {
		t.Fatal(got)
	}
	stale := filepath.Join(build, "Wallet/Certificates/stale.der")
	if err = os.WriteFile(stale, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	run(prepare)
	if _, err = os.Stat(stale); !os.IsNotExist(err) {
		t.Fatal("stale generated file survived")
	}
	changed := filepath.Join(source, "README.md")
	if err = os.WriteFile(changed, []byte("local edit\n"), 0600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(prepare).CombinedOutput()
	if err == nil || !strings.Contains(string(out), "local changes") {
		t.Fatalf("dirty checkout accepted: %v %s", err, out)
	}
	data, _ := os.ReadFile(changed)
	if string(data) != "local edit\n" {
		t.Fatal("discarded local edit")
	}
}

func TestParallelRunShutsDownItsSimulators(t *testing.T) {
	for _, test := range []struct {
		workers       int
		result        string
		shutdownError bool
	}{{4, "0", false}, {4, "1", false}, {12, "0", false}, {12, "1", false}, {4, "0", true}} {
		t.Run(fmt.Sprintf("%d-workers-exit-%s-shutdown-error-%t", test.workers, test.result, test.shutdownError), func(t *testing.T) {
			result := test.result
			root := t.TempDir()
			bin := filepath.Join(root, ".build/bin")
			run := filepath.Join(root, "run")
			for _, dir := range []string{bin, run} {
				if err := os.MkdirAll(dir, 0755); err != nil {
					t.Fatal(err)
				}
			}
			var err error
			if err = os.WriteFile(filepath.Join(bin, "conformance"), []byte("#!/bin/bash\nexit "+result+"\n"), 0755); err != nil {
				t.Fatal(err)
			}
			simctl := "#!/bin/bash\nprintf '%s\\n' \"$*\" >>\"$SIMULATOR_CALLS\"\n"
			if test.shutdownError {
				simctl += "if [ \"$2\" = shutdown ]; then echo shutdown-failed >&2; exit 1; fi\n"
			}
			if err = os.WriteFile(filepath.Join(bin, "xcrun"), []byte(simctl), 0755); err != nil {
				t.Fatal(err)
			}
			manifest := "vci-sdjwt\tA\tissuer-a\nvp-sdjwt\tB\tissuer-a\nvci-mdoc\tC\tissuer-c\nvp-mdoc\tD\tissuer-c\n"
			ids := []string{"A", "B", "C", "D"}
			if test.workers == 12 {
				manifest = ""
				ids = nil
				for _, format := range []string{"sdjwt", "mdoc"} {
					for shard := 0; shard < 5; shard++ {
						id := fmt.Sprintf("vci-%s-%d", format, shard)
						ids = append(ids, id)
						manifest += fmt.Sprintf("vci-%s\t%s\tissuer-%s\t%d\t5\n", format, id, id, shard)
					}
					id := "vp-" + format
					ids = append(ids, id)
					manifest += fmt.Sprintf("%s\t%s\tunused\t0\t1\n", id, id)
				}
			}
			if err = os.WriteFile(filepath.Join(run, "workers.tsv"), []byte(manifest), 0600); err != nil {
				t.Fatal(err)
			}
			previous := filepath.Join(run, "runner.log")
			if err = os.WriteFile(previous, []byte("previous results\n"), 0600); err != nil {
				t.Fatal(err)
			}
			calls := filepath.Join(root, "simulator-calls")
			t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
			t.Setenv("SIMULATOR_CALLS", calls)
			err = RunParallel(context.Background(), ParallelOptions{Executable: filepath.Join(bin, "conformance"), RunDir: run, ResumeLog: previous, Workers: test.workers}, io.Discard)
			if (err == nil) != (result == "0" && !test.shutdownError) {
				t.Fatalf("exit=%s err=%v", result, err)
			}
			if test.shutdownError && !strings.Contains(err.Error(), "shutdown-failed") {
				t.Fatalf("missing shutdown error: %v", err)
			}
			data, err := os.ReadFile(calls)
			if err != nil {
				t.Fatal(err)
			}
			for _, id := range ids {
				if strings.Count(string(data), "simctl shutdown "+id+"\n") != 1 {
					t.Errorf("must shut down %s exactly once: %s", id, data)
				}
			}
			if strings.Contains(string(data), "shutdown all") {
				t.Fatal("must not shut down other runs")
			}
			preserved, err := os.ReadFile(previous)
			if err != nil || !bytes.Equal(preserved, []byte("previous results\n")) {
				t.Fatalf("lost existing results: %v %s", err, preserved)
			}
		})
	}
}

func TestWalletCAKeepsIncompleteExistingCA(t *testing.T) {
	for _, name := range []string{"ca-key.pem", "ca-cert.pem"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, name)
			if err := os.WriteFile(path, []byte("existing CA material\n"), 0600); err != nil {
				t.Fatal(err)
			}
			out, err := exec.Command("sh", "../../scripts/wallet-ca.sh", dir).CombinedOutput()
			if err == nil || !strings.Contains(string(out), "incomplete") {
				t.Fatalf("incomplete CA accepted: %v %s", err, out)
			}
			data, _ := os.ReadFile(path)
			if string(data) != "existing CA material\n" {
				t.Fatal("replaced existing CA material")
			}
		})
	}
}

func TestParallelSetupReinstallsClonedApp(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{"scripts", "ios-app", ".build/bin", "run"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0755); err != nil {
			t.Fatal(err)
		}
	}
	script, err := os.ReadFile("../../scripts/parallel-run.sh")
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"scripts/parallel-run.sh": string(script),
		"ios-app/simulator.sh":    "#!/bin/bash\nif [[ $SIMULATOR_NAME == *sdjwt* ]]; then echo export DE_WALLET_IOS_UDID=sdjwt; else echo export DE_WALLET_IOS_UDID=mdoc; fi\n",
		".build/bin/conformance": `#!/bin/bash
if [ "$1" = parallel ]; then exit 0; fi
while [ "$#" -gt 0 ]; do
  if [ "$1" = --runner-log ]; then printf 'seed\n' >"$2"; exit 0; fi
  shift
done
exit 1
`,
		".build/bin/xcrun": `#!/bin/bash
printf '%s\n' "$*" >>"$SIMULATOR_CALLS"
case "$2" in
  get_app_container) echo "/apps/$3.app" ;;
  clone) echo "$3-vp" ;;
esac
`,
	}
	for path, data := range files {
		if err := os.WriteFile(filepath.Join(root, path), []byte(data), 0755); err != nil {
			t.Fatal(err)
		}
	}
	calls := filepath.Join(root, "calls")
	t.Setenv("PATH", filepath.Join(root, ".build/bin")+":"+os.Getenv("PATH"))
	t.Setenv("SIMULATOR_CALLS", calls)
	t.Setenv("OIDF_RUN_DIR", filepath.Join(root, "run"))
	t.Setenv("OIDF_SUITE_DIR", filepath.Join(root, "suite"))
	t.Setenv("WORKERS", "4")
	t.Setenv("RESUME_LOG", "")
	t.Setenv("SKIP_BUILD", "0")
	out, err := exec.Command(filepath.Join(root, "scripts/parallel-run.sh")).CombinedOutput()
	if err != nil {
		t.Fatalf("setup: %v: %s", err, out)
	}
	data, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	for _, format := range []string{"sdjwt", "mdoc"} {
		want := fmt.Sprintf("simctl boot %s-vp\nsimctl bootstatus %s-vp -b\nsimctl install %s-vp /apps/%s.app\nsimctl launch %s-vp org.sprind.wallet.dev\nsimctl terminate %s-vp org.sprind.wallet.dev\nsimctl shutdown %s-vp\n", format, format, format, format, format, format, format)
		if !strings.Contains(string(data), want) {
			t.Fatalf("clone was not reinstalled before use: %s", data)
		}
	}
}
