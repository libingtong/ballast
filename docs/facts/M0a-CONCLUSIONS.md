# M0a 取证结论

- 日期:2026-10-16;Claude Code 2.1.300;macOS(Darwin 25.5.0)。
- 方法:`scripts/probe/run-m0a.sh`。每个用例跑一次真实的 `claude -p`(模型 haiku,并行用例用 sonnet),只加载 project/local 设置,探针 hook 经 `--settings` 注入,用 `--allowedTools` 限定可执行的命令。没有改动本机用户设置。
- 原始 payload 已脱敏,存放在 `payloads/`;逐条结果见 `cases.json`。

## 对 A 区的结论

| 条目 | 结论 | 依据 |
|---|---|---|
| A1 公共字段 | 确认是有条件出现的字段。`SessionStart(startup)` 没有 `prompt_id/permission_mode/effort`;本次所有样本都没有 `scratchpad_dir`;`agent_*` 只在子 agent 内出现;`effort` 是对象 `{"level": ...}`,不是字符串 | common-fields |
| A3 SessionStart(compact) | 确认。`/compact` 依次触发 PreCompact(manual)、内部 agent 的 SubagentStop、SessionStart(source=compact)、PostCompact | compact |
| A7 退出码与 JSON | **确认审核意见 R05**:hook 以 exit 1 退出,但 stdout 是合法 deny JSON 时,工具**仍被拦截**。异常放行只能靠"stdout 保持为空" | exit1-json-deny |
| A8 tool_response | 成功的 Bash 返回 `stdout, stderr, interrupted, isImage, noOutputExpected`,另有可选的 `returnCodeInterpretation`;**没有退出码** | bash-ok, grep-exit1 |
| A9 失败路由 | 普通非 0 退出走 PostToolUseFailure,`error` 首行为 `Exit code N`,stdout/stderr 拼接在后。grep 无匹配(exit 1)走 **PostToolUse**,带 `returnCodeInterpretation:"No matches found"`。工具调用被 Claude Code 自身或 hook 拦下时,PostToolUse 和 PostToolUseFailure **都不触发** | bash-exit1, grep-exit1, bash-startfail |
| A10 Stop 上限 | 运行期确认:连续继续 8 次后,第 9 次 block 被覆盖,会话结束;中途调用工具会让计数重置 | stop-cap |
| A21 并行 PostToolUse | **本次未复现并发**。同一条消息里的 3 个 Read 是串行执行的。文档说会并发,所以设计**保留**跨进程锁 | parallel-posttooluse |
| A22 子 agent | 确认。子 agent 内的工具事件带 `agent_id/agent_type`;子 agent 结束时触发 SubagentStop,而不是 Stop。**压缩用的内部 agent 也会触发 SubagentStop,它的 `agent_type` 是空字符串** | subagent, compact |
| A24 updatedInput | 确认**不带 `permissionDecision` 也能改写输入**;带 allow 的对照组同样生效 | updatedinput-no-allow / -allow |

## 对设计的影响(M1 必须遵守)

1. **输出协议(B7)**:异常路径必须让 stdout 保持为空。不得先输出决策再做可能失败的操作。
2. **Stop 族的 warn**:映射为 `systemMessage`,实测不会让会话继续。可以保留 Stop/SubagentStop 的 warn 档,**不要**用 `additionalContext`。
3. **SubagentStop**:默认忽略 `agent_type` 为空的内部 agent(例如压缩 agent),避免误判;规则可以按 `agent_type` 过滤。
4. **PostToolUseFailure**:只作为"弱证据"和 `verify_failed_since_read` 的辅助信号;解析 `error` 首行 `Exit code N` 时要容错。
5. **被拦截的工具调用**不会产生 Post* 事件。状态机不能假设每个 PreToolUse 都有对应的 Post 事件。
6. **`effort` 字段**的类型是对象。解析时所有字段都按可选处理,类型不匹配时忽略该字段,不能报错。
7. `updatedInput` 可用,但 v0.3 已不依赖它。日后若要做 T1 前台链路,可以重新评估。

## 仍未核验

- 托管层行为(A13/A14/A15/A20)属于 M0b(P2)。
- PostToolUse 在其他工具或更多并发场景下是否真的并发。
