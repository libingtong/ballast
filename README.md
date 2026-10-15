# Ballast(压舱石)

Claude Code 的行为策略引擎:持续注入关键规则、工具调用前强制检查、违规留痕审计。

- 声明式规则 DSL(≤12 条),不写代码即可新增拦截
- 四档动作 `deny / ask / warn / log`,新规则默认影子模式(log)
- 证据以 `ballast verify` 的退出码与内容快照为准,不解析测试输出
- 三层配置 org > repo > user
- 审计、统计、回放评测

> 状态:开发中。设计文档见 [`docs/ballast-plan.md`](docs/ballast-plan.md)(v0.3),审核记录见 [`docs/ballast-plan-review.md`](docs/ballast-plan-review.md)。
