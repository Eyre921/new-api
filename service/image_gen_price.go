package service

import (
	"fmt"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
)

// imageGenFallbackKey 是图片生成的"全局兜底"ModelPrice key。
// 当具体图片模型（gpt-image-2、未来的 gpt-image-3 等）没有专属配置时，
// 退化查询这个 key —— 它在 defaultModelPrice 中默认值为 0.3 美元/次。
const imageGenFallbackKey = "image_generation"

// ResolveImageGenPrice 解析单次图片生成的固定价（美元）。
//
// 完整兜底链（从高到低，先命中先返回）：
//
//  1. ModelPrice["<model>:<quality>"]   —— 精细：按模型 + 质量分档
//     例：ModelPrice["gpt-image-2:high"] = 0.2
//
//  2. ModelPrice["<model>"]              —— 中等：按模型一刀切
//     例：ModelPrice["gpt-image-2"] = 0.05
//
//  3. ModelPrice["image_generation"]     —— 全局兜底（默认 0.3）
//     未来出 gpt-image-3 等新模型且管理员未配置时，按此价收，避免亏损。
//     管理员可在后台「固定价格」自由调整。
//
//  4. operation_setting.GetGPTImage1PriceOnceCall(quality, size)
//     —— 仅当 model 为空 / model 为 "gpt-image-1" 时使用，保留旧逻辑兼容。
//     注意：未知新模型不再走这一步，避免按老 gpt-image-1 价亏损。
//
//  5. 返回 0（同时记 warning 日志，触发限流避免日志洪水）
//     —— 极端情况：管理员清空了 ModelPrice["image_generation"]、
//     model 字段也丢失。此时不强行扣费、不阻断请求，但会告警让运维感知。
//
// 调用方：
//   - service/text_quota.go: Responses API 触发 image_generation tool call 时
//   - service/tool_billing.go: 预留的统一 tool 计费入口（当前未启用）
func ResolveImageGenPrice(model, quality, size string) float64 {
	// ① model + quality 复合 key
	if model != "" && quality != "" {
		if p, ok := ratio_setting.GetModelPrice(model+":"+quality, false); ok {
			return p
		}
	}

	// ② model 一刀切
	if model != "" {
		if p, ok := ratio_setting.GetModelPrice(model, false); ok {
			return p
		}
	}

	// ③ 全局兜底
	if p, ok := ratio_setting.GetModelPrice(imageGenFallbackKey, false); ok {
		return p
	}

	// ④ 仅对 gpt-image-1（或缺失 model 信息）回落到老硬编码
	if model == "" || model == "gpt-image-1" {
		return operation_setting.GetGPTImage1PriceOnceCall(quality, size)
	}

	// ⑤ 彻底找不到 —— 不扣费，但记 warning（带节流）
	logUnknownImageModel(model, quality, size)
	return 0
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
			"且未配置 ModelPrice[%q] 全局兜底；本次按 0 计费。"+
			"请到后台「倍率设置 → 固定价格」添加 %q 或 %q 的价格条目。",
		model, quality, size, imageGenFallbackKey, model, imageGenFallbackKey,
	))
}
