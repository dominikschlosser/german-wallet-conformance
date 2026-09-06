package conformance

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const fourWorkers = "vci-sdjwt\tA\tissuer-a\nvp-sdjwt\tB\tissuer-a\nvci-mdoc\tC\tissuer-c\nvp-mdoc\tD\tissuer-c\n"

func TestParallelRejectsInvalidWorkers(t *testing.T) {
	for _, test := range []struct{ name, from, to, want string }{
		{"simulator", "vp-mdoc\tD", "vp-mdoc\tA", "simulator is used twice"},
		{"issuer", "vci-mdoc\tC\tissuer-c", "vci-mdoc\tC\tissuer-a", "share alias"},
		{"group", "vp-mdoc", "vp-other", "unknown test group"},
		{"missing", "vp-mdoc\tD\tissuer-c\n", "", "contains 3 workers"},
		{"empty", "vci-mdoc\tC\tissuer-c", "vci-mdoc\tC\t", "needs a simulator"},
		{"shard", "vci-mdoc\tC\tissuer-c", "vci-mdoc\tC\tissuer-c\t2\t1", "does not match"},
		{"count", "vci-mdoc\tC\tissuer-c", "vci-mdoc\tC\tissuer-c\t0\t5", "does not match"},
		{"duplicate shard", "vp-mdoc", "vp-sdjwt", "repeats shard"},
		{"fields", "vci-mdoc\tC\tissuer-c", "vci-mdoc\tC\tissuer-c\t0\t1\textra", "must have 3 or 5 fields"},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "workers.tsv"), []byte(strings.Replace(fourWorkers, test.from, test.to, 1)), 0600); err != nil {
				t.Fatal(err)
			}
			// Validation must fail before the manager tries to read the absent resume log.
			err := RunParallel(context.Background(), ParallelOptions{RunDir: dir, Workers: 4}, io.Discard)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("expected %q, got %v", test.want, err)
			}
		})
	}
}

func TestParallelCancellationPreservesWorkerLogs(t *testing.T) {
	dir := t.TempDir()
	previous := filepath.Join(dir, "runner.log")
	for name, content := range map[string]string{"workers.tsv": fourWorkers, "runner.log": "saved results\n", "xcrun": "#!/bin/sh\necho \"$*\" >>\"$SIMULATOR_CALLS\"\n", "conformance": `#!/bin/sh
while [ "$#" -gt 0 ]; do
  if [ "$1" = --runner-log ]; then shift; runlog=$1; fi
  shift
done
trap 'echo stopped >>"$runlog"; exit 0' TERM
echo started >"$runlog"
while :; do sleep 0.1; done
`} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	t.Setenv("SIMULATOR_CALLS", filepath.Join(dir, "calls"))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- RunParallel(ctx, ParallelOptions{Executable: filepath.Join(dir, "conformance"), RunDir: dir, ResumeLog: previous, Workers: 4}, io.Discard)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		files, _ := filepath.Glob(filepath.Join(dir, "v*-*.log"))
		ready := 0
		for _, file := range files {
			data, _ := os.ReadFile(file)
			if string(data) == "started\n" {
				ready++
			}
		}
		if ready == 4 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("workers did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled run reported success")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("workers did not stop")
	}
	data, err := os.ReadFile(previous)
	if err != nil || !strings.HasPrefix(string(data), "saved results\n") || strings.Count(string(data), "started\nstopped\n") != 4 {
		t.Fatalf("worker logs lost: %v %s", err, data)
	}
	calls, err := os.ReadFile(filepath.Join(dir, "calls"))
	if err != nil || strings.Count(string(calls), "simctl shutdown ") != 4 {
		t.Fatalf("simulators not shut down: %v %s", err, calls)
	}
}
