package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	c "github.com/dominikschlosser/german-wallet-conformance/internal/conformance"
)

func env(name, fallback string) string {
	if s := os.Getenv(name); s != "" {
		return s
	}
	return fallback
}
func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := execute(ctx, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func execute(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: conformance run|parallel|drive|proxy|report|screenshots")
	}
	f := flag.NewFlagSet(args[0], flag.ContinueOnError)
	suite := f.String("suite", env("CONFORMANCE_SERVER", "https://localhost:8443"), "local suite URL")
	switch args[0] {
	case "parallel":
		workers := f.Int("workers", 4, "number of simulator workers")
		runDir := f.String("run-dir", "run", "run directory containing workers.tsv")
		resume := f.String("resume", "", "saved runner or seed log")
		suiteDir := f.String("suite-dir", env("OIDF_SUITE_DIR", ".build/suite"), "suite source checkout")
		backend := f.String("backend", env("DE_WALLET_BACKEND_URL", "https://localhost:8096"), "wallet backend URL")
		holder := f.String("holder-key", filepath.Join(os.Getenv("HOME"), ".german-wallet-conformance/holder-key.pem"), "holder key PEM")
		if err := f.Parse(args[1:]); err != nil {
			return err
		}
		executable, err := os.Executable()
		if err != nil {
			return err
		}
		return c.RunParallel(ctx, c.ParallelOptions{Executable: executable, Workers: *workers, RunDir: *runDir, ResumeLog: *resume, SuiteDir: *suiteDir, Backend: *backend, HolderKey: *holder, Args: append([]string{"--suite", *suite}, f.Args()...)}, os.Stdout)
	case "proxy":
		cert := f.String("cert", "", "TLS certificate")
		key := f.String("key", "", "TLS key")
		port := f.String("port", "8096", "listen port")
		upstream := f.String("upstream", "http://localhost:8086", "backend URL")
		if err := f.Parse(args[1:]); err != nil {
			return err
		}
		return c.ServeProxy(ctx, *cert, *key, *port, *upstream)
	case "report", "screenshots":
		only := f.String("only", env("ONLY_SCENARIOS", c.DefaultSelection), "include slugs containing these comma-separated strings")
		details := f.Bool("details", false, "include module verdicts and first failures")
		out := f.String("out", "docs/assets", "screenshot directory")
		chrome := f.String("chrome", "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome", "Chrome executable")
		if err := f.Parse(args[1:]); err != nil {
			return err
		}
		if f.NArg() != 1 {
			return fmt.Errorf("provide a runner.log path")
		}
		raw, err := os.ReadFile(f.Arg(0))
		if err != nil {
			return err
		}
		refs := c.SelectPlans(c.PlansOf(string(raw)), *only)
		if args[0] == "screenshots" {
			return c.Screenshots(ctx, *suite, *out, *chrome, refs)
		}
		plans, err := c.ReadReport(ctx, c.NewAPI(*suite, os.Getenv("CONFORMANCE_TOKEN")), refs, *details)
		if err != nil {
			return err
		}
		c.WriteReport(os.Stdout, *suite, plans, *details)
		return nil
	case "run", "drive":
		udid := f.String("udid", env("DE_WALLET_IOS_UDID", "booted"), "simulator UDID")
		bundle := f.String("bundle-id", env("DE_WALLET_IOS_BUNDLE_ID", "org.sprind.wallet.dev"), "app bundle ID")
		pin := f.String("pin", env("DE_WALLET_IOS_PIN", "123456"), "wallet PIN")
		flow := f.String("flow", os.Getenv("DE_WALLET_UI_FLOW"), "UI flow JSON")
		suiteDir := f.String("suite-dir", env("OIDF_SUITE_DIR", ".build/suite"), "suite source checkout")
		results := f.String("results-dir", "run/results", "result directory")
		runLog := f.String("runner-log", "run/runner.log", "runner log")
		backend := f.String("backend", env("DE_WALLET_BACKEND_URL", "https://localhost:8096"), "wallet backend URL")
		holderPath := f.String("holder-key", filepath.Join(os.Getenv("HOME"), ".german-wallet-conformance/holder-key.pem"), "holder key PEM")
		alias := f.String("vci-alias", env("OIDF_VCI_ALIAS", "oid4vc-dev-vci-de-wallet-ios"), "issuer alias compiled into app")
		sdjwt := f.String("sdjwt-configuration", env("DE_WALLET_SDJWT_CONFIGURATION", "wallet-test-sdjwt"), "suite SD-JWT configuration ID (empty omits its issuance)")
		only := f.String("only", env("ONLY_SCENARIOS", c.DefaultSelection), "include slugs containing these comma-separated strings")
		exclude := f.String("exclude", os.Getenv("EXCLUDE_SCENARIOS"), "exclude matching slugs")
		rerun := f.String("rerun", "", "variant[:module] selectors; repeat selected tests when resuming")
		rerunDeferred := f.Bool("rerun-deferred", false, "run only deferred issuance, repeating existing deferred instances")
		resume := f.String("resume", "", "previous runner log; preserve existing module instances")
		group := f.String("group", "", "vci-sdjwt, vci-mdoc, vp-sdjwt or vp-mdoc")
		shardIndex := f.Int("shard-index", 0, "worker index within the test group, starting at zero")
		shardCount := f.Int("shard-count", 1, "number of workers sharing the test group")
		if err := f.Parse(args[1:]); err != nil {
			return err
		}
		writer := io.Writer(os.Stdout)
		if args[0] == "run" {
			if err := os.MkdirAll(filepath.Dir(*runLog), 0700); err != nil {
				return err
			}
			file, err := os.OpenFile(*runLog, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if err != nil {
				return fmt.Errorf("use a fresh run directory: %w", err)
			}
			defer file.Close()
			writer = io.MultiWriter(os.Stdout, file)
		}
		logger := log.New(writer, "", log.LstdFlags)
		driver, err := c.NewDriver(ctx, *udid, *bundle, *pin, *flow, logger)
		if err != nil {
			return err
		}
		if args[0] == "drive" {
			if f.NArg() != 1 {
				return fmt.Errorf("provide a credential offer or presentation URL")
			}
			message, err := driver.Submit(ctx, f.Arg(0), "manual", "")
			if err != nil {
				return err
			}
			if message != "" {
				return errors.New(message)
			}
			return nil
		}
		if *group != "" {
			defer func() {
				if err := driver.Shutdown(); err != nil {
					logger.Printf("simulator shutdown: %v", err)
				}
			}()
		}
		api := c.NewAPI(*suite, os.Getenv("CONFORMANCE_TOKEN"))
		holder, err := c.HolderKey(*holderPath)
		if err != nil {
			return err
		}
		ca, err := api.Request(ctx, "GET", *backend+"/mock/ca.pem", "", nil)
		if err != nil {
			return err
		}
		base, err := url.Parse(*suite)
		if err != nil {
			return err
		}
		absolute, err := filepath.Abs(*suiteDir)
		if err != nil {
			return err
		}
		options := c.ConfigOptions{SuiteDir: absolute, SuiteURL: api.BaseURL, SuiteHost: base.Hostname(), VCIAlias: *alias, AliasSuffix: fmt.Sprint(time.Now().Unix()), RedirectURI: *bundle + ":/authorization", OfferEndpoint: api.BaseURL + "/test/a/" + *alias + "/credential_offer", BackendURL: *backend, BackendCA: string(ca), Holder: holder, SDJWTConfiguration: *sdjwt, Description: "German national wallet upstream iOS app; local simulator conformance run"}
		return c.NewRunner(api, driver, logger).Run(ctx, c.RunOptions{Config: options, ResultsDir: *results, Only: *only, Exclude: *exclude, Rerun: *rerun, ResumeLog: *resume, Group: *group, RerunDeferred: *rerunDeferred, ShardIndex: *shardIndex, ShardCount: *shardCount})
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}
