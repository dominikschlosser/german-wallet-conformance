# Build the iOS app

The main test command builds the [official app](https://github.com/german-national-wallet/de-eudi-wallet-ios) automatically from `upstream/ios`. This page explains the local build files and how to build separately.

`ios-app/overlay` supplies files missing from the published app: support packages, entitlements, dummy Firebase configuration, sample data and Dev settings. Fonts and the NFC video are placeholders. Preparation copies the app and overlay into `.build/ios`, preserving the app's Swift implementation.

The Swift placeholders keep test directories and the `logic-test` and `feature-test` packages valid for SwiftPM. They contain no tests.

## Build and launch

With the suite and backend running, build and launch the app:

```bash
ios-app/simulator.sh .build/suite/scripts/certs-keys/vp-signing-ca.crt
```

The script builds and installs the Dev app, enables Face ID and trusts the local certificates. It prints the simulator ID. The Dev setup does not exercise production telemetry, push delivery or device integrity checks.

| Variable                | Default or purpose                                                                                   |
| ----------------------- | ---------------------------------------------------------------------------------------------------- |
| `SIMULATOR_NAME`        | `iPhone 17`. Reuse or create a simulator with this name.                                             |
| `DE_WALLET_IOS_UDID`    | Select a simulator directly.                                                                         |
| `DE_WALLET_BACKEND_URL` | `https://localhost:8096`.                                                                            |
| `PID_ISSUER_URL`        | `https://localhost:8443/test/a/oid4vc-dev-vci-de-wallet-ios/`. Must match the runner's issuer alias. |
| `DE_WALLET_CA_DIR`      | `~/.german-wallet-conformance`.                                                                      |
| `DERIVED_DATA`          | `.build/DerivedData`. Contains the build log and app.                                                |
| `SKIP_BUILD=1`          | Reinstall the existing build. Changed settings or certificates need a rebuild.                       |
| `ERASE=1`               | Erase the selected simulator, including apps and credentials.                                        |

The certificate argument lets the wallet trust the suite's presentation requests. Additional reader CA certificates can be passed in PEM or DER format.

To inspect the project without building, run `ios-app/prepare.sh`. Keep local build changes in `ios-app/overlay`, since preparation replaces `.build/ios` and requires a clean upstream submodule.
