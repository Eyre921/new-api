package service

import (
	"fmt"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"

	"github.com/shopspring/decimal"
)

// ImageGenBillingMode 标识本次图片生成 tool call 实际命中的计费分支，
// 用于日志展示与可观测性。
type ImageGenBillingMode string

const (
	ImageGenBillingPerCall          ImageGenBillingMode = "per_call"           // ① / ② 命中 ModelPrice
	ImageGenBillingByToken          ImageGenBillingMode = "by_token"           // ③ 命中 ModelRatio
	ImageGenBillingFallbackPerCall  ImageGenBillingMode = "fallback_per_call"  // ④ 命中 image_generation 全局兜底
	ImageGenBillingCompileTimeFloor ImageGenBillingMode = "compile_time_floor" // ⑤ 编译期 0.3 兜底
)

// ImageGenBillingResult 是 image_generation tool call 的扣费结果。
// Quota 字段是已应用 GroupRatio 和 QuotaPerUnit 之后的最终值（可直接累加到总扣费）。
// 其余字段用于日志记录与未来排错。
type ImageGenBillingResult struct {
	Quota int                 // 最终扣费 quota（含 group_ratio + QuotaPerUnit）
	Mode  ImageGenBillingMode // 命中的计费分支

	// per_call / fallback_per_call 模式专属：基础单价（USD/次）
	PerCallPrice float64

	// by_token 模式专属：参与计费的明细
	InputTokens     int
	OutputTokens    int
	ModelRatio      float64
	CompletionRatio float64

	// 通用：用于日志展示
	Model   string
	Quality string
	Size    string
}

// ComputeImageGenQuota 计算单次 image_generation tool call 的扣费 quota，
// 完整兜底链（先命中先返回，与 ModelPriceHelper 的全局语义对齐）：
//
//  1. ModelPrice["<model>:<quality>"]   按次（精细分档）
//  2. ModelPrice["<model>"]              按次（模型一刀切）
//  3. ModelRatio["<model>"]              按 token（仅当响应里也带了 image_gen tokens）
//     公式与 new-api 文本模型完全一致：
//       quota = (input + output × completionRatio) × modelRatio × groupRatio
//     —— 等价于"额外调用了一次该模型"，运维操作语义与配置普通文本模型相同。
//  4. ModelPrice["image_generation"]     全局兜底（按次，默认 0.3）
//  5. DefaultImageGenFallbackPrice       编译期兜底（永远不返回 0）
//
// 参数：
//   - model:        底层图片模型名（如 gpt-image-2）。Responses API 路径从 response.tools[].model
//     提取；直接 Image API 路径用 request.model。可能为 ""。
//   - quality/size: 上游返回的实际值（auto 已被解析）。用于复合 key 与日志。
//   - inputTokens / outputTokens: 上游统计的 image_gen token 数（来源
//     tool_usage.image_gen）。若为 0 则不可走 by_token 分支。
//   - groupRatio:   当前请求生效的分组倍率。
func ComputeImageGenQuota(model, quality, size string, inputTokens, outputTokens int, groupRatio float64) ImageGenBillingResult {
	res := ImageGenBillingResult{Model: model, Quality: quality, Size: size}

	// ① ModelPrice["<model>:<quality>"]
	if model != "" && quality != "" {
		if p, ok := ratio_setting.GetModelPrice(model+":"+quality, false); ok {
			res.Mode = ImageGenBillingPerCall
			res.PerCallPrice = p
			res.Quota = perCallQuota(p, groupRatio)
			return res
		}
	}

	// ② ModelPrice["<model>"]
	if model != "" {
		if p, ok := ratio_setting.GetModelPrice(model, false); ok {
			res.Mode = ImageGenBillingPerCall
			res.PerCallPrice = p
			res.Quota = perCallQuota(p, groupRatio)
			return res
		}
	}

	// ③ ModelRatio["<model>"] —— 按 token 模式，等价于"额外调用了一次该模型"。
	// 需要同时满足：模型名已知 + 上游响应里也带了 token 数（无 token 数则无法计算）。
	if model != "" && (inputTokens > 0 || outputTokens > 0) {
		if mr, ok, _ := ratio_setting.GetModelRatio(model); ok {
			cr := ratio_setting.GetCompletionRatio(model)
			res.Mode = ImageGenBillingByToken
			res.InputTokens = inputTokens
			res.OutputTokens = outputTokens
			res.ModelRatio = mr
			res.CompletionRatio = cr
			res.Quota = byTokenQuota(inputTokens, outputTokens, mr, cr, groupRatio)
			return res
		}
	}

	// ④ ModelPrice["image_generation"] 全局按次兜底
	if p, ok := ratio_setting.GetModelPrice(ratio_setting.ImageGenFallbackKey, false); ok {
		res.Mode = ImageGenBillingFallbackPerCall
		res.PerCallPrice = p
		res.Quota = perCallQuota(p, groupRatio)
		return res
	}

	// 老 gpt-image-1 硬编码路径：仅当 model 为空 / 显式 gpt-image-1，
	// 保留旧版本扣费行为，避免历史数据迁移阵痛。
	if model == "" || model == "gpt-image-1" {
		p := operation_setting.GetGPTImage1PriceOnceCall(quality, size)
		res.Mode = ImageGenBillingFallbackPerCall
		res.PerCallPrice = p
		res.Quota = perCallQuota(p, groupRatio)
		return res
	}

	// ⑤ 编译期兜底 —— 即使管理员主动清空了 image_generation 这一行，
	// 也不会按 0 扣费，永远保底 DefaultImageGenFallbackPrice。
	logUnknownImageModel(model, quality, size)
	res.Mode = ImageGenBillingCompileTimeFloor
	res.PerCallPrice = ratio_setting.DefaultImageGenFallbackPrice
	res.Quota = perCallQuota(ratio_setting.DefaultImageGenFallbackPrice, groupRatio)
	return res
}

// perCallQuota 按次扣费的标准换算：price × QuotaPerUnit × groupRatio，
// 与 [helper/price.go:120] 中 ModelPriceHelper 的语义保持一致。
func perCallQuota(price, groupRatio float64) int {
	q := decimal.NewFromFloat(price).
		Mul(decimal.NewFromFloat(common.QuotaPerUnit)).
		Mul(decimal.NewFromFloat(groupRatio))
	return int(q.Round(0).IntPart())
}

// byTokenQuota 按 token 扣费的换算，公式来自 service/text_quota.go 的标准文本模型分支：
//
//	quota = (input + output × completionRatio) × modelRatio × groupRatio
//
// 注意：不乘 QuotaPerUnit —— 这与 ModelRatio 的"quota per token (含 QuotaPerUnit)"
// 单位约定一致，与 new-api 全局文本模型计费完全对齐。
func byTokenQuota(input, output int, modelRatio, completionRatio, groupRatio float64) int {
	ratio := decimal.NewFromFloat(modelRatio).Mul(decimal.NewFromFloat(groupRatio))
	prompt := decimal.NewFromInt(int64(input))
	completion := decimal.NewFromInt(int64(output)).Mul(decimal.NewFromFloat(completionRatio))
	q := prompt.Add(completion).Mul(ratio)
	return int(q.Round(0).IntPart())
}

// FormatImageGenBillingLog 为 record consume log 的 content 字段生成可读说明。
// 复用 ImageGenBillingResult 里已经留好的字段，避免重复计算。
func FormatImageGenBillingLog(r ImageGenBillingResult) string {
	switch r.Mode {
	case ImageGenBillingByToken:
		return fmt.Sprintf(
			"Image Generation Call 花费 %d (按 token: model=%s in=%d out=%d ratio=%.4f completion=%.4f)",
			r.Quota, r.Model, r.InputTokens, r.OutputTokens, r.ModelRatio, r.CompletionRatio,
		)
	case ImageGenBillingPerCall, ImageGenBillingFallbackPerCall, ImageGenBillingCompileTimeFloor:
		return fmt.Sprintf(
			"Image Generation Call 花费 %d (按次: model=%s price=$%.4f mode=%s)",
			r.Quota, r.Model, r.PerCallPrice, r.Mode,
		)
	default:
		return fmt.Sprintf("Image Generation Call 花费 %d", r.Quota)
	}
}

// unknownImageModelLogMu 与 unknownImageModelLogAt 用于限流 warning 日志：
// 即使在高并发场景下，对同一未知模型也最多每 5 分钟打印一次，避免日志洪水。
var (
	unknownImageModelLogMu sync.Mutex
	unknownImageModelLogAt = map[string]time.Time{}
)

const unknownImageModelLogInterval = 5 * time.Minute

func logUnknownImageModel(model, quality, size string) {
	unknownImageModelLogMu.Lock()
	now := time.Now()
	last, seen := unknownImageModelLogAt[model]
	if seen && now.Sub(last) < unknownImageModelLogInterval {
		unknownImageModelLogMu.Unlock()
		return
	}
	unknownImageModelLogAt[model] = now
	unknownImageModelLogMu.Unlock()

	common.SysError(fmt.Sprintf(
		"[ImageGenPrice] 未知图片模型 %q (quality=%q size=%q)，"+
			"且 ModelPrice[%q] 被清空；本次按编译期兜底价 %.4f USD 计费。"+
			"请到后台「倍率设置 → 固定价格」补回 %q 或为 %q 配置 ModelPrice/ModelRatio。",
		model, quality, size,
		ratio_setting.ImageGenFallbackKey, ratio_setting.DefaultImageGenFallbackPrice,
		ratio_setting.ImageGenFallbackKey, model,
	))
}
