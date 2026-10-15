// Package bashparse (unit U2) turns a bash command string into a
// model.CommandList using mvdan.cc/sh/v3/syntax (LangBash). Plan B1
// "Bash 解析契约"; docs/dsl-semantics.md §6.
//
// Hot path: yes (≤ 2ms p50 for a 100KB command, M0c). Imports: model,
// ports, mvdan.cc/sh/v3/syntax, stdlib. No regexp compilation at package
// init (compile lazily or use hand-written scanners).
//
// Contract (each item is a test row in testdata/expect.json):
//   - Every *syntax.CallExpr becomes one CommandNode; words that are pure
//     literals/quoted literals give Word{Lit}; anything with expansions gives
//     Word{Dynamic:true}. Assignments-only statements produce no node.
//   - Wrapper stripping (repeated, in order of appearance): env [-i] [-u N]
//     [VAR=...]..., nice [-n N|-N], nohup, timeout [opts] DURATION, time [-p],
//     command [-p], exec [-a name], sudo [opts incl. -u/-g with values]
//     (sets Sudo). Stripped names are recorded in Wrappers.
//   - Loop: innermost enclosing for/while/until (Kind, N, Bounded, Depth).
//     `for x in a b c` ⇒ for_list N=3 Bounded=true; `while true`/`while :`/
//     `until false` ⇒ Bounded=false; others unknown.
//   - Async: statement has `&` (x.Background) — applies to the nodes of that
//     statement, including every node of a backgrounded pipeline/subshell.
//   - InSubst: node is inside $(..), `..`, <(..), >(..).
//   - Redirects and Writes: > >> &> &>> >| and N> to a file (not /dev/null,
//     not &N dup) ⇒ Writes{Via:"redirect"}; tee FILE..., cp/mv/install SRC..
//     DEST (last non-option arg, or -t DIR), rsync DEST, sed -i/-i.bak/--in-place
//     FILE..., rm FILE..., truncate FILE..., dd of=FILE, touch FILE..., ln
//     TARGET LINK (LINK). Dynamic targets ⇒ Dynamic:true.
//   - Reads (frozen; feeds read_this_session for clients without a Read
//     tool, e.g. Codex exec_command). Operands below are argv words after
//     wrapper stripping; "-" (stdin) is skipped; after "--" every word is an
//     operand; a dynamic operand ⇒ Reads{Dynamic:true}; Via = the command
//     name. Collected for nodes at every depth (pipelines, $(..), sh -c).
//   - `< FILE` input redirection on any node (not <<, <<<, <&N) ⇒
//     Reads{Via:"redirect"}.
//   - cat, tac, nl, wc, file, less, more: every non-option operand.
//   - head, tail: every non-option operand; -n/-c/--lines/--bytes take
//     the next word when not attached (-n20, --lines=20 attached); words
//     "+N"/"-N" (digits) are options.
//   - sed WITHOUT -i/--in-place: the script is the first operand unless
//     -e/--expression or -f/--file was given (each consumes the next word
//     when not attached); every remaining operand is a read. sed WITH -i
//     produces Writes only (no Reads).
//   - grep, egrep, fgrep, rg: the pattern is the first operand unless
//     -e/--regexp or -f/--file was given (consume a value; the -f file is
//     not recorded). Options consuming the next word when not attached:
//     grep -A -B -C -m -d -D --max-count --context --after-context
//     --before-context --include --exclude --exclude-dir --label
//     --binary-files --color; rg additionally -g --glob --iglob -t --type
//     -T --type-not -j --threads -M --max-columns -E --encoding --max-depth
//     --sort --sortr -r --replace --pre --type-add --max-filesize
//     --context-separator. Every remaining operand is a read (a directory
//     operand is recorded too; it never equals a file path in the read set).
//   - awk, gawk, mawk: the program is the first operand unless -f was
//     given; -F/-v/-f consume a value (attached or next word); operands of
//     the form NAME=value are assignments, not files; every remaining
//     operand is a read.
//   - Unlisted commands (python, node, find, xargs cat, ...) produce no
//     Reads — a missing read only makes read-before-edit stricter.
//   - sh/bash/zsh/dash -c '<literal>' is parsed recursively (Depth+1, ≤2);
//     deeper or dynamic ⇒ the outer node only. Inner parse failure discards
//     the inner nodes only and sets nothing else.
//   - Heredoc bodies are never parsed as commands. Comments are ignored.
//   - `eval ...` ⇒ node kept AND CommandList.Unknown=true (reason "eval").
//     `source`/`.` of a dynamic file ⇒ Unknown=true.
//   - git: global options -C <dir> (accumulates), -c k=v, --git-dir[=],
//     --work-tree[=], --no-pager, --no-optional-locks, -p/--paginate,
//     --bare, --literal-pathspecs etc. are consumed; Git.Sub is the first
//     non-option word; Git.Args are the rest.
//   - sleep: "300", "5m", "1h", "1.5d", several args summed (GNU), "infinity"
//     ⇒ +Inf with Known=true; any dynamic/unparsable arg ⇒ Known=false.
//   - Pipes: one PipeEdge per `|` / `|&` between the last node of the left
//     side and the first node of the right side.
//   - Parse error ⇒ (nil, error). Never panics (fuzz target FuzzParse).
//
// Acceptance (U2): testdata/expect.json all rows; plan B1 regression set
// (14 rows, mirrored in testdata/bash-regress.json by U11) parses into node
// lists whose sleep/async/loop/git fields make the explicit-deny test rule
// produce the expected outcome; 100KB command < 5ms in a benchmark.
package bashparse
