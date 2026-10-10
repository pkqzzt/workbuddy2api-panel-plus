#!/usr/bin/env bash
#
# sync-upstream.sh —— 把上游更新合并进本仓库（pkqzzt/workbuddy2api-panel-plus）。
#
# 本仓库是 linguo2625469/workbuddy2api-panel 的增强分支：在它之上叠了三个 PR 的合并
# 与自有功能（多 API Key、逐 Key 用量台账、国内构建修复、面板资源缓存修复等，
# 逐文件清单见 docs/upstream-sync.md）。上游继续演进时用本脚本并入。
#
# 用法：
#   scripts/sync-upstream.sh           # 检查 + 合并（有冲突会停下并给出指引）
#   scripts/sync-upstream.sh --check   # 只报告有没有更新，不做任何改动
#   scripts/sync-upstream.sh --push    # 合并并自检通过后，顺带推送到 origin
#
set -euo pipefail

CHECK_ONLY=0
PUSH=0
for arg in "$@"; do
  case "$arg" in
    --check) CHECK_ONLY=1 ;;
    --push)  PUSH=1 ;;
    -h|--help) sed -n '2,13p' "$0"; exit 0 ;;
    *) echo "未知参数: $arg（支持 --check / --push）" >&2; exit 2 ;;
  esac
done

cd "$(dirname "$0")/.."
UPSTREAM_URL="https://github.com/linguo2625469/workbuddy2api-panel.git"
BRANCH="main"

# 1) upstream remote 必须存在（首次同步时自动补上）
if ! git remote get-url upstream >/dev/null 2>&1; then
  echo "→ 未配置 upstream remote，添加: $UPSTREAM_URL"
  git remote add upstream "$UPSTREAM_URL"
fi

# 2) 工作区必须干净：合并期间混进未提交改动最难排查
if [ -n "$(git status --porcelain)" ]; then
  echo "✗ 工作区有未提交改动，先 commit 或 stash 再同步：" >&2
  git status --short >&2
  exit 1
fi

# 3) 拉取上游
echo "→ 拉取上游 ..."
git fetch upstream --prune --quiet
BEHIND=$(git rev-list --count "HEAD..upstream/$BRANCH")
if [ "$BEHIND" -eq 0 ]; then
  echo "✓ 已是最新：HEAD 已包含 upstream/$BRANCH 的全部提交"
  exit 0
fi

echo "→ 上游有 $BEHIND 个新提交："
git log --oneline --no-decorate "HEAD..upstream/$BRANCH"
echo

# 上游的新 PR 也是潜在的"更新"（本仓库当初就是靠并入 PR 起步的）
if command -v gh >/dev/null 2>&1; then
  echo "→ 上游待处理的 PR（如需并入，用 git fetch upstream 'refs/pull/<N>/head' 拉取后单独合并）："
  gh pr list --repo linguo2625469/workbuddy2api-panel --state open --limit 10 2>/dev/null \
    | sed 's/^/    /' || echo "    （查询失败，跳过）"
  echo
fi

if [ "$CHECK_ONLY" -eq 1 ]; then
  echo "（--check 模式：仅报告，未做任何改动）"
  exit 0
fi

# 4) 合并预演：先算出会不会冲突（git >= 2.38 支持 --write-tree），
#    免得合到一半才发现要人工处理。
MT_OUT=$(mktemp)
if git merge-tree --write-tree HEAD "upstream/$BRANCH" >"$MT_OUT" 2>&1; then
  echo "→ 预演：可自动合并，无冲突"
else
  echo "⚠ 预演发现冲突，涉及文件："
  grep -E 'CONFLICT' "$MT_OUT" | sed 's/^/    /' || sed 's/^/    /' "$MT_OUT"
  echo
  echo "  这些文件含本仓库的自有改动，保留策略见 docs/upstream-sync.md 的逐文件清单。"
  echo "  下面照常合并，冲突会留在工作区等你处理。"
  echo
fi
rm -f "$MT_OUT"

# 5) 合并
echo "→ 合并 upstream/$BRANCH ..."
if git merge "upstream/$BRANCH" --no-edit -m "Merge upstream/$BRANCH into $BRANCH"; then
  echo "✓ 合并完成（无冲突）"
else
  echo
  echo "⚠ 有冲突，需要手工解决："
  git diff --name-only --diff-filter=U | sed 's/^/    /'
  echo
  echo "  处理步骤："
  echo "    1. 编辑上面列出的文件，删掉 <<<<<<< / ======= / >>>>>>> 三个标记行"
  echo "       保留策略见 docs/upstream-sync.md（自有功能一律保留，上游改动吸收非重叠部分）"
  echo "    2. git add <文件>"
  echo "    3. go build ./... && go test ./... -count=1"
  echo "    4. git commit --no-edit          # 完成这次合并"
  echo "    5. git push origin $BRANCH"
  exit 1
fi

# 6) 合并后自检（本机没装 Go 就跳过，用 docker compose build 验证也一样）
if command -v go >/dev/null 2>&1; then
  echo "→ 自检：go build + 全量测试"
  go build ./... 2>&1 | sed 's/^/    /'
  go test ./... -count=1 2>&1 | tail -20 | sed 's/^/    /'
else
  echo "→ 自检：本机未安装 Go，跳过（可改用 docker compose build 验证）"
fi

# 7) 可选推送
if [ "$PUSH" -eq 1 ]; then
  echo "→ 推送到 origin ..."
  git push origin "$BRANCH"
  echo "✓ 已推送，远端 origin/$BRANCH 已更新"
else
  echo "→ 本地已完成；确认无误后推送：git push origin $BRANCH"
fi
