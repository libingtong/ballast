#!/usr/bin/env bash
# runner-bwrap-in-docker:不经 Claude,按 §3.4 Linux 行的 bwrap 参数在容器内跑沙箱集成用例的 Linux 条目。
# run.sh 先在不同 --security-opt 组合下跑 matrix.sh,结果在 /out/matrix/<组合>.txt。
source /m0b/lib.sh
reset_world
mkdir -p /run/m0b && chown agent:agent /run/m0b
as_agent env CASE_X=1 python3 -I /m0b/cases/runner-bwrap-in-docker/probe.py > /tmp/probe.json 2> /tmp/probe.err
prc=$?
mkdir -p "$OUT/payloads/results"
if [ ! -s /tmp/probe.json ]; then
  finish unverifiable none "探测脚本没有产出结果(rc=$prc):$(head -c 300 /tmp/probe.err | tr '\n' ' ')"
  exit 0
fi
jq . /tmp/probe.json > "$OUT/payloads/results/items.json"
# --security-opt 矩阵
mrows=""
for v in all3 no-seccomp no-apparmor no-systempaths default; do
  r="$(cat "$OUT/matrix/$v.txt" 2>/dev/null | head -1)"
  mrows="$mrows[$v => ${r:-未运行}]"
done
cp -R "$OUT/matrix" "$OUT/payloads/results/matrix" 2>/dev/null
fails() { jq -r --arg b "$1" '.builds[$b].items | map(select(.ok|not)) | map(.name) | join(",")' /tmp/probe.json; }
nfail() { jq -r --arg b "$1" '.builds[$b].items | map(select(.ok|not)) | length' /tmp/probe.json; }
nitems() { jq -r --arg b "$1" '.builds[$b].items | length' /tmp/probe.json; }
smoke_rc="$(jq -r .literal_smoke.rc /tmp/probe.json)"; smoke_msg="$(jq -r .literal_smoke.msg /tmp/probe.json)"
varrun="$(jq -r .var_run_is_symlink /tmp/probe.json)"
b1="字面(祖先 --bind、位于 W 之后,去掉 --tmpfs /var/run 以便启动):$(nfail literal-novarrun)/$(nitems literal-novarrun) 项不符合期望[$(fails literal-novarrun)]"
b2="祖先改 --ro-bind 但仍位于 W 之后:$(nfail ro-after)/$(nitems ro-after) 项不符合期望[$(fails ro-after)]"
b3="祖先 --ro-bind 且位于 W 的 --bind 之前、/var/run 为符号链接时不再单独 --tmpfs:$(nfail fixed)/$(nitems fixed) 项不符合期望[$(fails fixed)]"
net="$(jq -r '.builds.fixed | "deny 策略下外部回环监听器被挡=\(.net_deny_loopback_blocked);allow 策略下可达=\(.net_allow_loopback_reachable);allow 策略下 /run 下的 unix socket 被挡=\(.allow_run_socket_blocked);W 外自定义位置的 unix socket 仍可连接=\(.custom_location_socket_reachable)(残余风险,只记录);Spec.LoaderEnv 经 --setenv 到达测试=\(.setenv_ld_reaches_test)"' /tmp/probe.json)"
pty="$(jq -r '.builds.fixed.pty | "rc=\(.rc) 输出=\(.output)"' /tmp/probe.json)"
newm="$(jq -r '.builds.fixed.items | map(select(.expect=="post")) | map("\(.name):rc=\(.rc),PostCheck 发现新匹配=\(.new_mandatory|length)") | join(";")' /tmp/probe.json)"
obs="--security-opt 矩阵(bwrap --unshare-net --ro-bind / / ... 在 agent 身份下能否启动):$mrows。Debian 12 镜像中 /var/run 是指向 /run 的符号链接=$varrun;按 §3.4 字面加 --tmpfs /var/run 时 bwrap 退出码 $smoke_rc:$smoke_msg。逐项结果(25 个文件/IPC 条目,deny 项须失败且宿主无变化,allow 项须成功,post 项允许创建但须被 PostCheck 重扫发现):${b1};${b2};${b3}。新建类条目(MissingProtected):${newm}。$net。pty 控制终端内 bwrap --new-session 里 open /dev/tty 与 TIOCSTI:$pty。"
extra=""
if [ "$smoke_rc" != 0 ] || [ "$(nfail literal-novarrun)" != 0 ] || [ "$(nfail ro-after)" != 0 ]; then
  extra="$extra 【契约问题】§3.4 Linux 行按字面执行不成立:(1) /var/run 是指向 /run 的符号链接时,--tmpfs /var/run 使 bwrap 无法启动;(2) 祖先用 --bind 会让家目录整体可写(.bashrc、~/.ballast/evidence 等写入成功),祖先用 --ro-bind 但排在 W 的 --bind 之后会把 W 一并变成只读。最小修正:祖先用 --ro-bind 且先于 W 的 --bind;只在 /var/run 不是符号链接时才加 --tmpfs /var/run。按此修正后全部条目符合期望。"
fi
if [ "$(cat "$OUT/matrix/no-apparmor.txt" 2>/dev/null | head -1)" = OK ]; then
  extra="$extra 本环境(Docker Desktop 的 Linux 虚拟机,没有 AppArmor 强制)下只需要 seccomp 与 systempaths 两项 unconfined,apparmor=unconfined 不是必需的;启用 AppArmor 的宿主(例如 Ubuntu 24.04 CI,user namespace 受 AppArmor 限制)是否仍需要它【未核验】,在那之前按设计保留三项。"
fi
obs="$obs$extra"
need3=0
[ "$(cat "$OUT/matrix/all3.txt" 2>/dev/null | head -1)" = OK ] && need3=1
# 字面参数无法启动、或字面顺序下有条目不符合期望,即文档说法被推翻
if [ "$need3" = 1 ] && [ "$smoke_rc" = 0 ] && [ "$(nfail literal-novarrun)" = 0 ] && [ "$(nfail fixed)" = 0 ]; then
  finish pass none "$obs"
else
  finish fail none "$obs"
fi
