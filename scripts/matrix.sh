#!/usr/bin/env bash
# shellcheck disable=SC2164,SC2034  # set -e is on: a failing cd aborts the script
# matrix.sh - plan B3 verification matrix: 9 stacks x {success, failure}.
#
#   matrix.sh --mode stub    always runs. `ballast init --yes` must propose the
#                            right command for each stack's marker files, then
#                            `ballast verify` runs it with a stub toolchain (a
#                            copy of accutil named like the tool) and must
#                            report pass / fail correctly and keep --check in
#                            step. Needs no real toolchain.
#   matrix.sh --mode real    the real toolchains. A stack whose toolchain is
#                            missing (or cannot run its fixture here) is an
#                            explicit SKIP locally and a FAILURE when CI=true.
#                            With CI=true each OS owns the stacks of the plan:
#                            Linux 8 (all but swift), macOS swift, Windows
#                            dotnet; the others are skipped there on purpose.
#                            Stacks that must download plugins or packages
#                            (maven, gradle, dotnet, ruby) only run under CI or
#                            with BALLAST_MATRIX_ALLOW_NET=1, because the local
#                            scripts never touch the network.
#
# The two modes are reported separately (SUMMARY script=matrix-stub|matrix-real).
# shellcheck source=lib.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib.sh"

MODE=""
while [ "$#" -gt 0 ]; do
  case $1 in
    --mode) MODE="${2:-}"; shift 2 ;;
    --mode=*) MODE="${1#--mode=}"; shift ;;
    *) echo "usage: matrix.sh --mode stub|real" >&2; exit 2 ;;
  esac
done
case $MODE in
  stub | real) ;;
  *) echo "usage: matrix.sh --mode stub|real" >&2; exit 2 ;;
esac

lib_init "matrix-$MODE"
build_ballast
ORIG_HOME="${HOME:-}" # before sandbox_new redirects it (toolchain homes, see below)
sandbox_new matrix

STACKS="go node python rust maven gradle dotnet swift ruby"

# expected_argv STACK: the JSON argv `ballast init` must propose for the
# marker files of internal/project/testdata/stacks/<stack>.
expected_argv() {
  case $1 in
    go) echo '["go","test","./..."]' ;;
    node) echo '["npm","test"]' ;;
    python) echo '["python","-m","pytest"]' ;;
    rust) echo '["cargo","test"]' ;;
    maven) echo '["./mvnw","-q","test"]' ;;
    gradle) echo '["./gradlew","test"]' ;;
    dotnet) echo '["dotnet","test"]' ;;
    swift) echo '["swift","test"]' ;;
    ruby) echo '["bundle","exec","rspec"]' ;;
  esac
}

# stub_name STACK: the name a stub must carry to stand in for argv[0].
stub_name() {
  case $1 in
    go) echo go ;; node) echo npm ;; python) echo python ;; rust) echo cargo ;;
    maven) echo mvnw ;; gradle) echo gradlew ;; dotnet) echo dotnet ;; swift) echo swift ;; ruby) echo bundle ;;
  esac
}

# new_repo DIR: an empty git repository.
new_repo() { git_init "$1"; }

# init_project DIR WANT_ARGV_JSON LABEL: ballast init --yes inside DIR and check it.
init_project() {
  local dir=$1 want=$2 label=$3
  bl_dir "$dir" init --yes
  expect_rc "$label: ballast init --yes" 0
  check_eq "$label: init proposes the stack's command" "$("$ACCUTIL" json get "$dir/.ballast/project.json" verify.0.argv)" "$want"
  check_eq "$label: required entry named test" "$("$ACCUTIL" json get "$dir/.ballast/project.json" verify.0.name)" "test"
}

# run_cases LABEL DIR: success and failure are separate repos prepared by the
# caller; this runs the pass leg: background verify + status + --check fresh.
pass_leg() {
  local label=$1 dir=$2 job
  cd "$dir"
  bl verify test
  expect_rc "$label success: verify test starts" 0
  job="$(printf '%s\n' "$BL_OUT" | sed -n 's/^job=\([^ ]*\).*/\1/p' | head -n 1)"
  if wait_status 0 120 "$job"; then
    ok "$label success: verify --status reports pass (0)"
  else
    bad "$label success: verify --status reports pass (0)" "last status $BL_RC" "$BL_OUT" "$BL_ERR"
  fi
  bl verify --check
  expect_rc "$label success: --check is fresh (0)" 0
  cd "$SB_WORK"
}

fail_leg() {
  local label=$1 dir=$2
  cd "$dir"
  bl verify --foreground test
  expect_rc "$label failure: verify --foreground reports fail (1)" 1
  bl verify --status test
  expect_rc "$label failure: verify --status reports fail (1)" 1
  bl verify --check
  expect_rc "$label failure: --check is stale (10)" 10
  cd "$SB_WORK"
}

# ===========================================================================
if [ "$MODE" = stub ]; then
  STUBBIN="$TMP_ROOT/stubbin"
  mkdir -p "$STUBBIN"
  for t in go npm pnpm yarn python python3 cargo mvn gradle dotnet swift bundle; do
    cp "$ACCUTIL" "$STUBBIN/$t$EXE"
  done
  ORIG_PATH="$PATH"

  for stack in $STACKS; do
    section "stub: $stack"
    FIX="$REPO_ROOT/internal/project/testdata/stacks/$stack"
    [ -d "$FIX" ] || die "fixture directory missing: $FIX"
    for leg in success failure; do
      DIR="$SB_WORK/$stack-$leg"
      new_repo "$DIR"
      cp -R "$FIX/." "$DIR/"
      # The repo-local wrappers (mvnw, gradlew) become stubs as well.
      for w in mvnw gradlew; do
        if [ -f "$DIR/$w" ]; then
          cp "$ACCUTIL" "$DIR/$w"
          if [ "$IS_WINDOWS" = 1 ]; then cp "$ACCUTIL" "$DIR/$w.exe"; fi
          chmod +x "$DIR/$w"
        fi
      done
      gitc "$DIR" add -A
      gitc "$DIR" commit -q -m fixture
      init_project "$DIR" "$(expected_argv "$stack")" "$stack $leg"
      gitc "$DIR" add -A
      gitc "$DIR" commit -q -m project
      LOG="$SB_ROOT/stub-$stack-$leg.log"
      : >"$LOG"
      export PATH="$STUBBIN:$ORIG_PATH"
      ACC_STUB_LOG="$(native "$LOG")"
      export ACC_STUB_LOG
      if [ "$leg" = success ]; then export ACC_STUB_EXIT=0; else export ACC_STUB_EXIT=1; fi
      if [ "$leg" = success ]; then pass_leg "$stack" "$DIR"; else fail_leg "$stack" "$DIR"; fi
      PATH="$ORIG_PATH"
      unset ACC_STUB_LOG ACC_STUB_EXIT
      want_stub="$(stub_name "$stack")"
      if grep -q "^$want_stub " "$LOG" || grep -qx "$want_stub" "$LOG"; then
        ok "$stack $leg: the proposed command really ran (stub log)"
      else
        bad "$stack $leg: the proposed command really ran" "stub log: $(cat "$LOG")"
      fi
    done
  done

  section "stub: two stacks in one repository"
  DIR="$SB_WORK/multi"
  new_repo "$DIR"
  cp "$REPO_ROOT/internal/project/testdata/stacks/go/go.mod" "$DIR/"
  cp "$REPO_ROOT/internal/project/testdata/stacks/node/package.json" "$DIR/"
  gitc "$DIR" add -A
  gitc "$DIR" commit -q -m fixture
  bl_dir "$DIR" init --yes
  expect_rc "multi: init --yes" 0
  check_eq "multi: names are test-<stack>" "$("$ACCUTIL" json get "$DIR/.ballast/project.json" verify.0.name),$("$ACCUTIL" json get "$DIR/.ballast/project.json" verify.1.name)" "test-go,test-node"
  gitc "$DIR" add -A
  gitc "$DIR" commit -q -m project
  LOG="$SB_ROOT/stub-multi.log"
  : >"$LOG"
  ACC_STUB_LOG="$(native "$LOG")"
  export PATH="$STUBBIN:$ORIG_PATH" ACC_STUB_LOG ACC_STUB_EXIT=0
  cd "$DIR"
  bl verify
  expect_rc "multi: ballast verify starts every required entry" 0
  for n in test-go test-node; do
    if wait_status 0 60 "$n"; then ok "multi: $n passes"; else bad "multi: $n passes" "status $BL_RC"; fi
  done
  bl verify --check
  expect_rc "multi: both entries pass -> fresh" 0
  export ACC_STUB_EXIT=1
  bl verify --foreground test-node
  expect_rc "multi: a failing entry fails" 1
  bl verify --check
  expect_rc "multi: one failing required entry -> stale" 10
  PATH="$ORIG_PATH"
  unset ACC_STUB_LOG ACC_STUB_EXIT
  cd "$SB_WORK"
  finish
fi

# ===========================================================================
# real mode
# sandbox_new moved HOME away; rustup keeps its toolchains under $HOME/.rustup,
# so without this the installed (rustup-managed) cargo reports "no default
# toolchain configured". Only read access to the real toolchain directory is
# needed; CARGO_HOME (registry cache, build locks) stays in the sandbox.
if [ -z "${RUSTUP_HOME:-}" ] && [ -n "${ORIG_HOME:-}" ] && [ -d "$ORIG_HOME/.rustup" ]; then
  export RUSTUP_HOME="$ORIG_HOME/.rustup"
fi

PLATFORM="$(uname -s)"
case $PLATFORM in
  Darwin) OSKEY=darwin ;;
  Linux) OSKEY=linux ;;
  *) OSKEY=windows ;;
esac
# BALLAST_MATRIX_OS=linux|darwin|windows pretends to be that OS when deciding
# which stacks CI owns (used to test the CI rules on another machine).
OSKEY="${BALLAST_MATRIX_OS:-$OSKEY}"

# assigned_here STACK: under CI, is this stack run on this OS (plan B3)?
assigned_here() {
  case $OSKEY:$1 in
    linux:swift) return 1 ;;
    linux:*) return 0 ;;
    darwin:swift) return 0 ;;
    windows:dotnet) return 0 ;;
  esac
  return 1
}

needs_net() {
  case $1 in maven | gradle | dotnet | ruby) return 0 ;; esac
  return 1
}

# fixture STACK pass|fail DIR: a tiny project that passes / fails its tests.
fixture() {
  local stack=$1 leg=$2 dir=$3
  case $stack in
    go)
      write_file "$dir/go.mod" "module example.invalid/m

go 1.21
"
      if [ "$leg" = pass ]; then body='func TestOK(t *testing.T) {}'; else body='func TestBoom(t *testing.T) { t.Fatal("boom") }'; fi
      write_file "$dir/m_test.go" "package m

import \"testing\"

$body
"
      ;;
    node)
      write_file "$dir/package.json" '{"name":"m","version":"1.0.0","scripts":{"test":"node test.js"}}
'
      if [ "$leg" = pass ]; then write_file "$dir/test.js" "process.exit(0);
"; else write_file "$dir/test.js" "process.exit(1);
"; fi
      ;;
    python)
      write_file "$dir/pyproject.toml" '[tool.pytest.ini_options]
testpaths = ["."]
'
      if [ "$leg" = pass ]; then write_file "$dir/test_m.py" "def test_ok():
    assert True
"; else write_file "$dir/test_m.py" "def test_boom():
    assert False
"; fi
      ;;
    rust)
      write_file "$dir/Cargo.toml" '[package]
name = "m"
version = "0.1.0"
edition = "2021"
'
      mkdir -p "$dir/src"
      if [ "$leg" = pass ]; then ass='assert!(true)'; else ass='assert!(false)'; fi
      write_file "$dir/src/lib.rs" "#[test]
fn t() {
    $ass;
}
"
      ;;
    maven)
      write_file "$dir/pom.xml" '<project xmlns="http://maven.apache.org/POM/4.0.0">
  <modelVersion>4.0.0</modelVersion>
  <groupId>invalid.example</groupId><artifactId>m</artifactId><version>1</version>
  <properties><maven.compiler.source>11</maven.compiler.source><maven.compiler.target>11</maven.compiler.target>
    <project.build.sourceEncoding>UTF-8</project.build.sourceEncoding></properties>
</project>
'
      mkdir -p "$dir/src/main/java"
      if [ "$leg" = pass ]; then write_file "$dir/src/main/java/A.java" "class A {}
"; else write_file "$dir/src/main/java/A.java" "class A { this does not compile }
"; fi
      ;;
    gradle)
      write_file "$dir/build.gradle" "plugins { id 'java' }
"
      mkdir -p "$dir/src/main/java"
      if [ "$leg" = pass ]; then write_file "$dir/src/main/java/A.java" "class A {}
"; else write_file "$dir/src/main/java/A.java" "class A { this does not compile }
"; fi
      ;;
    dotnet)
      # A real xunit test project. A plain library would not do: `dotnet test`
      # on a project without the test SDK only restores and exits 0 even when
      # the code does not compile, so the failing fixture would "pass". The
      # packages come from nuget.org, which is why this stack only runs under
      # CI (or with BALLAST_MATRIX_ALLOW_NET=1, see needs_net).
      # The SDK is pinned to the 8.0 band that CI installs (setup-dotnet 8.0.x);
      # the runner images also carry newer SDKs.
      write_file "$dir/global.json" '{"sdk":{"version":"8.0.100","rollForward":"latestFeature"}}
'
      # Target the framework of the SDK that global.json selects (its
      # targeting pack ships with it).
      local tfm
      tfm="net$(cd "$dir" && dotnet --version 2>/dev/null | cut -d. -f1).0"
      write_file "$dir/m.csproj" "<Project Sdk=\"Microsoft.NET.Sdk\">
  <PropertyGroup>
    <TargetFramework>$tfm</TargetFramework>
    <IsPackable>false</IsPackable>
  </PropertyGroup>
  <ItemGroup>
    <PackageReference Include=\"Microsoft.NET.Test.Sdk\" Version=\"17.11.1\" />
    <PackageReference Include=\"xunit\" Version=\"2.9.2\" />
    <PackageReference Include=\"xunit.runner.visualstudio\" Version=\"2.8.2\" />
  </ItemGroup>
</Project>
"
      if [ "$leg" = pass ]; then ass='Xunit.Assert.True(true);'; else ass='Xunit.Assert.True(false);'; fi
      write_file "$dir/MTests.cs" "public class MTests
{
    [Xunit.Fact]
    public void M() { $ass }
}
"
      ;;
    swift)
      write_file "$dir/Package.swift" '// swift-tools-version:5.5
import PackageDescription
let package = Package(
    name: "m",
    targets: [.target(name: "m"), .testTarget(name: "mTests", dependencies: ["m"])]
)
'
      mkdir -p "$dir/Sources/m" "$dir/Tests/mTests"
      write_file "$dir/Sources/m/m.swift" "public func m() {}
"
      if [ "$leg" = pass ]; then ass='XCTAssertTrue(true)'; else ass='XCTAssertTrue(false)'; fi
      write_file "$dir/Tests/mTests/mTests.swift" "import XCTest
@testable import m
final class mTests: XCTestCase {
    func testM() { $ass }
}
"
      ;;
    ruby)
      write_file "$dir/Gemfile" "source 'https://rubygems.org'
gem 'rspec'
"
      mkdir -p "$dir/spec"
      if [ "$leg" = pass ]; then ass='expect(true).to eq(true)'; else ass='expect(true).to eq(false)'; fi
      write_file "$dir/spec/m_spec.rb" "describe 'm' do
  it 'works' do
    $ass
  end
end
"
      ;;
  esac
}

# argv_run DIR: run the proposed command of DIR/.ballast/project.json
# directly (the oracle); returns its exit status. Words are read from the
# JSON array with accutil.
oracle_run() {
  local dir=$1 n i word words
  n="$("$ACCUTIL" json len "$dir/.ballast/project.json" verify.0.argv)"
  words=()
  i=0
  while [ "$i" -lt "$n" ]; do
    words+=("$("$ACCUTIL" json get "$dir/.ballast/project.json" "verify.0.argv.$i")")
    i=$((i + 1))
  done
  # The raw tool's output is kept (tail shown when the oracle disagrees) so a
  # failing CI run says why the toolchain cannot judge the fixture.
  (cd "$dir" && "${words[@]}" >"$ORACLE_LOG" 2>&1)
}

ORACLE_LOG="$TMP_ROOT/oracle.log"
oracle_tail() { tail -n 12 "$ORACLE_LOG" 2>/dev/null | cut -c1-200 || true; }

for stack in $STACKS; do
  section "real: $stack"
  name="$stack (real)"
  want="$(expected_argv "$stack")"
  tool="$(printf '%s' "$want" | sed 's/^\["\([^"]*\)".*/\1/')"

  if is_ci && ! assigned_here "$stack"; then
    skip "$name" "not assigned to $OSKEY in the CI plan (Linux: all but swift, macOS: swift, Windows: dotnet)"
    continue
  fi
  if needs_net "$stack" && ! is_ci && [ "${BALLAST_MATRIX_ALLOW_NET:-0}" != 1 ]; then
    skip "$name" "downloads plugins or packages; local runs stay offline (set BALLAST_MATRIX_ALLOW_NET=1 to try)"
    continue
  fi
  case $tool in
    ./*) toolpath="" ;; # repo wrapper: the fixture has none, init proposes the plain tool below
    *) toolpath="$(command -v "$tool" 2>/dev/null || true)" ;;
  esac
  # Without the wrapper files init proposes the plain tool for maven/gradle.
  case $stack in
    maven) tool=mvn; want='["mvn","-q","test"]'; toolpath="$(command -v mvn 2>/dev/null || true)" ;;
    gradle) tool=gradle; want='["gradle","test"]'; toolpath="$(command -v gradle 2>/dev/null || true)" ;;
    ruby) want='["bundle","exec","rspec"]' ;;
  esac
  if [ -z "$toolpath" ]; then
    skip_or_fail "$name" "toolchain \"$tool\" not found on PATH"
    continue
  fi

  PASS_DIR="$SB_WORK/real-$stack-pass"
  FAIL_DIR="$SB_WORK/real-$stack-fail"
  new_repo "$PASS_DIR"
  new_repo "$FAIL_DIR"
  fixture "$stack" pass "$PASS_DIR"
  fixture "$stack" fail "$FAIL_DIR"
  for d in "$PASS_DIR" "$FAIL_DIR"; do
    gitc "$d" add -A
    gitc "$d" commit -q -m fixture
  done
  init_project "$PASS_DIR" "$want" "$name pass"
  init_project "$FAIL_DIR" "$want" "$name fail"
  for d in "$PASS_DIR" "$FAIL_DIR"; do
    gitc "$d" add -A
    gitc "$d" commit -q -m project
  done

  # The oracle: the raw tool must agree with the fixture's intent here,
  # otherwise this machine cannot judge ballast against it.
  if ! oracle_run "$PASS_DIR"; then
    skip_or_fail "$name" "the toolchain is present but cannot run the passing fixture here (missing test framework or offline?)
$(oracle_tail)"
    continue
  fi
  if oracle_run "$FAIL_DIR"; then
    skip_or_fail "$name" "the failing fixture unexpectedly passes with the raw tool
$(oracle_tail)"
    continue
  fi
  ok "$name: the raw tool passes the success fixture and fails the failure fixture"
  pass_leg "$name" "$PASS_DIR"
  fail_leg "$name" "$FAIL_DIR"
done

finish
