package service

import (
	"math"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/operation_setting"
)

// ToolCallUsage captures all tool call counts from a single request.
type ToolCallUsage struct {
	ModelName              string
	WebSearchCalls         int
	WebSearchToolName      string // "web_search_preview", "web_search", etc.
	FileSearchCalls        int
	ImageGenerationCall    bool
	ImageGenerationModel   string // 上游响应中 response.tools[].model（如 gpt-image-2）
	ImageGenerationQuality string
	ImageGenerationSize    string
}

// ToolCallItem represents a single billed tool usage line.
type ToolCallItem struct {
	Name       string  `json:"name"`
	CallCount  int     `json:"call_count"`
	PricePer1K float64 `json:"price_per_1k"`
	TotalPrice float64 `json:"total_price"`
	Quota      int     `json:"quota"`
}

// ToolCallResult holds the aggregated tool call billing for a request.
type ToolCallResult struct {
	TotalQuota int            `json:"total_quota"`
	Items      []ToolCallItem `json:"items,omitempty"`
}

// ComputeToolCallQuota calculates the total quota for all tool calls in a
// request. Tool prices are resolved via GetToolPriceForModel which supports
// model-prefix overrides. groupRatio is applied.
func ComputeToolCallQuota(usage ToolCallUsage, groupRatio float64) ToolCallResult {
	var items []ToolCallItem
	totalQuota := 0

	addItem := func(toolName string, count int) {
		if count <= 0 {
			return
		}
		pricePer1K := operation_setting.GetToolPriceForModel(toolName, usage.ModelName)
		if pricePer1K <= 0 {
			return
		}
		totalPrice := pricePer1K * float64(count) / 1000
		quota := int(math.Round(totalPrice * common.QuotaPerUnit * groupRatio))
		items = append(items, ToolCallItem{
			Name:       toolName,
			CallCount:  count,
			PricePer1K: pricePer1K,
			TotalPrice: totalPrice,
			Quota:      quota,
		})
		totalQuota += quota
	}

	if usage.WebSearchCalls > 0 && usage.WebSearchToolName != "" {
		addItem(usage.WebSearchToolName, usage.WebSearchCalls)
	}

	if usage.FileSearchCalls > 0 {
		addItem("file_search", usage.FileSearchCalls)
	}

	if usage.ImageGenerationCall {
		// 与 text_quota.go 的统一计费逻辑对齐（ModelPrice → ModelRatio → 兜底）。
		// 这里没有 image_gen 的 token 信息（ToolCallUsage 暂未携带），
		// 因此 by_token 分支不会命中；按次和兜底分支与 text_quota.go 完全一致。
		imageModel := usage.ImageGenerationModel
		if imageModel == "" {
			imageModel = usage.ModelName
		}
		billing := ComputeImageGenQuota(
			imageModel,
			usage.ImageGenerationQuality,
			usage.ImageGenerationSize,
			0, 0,
			groupRatio,
		)
		items = append(items, ToolCallItem{
			Name:       "image_generation",
			CallCount:  1,
			PricePer1K: billing.PerCallPrice,
			TotalPrice: billing.PerCallPrice,
			Quota:      billing.Quota,
		})
		totalQuota += billing.Quota
	}

	return ToolCallResult{
		TotalQuota: totalQuota,
		Items:      items,
	}
}
