#!/usr/bin/env bash
source /m0b/lib.sh
# 在 T1 模板上(仅测试用)追加:allowWrite=<home>、/srv(使 denyWrite 成为 Bash 写入的唯一屏障)、三个 agent 可写的目录
# 分别用单斜杠、双斜杠、绝对路径/glob 形式保护。目录均属 agent,所以失败只可能来自规则。
cred_world '(.sandbox.filesystem.allowWrite = ["/home/agent","/srv"])
  | (.sandbox.filesystem.denyWrite += ["/srv/abs","/srv/glob/secret*.txt"])
  | (.permissions.deny += ["Edit(/srv/single/**)","Edit(//srv/double/**)","Write(//srv/wonly/**)"])'
P=/home/agent
mkdir -p /srv/abs /srv/glob /srv/single /srv/double /srv/wonly /srv/ctrl $P/proj/.ballast
printf 'orig' > /srv/single/e; printf 'orig' > /srv/double/e; printf 'orig' > /srv/ctrl/e
chown -R agent:agent /srv $P/proj
atts="$(jq -s . <<<"$(
  attempt_bash tilde-bash $P/.ballast/evidence/t
  attempt_bash relative-bash $P/proj/.ballast/rel
  attempt_bash abs-bash /srv/abs/x
  attempt_bash glob-bash /srv/glob/secret1.txt
  attempt_bash glob-control /srv/glob/other.txt
  attempt_bash allowwrite-control $P/outside/ok
  attempt_write tilde-write $P/.ballast/evidence/tw
  attempt_write relative-write $P/proj/.ballast/rw
  attempt_write single-slash-write /srv/single/x
  attempt_write double-slash-write /srv/double/x
  attempt_edit single-slash-edit /srv/single/e
  attempt_edit double-slash-edit /srv/double/e
  attempt_write write-family-rule-only /srv/wonly/x
  attempt_write control-write $P/outside/ok.txt
  attempt_edit control-edit /srv/ctrl/e
)")"
attempts_run "$atts" --permission-mode default
obs="T1 模板(仅测试)另加 allowWrite=[<home>,/srv]、denyWrite=[/srv/abs,/srv/glob/secret*.txt]、permissions.deny=[Edit(/srv/single/**)(单斜杠绝对路径),Edit(//srv/double/**)(双斜杠),Write(//srv/wonly/**)(只有 Write 族规则)]。结果:$ATT_RESULT(发出 $ATT_STEPS 个工具调用)。解读:tilde-bash/relative-bash/abs-bash/glob-bash=blocked 表示 denyWrite 的 ~/、./、绝对路径、glob 形式生效(glob-control 与 allowwrite-control 必须是 written,证明 denyWrite 是唯一屏障);tilde-write/relative-write 来自模板里的 Edit(~/…)、Edit(./…) 规则;single-slash-write 与 single-slash-edit=written 表示单斜杠开头的 permissions.deny 条目不按文件系统绝对路径解释(ManagedForms 里 org 规则目录、二进制目录、绝对路径 protected_extra 的 Edit/Write/NotebookEdit 条目应改为 // 形式);write-family-rule-only=written 表示只写 Write(…) 规则挡不住 Write 工具(Edit 族规则才是 Edit 与 Write 工具共同的屏障)。"
ok=1
b=0; for n in tilde-bash relative-bash abs-bash glob-bash tilde-write relative-write single-slash-write double-slash-write single-slash-edit double-slash-edit; do [ "$(att $n)" = blocked ] && b=$((b+1)); done
[ "$b" = 10 ] && [ "$(att glob-control)" = written ] && [ "$(att allowwrite-control)" = written ] && [ "$(att control-write)" = written ] && [ "$(att control-edit)" = written ] && ok=0
CRED_CONTROLS="glob-control allowwrite-control control-write control-edit"
cred_finish "$ok" "$obs"
