package conformance

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

type worker struct {
	Group, Simulator, Issuer string
	Shard, Count             int
}

func readWorkers(path string, count int) ([]worker, error) {
	if count < 4 || count%2 != 0 {
		return nil, fmt.Errorf("--workers must be an even number of at least four")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	expected := map[string]int{"vci-sdjwt": (count - 2) / 2, "vci-mdoc": (count - 2) / 2, "vp-sdjwt": 1, "vp-mdoc": 1}
	devices, issuers := map[string]bool{}, map[string]bool{}
	shards := map[struct {
		group string
		index int
	}]bool{}
	var workers []worker
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Split(scanner.Text(), "\t")
		if len(fields) != 3 && len(fields) != 5 {
			return nil, fmt.Errorf("%s: row %d must have 3 or 5 fields", path, len(workers)+1)
		}
		w := worker{Group: fields[0], Simulator: fields[1], Issuer: fields[2], Count: 1}
		if len(fields) == 5 {
			w.Shard, err = strconv.Atoi(fields[3])
			if err != nil {
				return nil, fmt.Errorf("%s: invalid shard %q", w.Group, fields[3])
			}
			w.Count, err = strconv.Atoi(fields[4])
			if err != nil {
				return nil, fmt.Errorf("%s: invalid worker count %q", w.Group, fields[4])
			}
		}
		if expected[w.Group] == 0 {
			return nil, fmt.Errorf("unknown test group %q", w.Group)
		}
		if w.Count != expected[w.Group] || w.Shard < 0 || w.Shard >= w.Count {
			return nil, fmt.Errorf("%s: shard %d of %d does not match --workers %d", w.Group, w.Shard, w.Count, count)
		}
		if w.Simulator == "" || w.Issuer == "" {
			return nil, fmt.Errorf("%s needs a simulator and issuer alias", w.Group)
		}
		if devices[w.Simulator] {
			return nil, fmt.Errorf("simulator is used twice: %s", w.Simulator)
		}
		devices[w.Simulator] = true
		key := struct {
			group string
			index int
		}{w.Group, w.Shard}
		if shards[key] {
			return nil, fmt.Errorf("%s repeats shard %d", w.Group, w.Shard)
		}
		shards[key] = true
		if strings.HasPrefix(w.Group, "vci-") {
			if issuers[w.Issuer] {
				return nil, fmt.Errorf("issuance workers share alias: %s", w.Issuer)
			}
			issuers[w.Issuer] = true
		}
		workers = append(workers, w)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(workers) != count {
		return nil, fmt.Errorf("%s contains %d workers, expected %d", path, len(workers), count)
	}
	return workers, nil
}

type ParallelOptions struct {
	Executable, RunDir, ResumeLog, SuiteDir, Backend, HolderKey string
	Workers                                                     int
	Args                                                        []string
}

// RunParallel starts the prepared workers and preserves their logs on every exit.
func RunParallel(ctx context.Context, o ParallelOptions, output io.Writer) (result error) {
	workers, err := readWorkers(filepath.Join(o.RunDir, "workers.tsv"), o.Workers)
	if err != nil {
		return err
	}
	previous, err := os.ReadFile(o.ResumeLog)
	if err != nil {
		return err
	}
	attempt := strconv.FormatInt(time.Now().UnixNano(), 10)
	snapshot := filepath.Join(o.RunDir, "resume-"+attempt+".log")
	if err := os.WriteFile(snapshot, previous, 0600); err != nil {
		return err
	}
	logs := []string{snapshot}
	var started []worker
	var running sync.WaitGroup
	ctx, cancel := context.WithCancel(ctx)
	defer func() {
		cancel()
		running.Wait()
		for _, w := range started {
			cleanup, stop := context.WithTimeout(context.Background(), 30*time.Second)
			out, err := exec.CommandContext(cleanup, "xcrun", "simctl", "shutdown", w.Simulator).CombinedOutput()
			if err != nil && !strings.Contains(string(out), "current state: Shutdown") {
				result = errors.Join(result, fmt.Errorf("shutdown %s: %w: %s", w.Simulator, err, out))
			}
			stop()
		}
		path := filepath.Join(o.RunDir, "runner.log")
		result = errors.Join(result, mergeRunLogs(path, logs))
		fmt.Fprintln(output, "Combined runner log:", path)
	}()

	common := []string{"run", "--suite-dir", o.SuiteDir, "--backend", o.Backend, "--holder-key", o.HolderKey, "--results-dir", filepath.Join(o.RunDir, "results")}
	results := make(chan error, len(workers))
	for _, w := range workers {
		if err := ctx.Err(); err != nil {
			return err
		}
		started = append(started, w)
		// Boot also returns an error for an already booted simulator.
		_ = exec.CommandContext(ctx, "xcrun", "simctl", "boot", w.Simulator).Run()
		if out, err := exec.CommandContext(ctx, "xcrun", "simctl", "bootstatus", w.Simulator, "-b").CombinedOutput(); err != nil {
			return fmt.Errorf("boot %s: %w: %s", w.Simulator, err, out)
		}
		name := w.Group
		if w.Count > 1 {
			name += "-" + strconv.Itoa(w.Shard)
		}
		logPath := filepath.Join(o.RunDir, name+"-"+attempt+".log")
		logs = append(logs, logPath)
		args := append([]string{}, common...)
		args = append(args, "--udid", w.Simulator, "--vci-alias", w.Issuer, "--group", w.Group, "--shard-index", strconv.Itoa(w.Shard), "--shard-count", strconv.Itoa(w.Count), "--resume", snapshot, "--runner-log", logPath)
		args = append(args, o.Args...)
		console, err := os.Create(filepath.Join(o.RunDir, name+"-console.log"))
		if err != nil {
			return err
		}
		cmd := exec.CommandContext(ctx, o.Executable, args...)
		cmd.Stdout, cmd.Stderr = console, console
		cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
		cmd.WaitDelay = 10 * time.Second
		if err := cmd.Start(); err != nil {
			console.Close()
			return err
		}
		running.Go(func() {
			defer console.Close()
			err := cmd.Wait()
			if err != nil {
				err = fmt.Errorf("%s: %w (see %s)", name, err, console.Name())
			}
			results <- err
		})
		fmt.Fprintf(output, "%s: simulator %s; log %s\n", name, w.Simulator, logPath)
	}
	for range workers {
		result = errors.Join(result, <-results)
	}
	return result
}

func mergeRunLogs(path string, logs []string) error {
	file, err := os.CreateTemp(filepath.Dir(path), "runner-*.log")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	for _, path := range logs {
		source, err := os.Open(path)
		if errors.Is(err, os.ErrNotExist) {
			continue // A worker can fail before opening its log.
		}
		if err != nil {
			return err
		}
		_, err = io.Copy(file, source)
		source.Close()
		if err != nil {
			return err
		}
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}
