// keyring_ctx.go 把「本次请求命中的 API Key」从鉴权阶段传递到记账阶段。
//
// 鉴权在 withAuth（最外层中间件）完成，而逐 Key 用量归集发生在 chatCompletions
// 的出口统计里——中间隔着整个选号/轮转/流式管线，用一个 context 值传递比让管线
// 层层透传参数干净得多，也避免给 chatCompletions 增加一个"只有统计关心"的入参。
package server

import (
	"context"

	"github.com/linguo2625469/workbuddy2api-panel/internal/httpauth"
)

// keySpecCtxKey 请求上下文里承载「本次请求命中的 API Key 配置」的键。
// 用私有空结构体：不会与其他包的键冲突，也不占内存。
type keySpecCtxKey struct{}

// withKeySpec 把命中的 API Key 配置放进请求上下文（鉴权阶段写入）。
func withKeySpec(ctx context.Context, spec httpauth.KeySpec) context.Context {
	return context.WithValue(ctx, keySpecCtxKey{}, spec)
}

// keySpecFrom 取出当前请求命中的 API Key 配置。
// 未启用多 Key（Keyring 为 nil）且单密钥为空时返回 false，调用方据此跳过限制与统计。
func keySpecFrom(ctx context.Context) (httpauth.KeySpec, bool) {
	spec, ok := ctx.Value(keySpecCtxKey{}).(httpauth.KeySpec)
	return spec, ok
}
