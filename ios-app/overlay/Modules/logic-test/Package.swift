// swift-tools-version: 5.10
// Placeholder for the unpublished logic-test package (the mocking harness is not in the mirror).
// It exists so the module manifests that name it resolve; it builds an empty library.
import PackageDescription

let package = Package(
  name: "logic-test",
  platforms: [.iOS(.v16)],
  products: [
    .library(name: "logic-test", targets: ["logic-test"])
  ],
  targets: [
    .target(name: "logic-test", path: "./Sources")
  ]
)
