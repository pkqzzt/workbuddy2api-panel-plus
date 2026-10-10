# 上游同步

本仓库（[`pkqzzt/workbuddy2api-panel-plus`](https://github.com/pkqzzt/workbuddy2api-panel-plus)）是
[`linguo2625469/workbuddy2api-panel`](https://github.com/linguo2625469/workbuddy2api-panel) 的增强分支：
在它的基础上并入了三个 PR，并叠了一组自有功能。上游继续演进时按本文同步。

## 一条命令

```bash
scripts/sync-upstream.sh           # 检查 + 合并（有冲突会停下并给出指引）
scripts/sync-upstream.sh --check   # 只报告有没有更新，不做任何改动
scripts/sync-upstream.sh --push    # 合并并自检通过后，顺带推送到 origin
```

脚本会：拉取上游 → 报告新提交与上游待处理的 PR → **用 `git merge-tree` 预演会不会冲突** →
合并 → 构建自检（本机有 Go 时）→ 可选推送。工作区不干净时会直接拒绝执行。

## 分叉结构

```
upstream/main ──●──●──●                （上游继续演进）
                \
                 ● 三个 PR 的合并（#138 积分历史 / #143 手机端适配 / #148 空名 tool_call）
                 ● 多 API Key + 逐 Key 用量台账
                 ● 国内构建与面板缓存修复
                 ● 本仓库 main ← 推送至 origin
```

同步就是 `git merge upstream/main`：上游新提交被并入，我们的提交保留在自己的位置上。

## 自有改动清单（冲突时的判断依据）

冲突不是"谁对谁错"，而是"两边都改了同一处"。原则只有一条：**自有功能一律保留，同时吸收上游改动的非重叠部分**。

### 完全由本仓库新增的文件（冲突时直接保留我们的）

| 文件 | 内容 |
|---|---|
| `internal/httpauth/keyring.go` | 多 Key 匹配表（模型白名单 / 出口绑定） |
| `internal/keyusage/keyusage.go` | 逐 Key 用量台账 |
| `internal/server/keyring_ctx.go` | 命中密钥的请求上下文传递 |
| `internal/panel/apikeys.go` | `GET /panel/api/key_usage` 接口 |

### 与上游共改、需要融合的文件

| 文件 | 我们的改动 | 冲突时怎么办 |
|---|---|---|
| `internal/server/handler.go` | `withAuth` 多 Key 分支；`chatCompletions` 里的白名单 / realm 绑定 / 用量 defer | 上游的管线改动与我们的三段并存；三段若被冲散，按 git 历史（`git log -p ea9566a -- <file>`）补回 |
| `cmd/server/config.go` | `APIKeyConfig` 类型、`APIKeys` 字段、`normalizeAPIKeys` | 保留我们的结构体与校验，吸收上游新增的其他配置字段 |
| `cmd/server/main.go` | `buildKeyring`/`keyUsage` 装配、`livecfg` 注入、`appVersion` | 装配保留；版本号保持本仓库的（当前 `1.14.0-panel`），不要退回上游的 |
| `internal/livecfg/livecfg.go` | `Snapshot.Keyring` 字段 | 只加不减：上游新增字段照收，`Keyring` 保留 |
| `internal/panel/panel.go` | `key_usage` 路由、`Config.Keyring`/`KeyUsage` | 与上游新增路由并存 |
| `internal/panel/index.go` | `writeAsset`（ETag + no-cache） | 上游若重写这几个 handler，把 ETag 逻辑套回去（没有它浏览器会缓存旧前端） |
| `internal/panel/index.html` | 「API Key 用量」区块 | 与上游新增区块并存 |
| `internal/panel/app.js` | `loadKeyUsage` / `renderKeyUsage`、`go()` 的 usage 分支 | 并存 |
| `internal/panel/mobile.css` | `#kuBody` 整行规则 | 并存 |
| `Dockerfile` | `ARG GOPROXY` / `ARG APK_MIRROR` | **必须保留**——去掉之后国内构建会卡在 `go mod download` |
| `docker-compose.yml` | `build.args` 透传上述两个变量 | 保留，可与上游新增字段合并 |
| `.github/workflows/docker-ghcr.yml` | 境外 runner 的 `build-args`（走官方 Go 代理） | 保留 |
| `config.example.json` | `"api_keys": []` | 保留 |
| `README.md` | 衍生说明、升级章节、国内镜像小节、多 API Key 章节 | 与上游同段落融合 |

### 上游 PR 带进来的文件

`#138` / `#143` / `#148` 的内容已在本仓库。若上游之后也合并了同样的 PR，git 通常能认出相同提交而不冲突；万一上游做了改写（squash / 追加修改），按"功能等价"取舍即可。

## 同步之后

1. `go build ./... && go vet ./... && go test ./... -count=1`（脚本里有 Go 时会自动跑）
2. 用了 Docker 的话：`docker compose up -d --build`（注意 Dockerfile 的 goproxy 修复还在）
3. 推送后在 GitHub 上确认 CI（`go-binaries` + `docker-ghcr`）通过，镜像 tag 会带新的短 sha

## 上游的新 PR

本仓库起步靠的就是并入上游 PR，所以上游的 PR 也是"更新"的来源：

```bash
# 列出上游待合并的 PR（脚本在检测到更新时也会列）
gh pr list --repo linguo2625469/workbuddy2api-panel --state open

# 把某个 PR 的分支取到本地并合入
git fetch upstream 'refs/pull/<N>/head:refs/remotes/upstream/pr/<N>'
git merge upstream/pr/<N> --no-edit -m "Merge PR #<N>"
```

并入前先看 PR 描述里的验证说明与改动文件；若它碰了上表里"我们改过的文件"，
按同一套原则融合，别直接覆盖。
