// swift-tools-version: 5.10
// Placeholder for the unpublished feature-test package (the mocking harness is not in the mirror).
// It exists so the module manifests that name it resolve; it builds an empty library.
import PackageDescription

let package = Package(
  name: "feature-test",
  platforms: [.iOS(.v16)],
  products: [
    .library(name: "feature-test", targets: ["feature-test"])
  ],
  targets: [
    .target(name: "feature-test", path: "./Sources")
  ]
)
