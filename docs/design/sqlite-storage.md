# 数据与产物

SQLite 是运行期持久元数据的唯一来源。附件内容保留为文件，诊断日志独立写文本；JSON 仅用于外部协议、CLI 输入输出、测试夹具及显式旧数据导入，不用于运行期状态双写或回退。

## 实例目录

```text
~/.codex-link-clawbot/
  data/
    clawbot.db                     # 配置、凭据、会话意图、请求、结果和回执
    clawbot.db-wal / clawbot.db-shm # SQLite 按需创建的辅助文件
    artifacts/requests/<id>/
      input/                       # 已下载的入站附件
      output/                      # 已登记的最终产物
  tmp/
    render/                        # HTML、Chromium profile、阅读图片
    voice/                         # 语音合成与转码中间文件
    download/                      # 部署候选下载
  logs/service.log                 # 另有轮转归档
  backups/                        # 离线导入备份、部署回执及快照
  control.sock                    # 本机运行控制
  .state.lock                     # 运行与离线操作互斥
```

工作空间是独立的项目目录，不是产物库；未配置时默认工作空间位于状态根下的 `workspace/`，同样不属于业务备份。构建程序位于仓库根目录，Air 中间文件在仓库 `tmp/`；没有统一的仓库 `output/` 发布层级。

目录为 `0700`，数据库及附件为 `0600`，受管路径拒绝符号链接。完整路径约定由 [Layout](../../internal/platform/storage/layout.go)维护。

## 数据职责

| 数据 | 主要表 |
| --- | --- |
| 配置和可选能力 | `settings`、`workspaces`、`codex_env`、`voice_providers` |
| 微信凭据、同步游标及批次去重 | `credentials`、`sync_cursors`、`sync_receipts` |
| 目标意图和各工作空间选择 | `target_owners`、`target_intents`、`target_selections` |
| 偏好、入口锁与异常待办通知 | `preferences`、`remote_locks`、`notices` |
| 草稿、引用与菜单回执 | `drafts`、`attachment_refs`、`draft_receipts`、`menu_receipts` |
| 请求、原始输入、完成检查点 | `requests`、`request_inputs`、`completions` |
| 冻结回答、产物、外部图片与交付 | `results`、`artifacts`、`result_urls`、`delivery_receipts` |
| 忙碌拒绝和主动清理后的去重 | `rejected_sources`、`cleared_sources` |
| 导入及部署审计 | `import_files`、`import_sources`、`snapshot_entries`、`deployment_receipts` |

集合按关系行保存，不把整份 JSON 放入一个状态列。完整表定义见 [schema.sql](../../internal/platform/storage/schema.sql)。外部图片 URL 只是引用，不保证其源站留存，也不算本地文件归档。

## 提交与读取

采用纯 Go SQLite 驱动、WAL、外键、FULL 同步和有界 busy timeout。事务内使用同一连接；请求创建与输入、完成事实与检查点、冻结结果与产物、首次交付回执与终态分别原子提交。文件写入私有目录并同步后才登记，不能用数据库记录代替真实文件完整性。

每个实例只有一个运行期请求存储和目标存储。运行期根据已提交的内存基线生成关系行增量，失败时不推进状态；阶段更新不重写正文，重投只更新回执。不要直接在运行中用 SQL 修改业务表。

读取元数据、执行和下载有不同边界：

- 列表批量读取回执，产物分页在 SQL 中计数与取页，不加载全部文件或正文。
- 查看原文和结果检查所有者、期限及结构，不读取附件内容。
- 执行与重试必须校验全部输入附件，实际投递必须校验完整结果文件。
- 下载只校验所选的登记文件：类型、路径、大小、文件身份及 SHA-256，哈希读取有上限。返回已回卷的同一文件句柄。

因此，一个文件损坏不会遮蔽有效文字或阻断其他完好文件；但清单可见也不能证明它可发送。

## 留存与容量

| 数据 | 到期规则 | 限制 |
| --- | --- | --- |
| 附件草稿 | 最后补充后 30 分钟 | 每绑定 4 图、8 文件引用、1 MiB 说明 |
| 已提交输入及上下文 | 请求终态后 24 小时 | 全实例有效输入 500 MiB |
| 冻结回答与产物 | 执行完成起 7 天 | 全实例有效结果 1 GiB |
| 请求历史及主动清理回执 | 原请求结束起 30 天 | 每绑定最多 1000 条请求记录 |

单图最多 20 MiB，单文件最多 50 MiB，每次输入附件合计最多 100 MiB；结果最多 8 个本地文件、合计 100 MiB，回答最多 5 MiB。新工作预留最多 105 MiB 结果空间，待下载输入预留 100 MiB，完成后按实际大小结算。容量是业务有效载荷，不包括数据库开销、项目文件、日志和备份。

后台每分钟清理到期数据，启动及接收路径也会检查。达到限额拒绝新增，不提前删除未到期成果。输入清理不删除有效结果，恢复归档和重投不延长结果期限。

网页可以确认清理已结束请求，删除输入、结果和列表记录，只保留防止旧来源再次执行的最小回执。不删除 Codex 在用户项目中直接产生的文件。

## 产物命令

```bash
codex-link-clawbot artifacts list --limit 100
codex-link-clawbot artifacts verify
codex-link-clawbot artifacts prune
```

`list` 按时间倒序，数量范围 1–8000，缺省 100；`verify` 校验有效产物。两者可加 `--owner` 筛选，CLI 未指定时覆盖所有者，不等同网页仅当前绑定的范围。

`prune` 必须停服，只按留存期清理整个实例，不接受 `--owner`，不提前删除有效成果。三者支持 `--root` 指定状态根，不能因此让另一实例并行运行。命令实现见 [artifacts.go](../../internal/cli/artifacts.go)。

## 临时文件与备份

渲染和语音文件使用后删除，异常残留在下次启动、取得运行锁后清理。部署下载在部署命令结束时清理，不在服务启动时删除，避免影响进行中的升级。异常下载残留须确认没有部署后再处理。

一致性数据库备份使用 `VACUUM INTO`；运行中不能只复制主数据库而忽略 WAL。离线 JSON 备份保留原格式，导入成功后不自动过期。事务部署成功后会删除该次敏感业务快照，保留部署回执；不应把部署快照当成长期灾备。

快照只接管明确的业务状态路径，不回滚用户项目、未知根文件、日志或既有备份。迁移和部署的操作步骤、失败恢复见[离线迁移](../operations/migration.md)与[部署](../operations/deployment.md)。
