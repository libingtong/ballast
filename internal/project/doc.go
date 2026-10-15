// Package project (unit U8) reads and validates <repo>/.ballast/project.json
// and implements `ballast init` stack detection (plan B3).
//
// Hot path: Load yes (lazy, only when an evidence predicate runs). Imports:
// model, ports, sys, doublestar (exempt validation), stdlib.
//
// project.json (frozen):
//
//	{"version":1,
//	 "verify":[{"name":"test","argv":["go","test","./..."],"timeout_s":1800,"required":true,
//	            "env":["CGO_ENABLED=0"],"cwd":"."}],
//	 "exempt":["**/*.md","docs/**"]}
//
// Validation: version absent or 1; names ^[a-z0-9][a-z0-9_-]{0,31}$ unique;
// argv non-empty, argv[0] non-empty; timeout_s 1..86400 (default 1800);
// cwd relative, no "..", inside the repo; exempt globs valid doublestar.
// Unknown fields are errors. Invalid project.json ⇒ Load error ⇒ evidence
// predicates unknown(no_project) + audit diag; `ballast verify` exits 2.
//
// init (Detect) recognizes 9 stacks by marker files at the repo root and
// SUGGESTS commands; the CLI writes project.json only after confirmation
// (or --yes), never overwriting an existing file without --force.
//
//	stack   marker(s)                         suggested argv
//	go      go.mod                            go test ./...
//	node    package.json (+lock file picks)   npm test | pnpm test | yarn test
//	python  pyproject.toml|setup.cfg|setup.py|pytest.ini  python -m pytest
//	rust    Cargo.toml                        cargo test
//	maven   pom.xml (mvnw preferred)          ./mvnw -q test | mvn -q test
//	gradle  build.gradle[.kts] (gradlew pref) ./gradlew test | gradle test
//	dotnet  *.sln | *.csproj | *.fsproj       dotnet test
//	swift   Package.swift                     swift test
//	ruby    Gemfile (spec/ ⇒ rspec)           bundle exec rspec | bundle exec rake test
//
// Default exempt suggestion: ["**/*.md", "docs/**"].
//
// Acceptance (U8): testdata/stacks/<stack>/ fixtures (stub mode, created by
// U8) detect exactly one stack each; multi-stack repos list all; Load
// rejects every invalid row of testdata/invalid.json; Loader implements
// ports.ProjectLoader.
package project
