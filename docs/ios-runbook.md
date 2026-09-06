# iOS runbook

## Prerequisites

- macOS with Xcode and an iOS simulator runtime.
- Go 1.26+, Java 21+, Maven, Git, jq and OpenSSL.
- Docker with Compose v2, running.
- idb: `pipx install fb-idb` and `brew install facebook/fb/idb-companion`.
- Google Chrome for overview screenshots.

## Start a run

```bash
git submodule update --init --recursive
scripts/run-conformance.sh
```

This starts the suite and wallet backend, configures TLS, builds the app and completes onboarding. The suite issues credentials that the wallet then uses for presentation. Initial downloads and builds take several minutes. Later runs reuse them.

The runner keeps the Mac awake until it finishes. It closes the app and browser before each test to clear cached issuer metadata and pending browser callbacks. Stored credentials remain available. It shuts down its simulators when their tests finish, including after an interrupted run.

Use `scripts/run-conformance.sh --prepare` to prepare the same setup without running tests.

Many issuance tests wait 20 seconds for additional requests. After driving the wallet, the runner waits up to three minutes for completion, or 45 seconds after an app error. Simulator startup has a separate wait.

### Parallel runs

```bash
scripts/run-conformance.sh --workers 4
# For a machine with more memory:
scripts/run-conformance.sh --workers 12
```

Two simulators handle presentation, one per format. The rest share issuance tests for SD-JWT and mdoc. Each issuance worker has its own issuer URL. Setup issues one credential per format and clones those wallets for presentation. The two setup issuances count toward the results.

Use an even worker count of at least four. Each run creates its own simulators. More workers need more memory and an app build for each issuer URL.

### Resume

```bash
scripts/run-conformance.sh --workers 4 --resume /path/to/run/runner.log
```

Resume keeps every completed result, including failures. Use the same worker count. Keep the run directory, suite database and simulators so the runner can find them. `workers.tsv` records which simulator and issuer serve each group of tests.

To repeat only deferred issuance while retaining other results:

```bash
scripts/run-conformance.sh --workers 4 --resume /path/to/run/runner.log --rerun-deferred
```

An active test gets 45 seconds to finish before the runner captures its screen and cancels it. To resume a single simulator run, omit `--workers 4` and use the original simulator.

### Select tests

The default is VCI Final, VCI HAIP and VP HAIP. The same selection applies to reports and screenshots.

Wallet initiated issuance is excluded from runs, reports and screenshots because this setup cannot complete the app's eID enrollment flow.

Run one issuance test:

```bash
ONLY_SCENARIOS=vci-final-sdjwt-preauth-byval-immediate-plain \
  scripts/run-conformance.sh --rerun 1:1
```

Run a six test sample with both formats: two issuance tests and one presentation test per format.

```bash
ONLY_SCENARIOS=vci-final-sdjwt-preauth-byval-immediate-plain,vci-final-mdoc-preauth-byval-immediate-plain,vp-haip \
  scripts/run-conformance.sh --workers 4 --rerun 1:3,2:3,3:1,4:1
```

`ONLY_SCENARIOS` replaces the default selection with variant names containing any of the listed strings. `EXCLUDE_SCENARIOS` removes matches. The runner numbers the remaining variants in order. For example, `--rerun 2:6` runs test 6 in variant 2. Use commas to select several tests.

With `--resume`, an explicit `--rerun` repeats the selected tests. The report uses their new results.

Presentation requires stored suite credentials. Parallel setup issues them automatically. For a single simulator presentation run, issue them first.

## Settings

| Variable             | Purpose                                                                                       |
| -------------------- | --------------------------------------------------------------------------------------------- |
| `DE_WALLET_IOS_UDID` | Simulator for serial runs. Default: select or create `iPhone 17`.                             |
| `SKIP_BUILD=1`       | Reinstall the existing app build. Its settings and certificates must still match.             |
| `OIDF_RUN_DIR`       | Result directory. Default: a timestamped directory under `~/.german-wallet-conformance/run/`. |
| `OIDF_SUITE_DIR`     | Pinned suite checkout. Default: `.build/suite`.                                               |
| `DE_WALLET_CA_DIR`   | Local CA directory. Default: `~/.german-wallet-conformance`.                                  |

## Results and screenshots

The command prints the run directory. It contains `runner.log` for progress and app actions, and `results/` for configurations and suite logs. Parallel runs combine their logs when finished.

Read the results or capture the suite overview:

```bash
.build/bin/conformance report /path/to/runner.log
.build/bin/conformance report --details /path/to/runner.log
.build/bin/conformance screenshots /path/to/runner.log
```

Reports use the latest result for each test. `--details` adds the first failed check. Screenshots go to `docs/assets`, with wallet screens from completed Review tests in `docs/assets/review`. Capture them after the tests finish, with the suite still running.

Use `--only` to report or capture a different selection, for example `report --only vp-final /path/to/runner.log`. Use `--only ''` to include all supported variants in the log.

A nonzero exit can mean a failed check, a warning, an incomplete test or a runner error. Check the logs for the cause.

## Services

Setup fetches suite **release-v5.2.4**, applies the [SD-JWT configuration patch](ios-results.md#sd-jwt-issuance-and-german-pid-enrollment) and builds it with `mvn clean package`. The Java server runs on the host. MongoDB and nginx run in Docker.

Suite build and server logs are in `.build/suite-service`. MongoDB data is in `.build/suite/mongo/data`. Backend accounts and keys are in Docker volumes.

The suite uses ports 8080, 8443–8445 and 27017. Its JAR settings are `fintechlabs.devmode=true`, `fintechlabs.startredir=false`, `fintechlabs.base_url=https://localhost:8443` and `fintechlabs.base_mtls_url=https://localhost:8444`. The committed [nginx configuration](../suite/nginx.conf) applies the [metadata workaround](ios-results.md#valid-mtls-endpoint-metadata-is-rejected) and reuses connections to the host server.

The backend serves HTTP on 8086. The runner supplies TLS on 8096 for the duration of setup and tests.

Stop services while retaining data:

```bash
kill "$(cat .build/suite-service/server.pid)"
(cd .build/suite && docker compose -p german-wallet-suite -f docker-compose-dev-mac-nodocker.yml stop)
(cd upstream/mock-backend && docker compose -p de-wallet-ios-conformance stop)
```

## Troubleshooting

- **Certificate chain rejected:** rebuild the app to bundle the current CA certificates.
- **`MDVM_TOKEN_VERIFICATION_FAILURE`:** app and backend registrations differ. Use a fresh simulator with a new backend Compose project.
- **Incomplete interaction:** inspect the app screen and `runner.log` to see where the exchange stopped.
- **PID offer rejected:** see the enrollment and proof requirements in [setup limits](ios-results.md#setup-limits).
