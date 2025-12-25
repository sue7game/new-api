package relay

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/relay/channel"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/model_setting"
	"github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
)

const claudePassthroughURL = "https://ai.megallm.io/v1/messages"

func ClaudeHelper(c *gin.Context, info *relaycommon.RelayInfo) (newAPIError *types.NewAPIError) {

	info.InitChannelMeta(c)

	claudeReq, ok := info.Request.(*dto.ClaudeRequest)

	if !ok {
		return types.NewErrorWithStatusCode(fmt.Errorf("invalid request type, expected *dto.ClaudeRequest, got %T", info.Request), types.ErrorCodeInvalidRequest, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
	}

	if shouldClaudePassThrough(info) {
		return claudePassThrough(c, info, claudeReq)
	}

	request, err := common.DeepCopy(claudeReq)
	if err != nil {
		return types.NewError(fmt.Errorf("failed to copy request to ClaudeRequest: %w", err), types.ErrorCodeInvalidRequest, types.ErrOptionWithSkipRetry())
	}

	err = helper.ModelMappedHelper(c, info, request)
	if err != nil {
		return types.NewError(err, types.ErrorCodeChannelModelMappedError, types.ErrOptionWithSkipRetry())
	}

	adaptor := GetAdaptor(info.ApiType)
	if adaptor == nil {
		return types.NewError(fmt.Errorf("invalid api type: %d", info.ApiType), types.ErrorCodeInvalidApiType, types.ErrOptionWithSkipRetry())
	}
	adaptor.Init(info)

	if request.MaxTokens == 0 {
		request.MaxTokens = uint(model_setting.GetClaudeSettings().GetDefaultMaxTokens(request.Model))
	}

	if model_setting.GetClaudeSettings().ThinkingAdapterEnabled &&
		strings.HasSuffix(request.Model, "-thinking") {
		if request.Thinking == nil {
			// 因为BudgetTokens 必须大于1024
			if request.MaxTokens < 1280 {
				request.MaxTokens = 1280
			}

			// BudgetTokens 为 max_tokens 的 80%
			request.Thinking = &dto.Thinking{
				Type:         "enabled",
				BudgetTokens: common.GetPointer[int](int(float64(request.MaxTokens) * model_setting.GetClaudeSettings().ThinkingAdapterBudgetTokensPercentage)),
			}
			// TODO: 临时处理
			// https://docs.anthropic.com/en/docs/build-with-claude/extended-thinking#important-considerations-when-using-extended-thinking
			request.TopP = 0
			request.Temperature = common.GetPointer[float64](1.0)
		}
		if !model_setting.ShouldPreserveThinkingSuffix(info.OriginModelName) {
			request.Model = strings.TrimSuffix(request.Model, "-thinking")
		}
		info.UpstreamModelName = request.Model
	}

	if info.ChannelSetting.SystemPrompt != "" {
		if request.System == nil {
			request.SetStringSystem(info.ChannelSetting.SystemPrompt)
		} else if info.ChannelSetting.SystemPromptOverride {
			common.SetContextKey(c, constant.ContextKeySystemPromptOverride, true)
			if request.IsStringSystem() {
				existing := strings.TrimSpace(request.GetStringSystem())
				if existing == "" {
					request.SetStringSystem(info.ChannelSetting.SystemPrompt)
				} else {
					request.SetStringSystem(info.ChannelSetting.SystemPrompt + "\n" + existing)
				}
			} else {
				systemContents := request.ParseSystem()
				newSystem := dto.ClaudeMediaMessage{Type: dto.ContentTypeText}
				newSystem.SetText(info.ChannelSetting.SystemPrompt)
				if len(systemContents) == 0 {
					request.System = []dto.ClaudeMediaMessage{newSystem}
				} else {
					request.System = append([]dto.ClaudeMediaMessage{newSystem}, systemContents...)
				}
			}
		}
	}

	var requestBody io.Reader
	if model_setting.GetGlobalSettings().PassThroughRequestEnabled || info.ChannelSetting.PassThroughBodyEnabled {
		body, err := common.GetRequestBody(c)
		if err != nil {
			return types.NewErrorWithStatusCode(err, types.ErrorCodeReadRequestBodyFailed, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
		}
		requestBody = bytes.NewBuffer(body)
	} else {
		convertedRequest, err := adaptor.ConvertClaudeRequest(c, info, request)
		if err != nil {
			return types.NewError(err, types.ErrorCodeConvertRequestFailed, types.ErrOptionWithSkipRetry())
		}
		jsonData, err := common.Marshal(convertedRequest)
		if err != nil {
			return types.NewError(err, types.ErrorCodeConvertRequestFailed, types.ErrOptionWithSkipRetry())
		}

		// remove disabled fields for Claude API
		jsonData, err = relaycommon.RemoveDisabledFields(jsonData, info.ChannelOtherSettings)
		if err != nil {
			return types.NewError(err, types.ErrorCodeConvertRequestFailed, types.ErrOptionWithSkipRetry())
		}

		// apply param override
		if len(info.ParamOverride) > 0 {
			jsonData, err = relaycommon.ApplyParamOverride(jsonData, info.ParamOverride, relaycommon.BuildParamOverrideContext(info))
			if err != nil {
				return types.NewError(err, types.ErrorCodeChannelParamOverrideInvalid, types.ErrOptionWithSkipRetry())
			}
		}

		if common.DebugEnabled {
			println("requestBody: ", string(jsonData))
		}
		requestBody = bytes.NewBuffer(jsonData)
	}

	statusCodeMappingStr := c.GetString("status_code_mapping")
	var httpResp *http.Response
	resp, err := adaptor.DoRequest(c, info, requestBody)
	if err != nil {
		return types.NewOpenAIError(err, types.ErrorCodeDoRequestFailed, http.StatusInternalServerError)
	}

	if resp != nil {
		httpResp = resp.(*http.Response)
		info.IsStream = info.IsStream || strings.HasPrefix(httpResp.Header.Get("Content-Type"), "text/event-stream")
		if httpResp.StatusCode != http.StatusOK {
			newAPIError = service.RelayErrorHandler(c.Request.Context(), httpResp, false)
			// reset status code 重置状态码
			service.ResetStatusCode(newAPIError, statusCodeMappingStr)
			return newAPIError
		}
	}

	usage, newAPIError := adaptor.DoResponse(c, httpResp, info)
	//log.Printf("usage: %v", usage)
	if newAPIError != nil {
		// reset status code 重置状态码
		service.ResetStatusCode(newAPIError, statusCodeMappingStr)
		return newAPIError
	}

	service.PostClaudeConsumeQuota(c, info, usage.(*dto.Usage))
	return nil
}

func claudePassThrough(c *gin.Context, info *relaycommon.RelayInfo, request *dto.ClaudeRequest) (newAPIError *types.NewAPIError) {

	fmt.Println("===== Claude PassThrough claudePassThrough222 =====")

	// Map model to upstream variant while keeping raw body untouched.
	_ = helper.ModelMappedHelper(c, info, nil)

	rawBody, err := common.GetRequestBody(c)
	if err != nil {
		return types.NewErrorWithStatusCode(err, types.ErrorCodeReadRequestBodyFailed, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
	}
	if request != nil && request.Stream {
		info.IsStream = true
	}

	//logger.LogInfo(c, fmt.Sprintf("claude passthrough start: channel=%d stream=%t url=%s origin_model=%s upstream_model=%s", info.ChannelId, info.IsStream, claudePassthroughURL, info.OriginModelName, info.UpstreamModelName))

	if info.UpstreamModelName != "" && info.UpstreamModelName != info.OriginModelName {
		var payload map[string]any
		if err := common.Unmarshal(rawBody, &payload); err == nil {
			if payload["model"] != nil {
				payload["model"] = info.UpstreamModelName
				if patched, err := common.Marshal(payload); err == nil {
					rawBody = patched
					//logger.LogInfo(c, fmt.Sprintf("claude passthrough model patched to upstream_model=%s", info.UpstreamModelName))
				}
			}
		}
	}

	req, err := http.NewRequest(c.Request.Method, claudePassthroughURL, bytes.NewBuffer(rawBody))
	if err != nil {
		return types.NewErrorWithStatusCode(err, types.ErrorCodeDoRequestFailed, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
	}
	req.Header = buildClaudePassThroughHeaders(c, info)

	resp, err := channel.DoRequest(c, req, info)
	if err != nil {
		return types.NewOpenAIError(err, types.ErrorCodeDoRequestFailed, http.StatusInternalServerError)
	}
	if resp == nil {
		return types.NewError(fmt.Errorf("empty upstream response"), types.ErrorCodeDoRequestFailed, types.ErrOptionWithSkipRetry())
	}

	statusCodeMappingStr := c.GetString("status_code_mapping")
	info.IsStream = info.IsStream || strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream")
	//logger.LogInfo(c, fmt.Sprintf("claude passthrough upstream status: %d stream=%t", resp.StatusCode, info.IsStream))

	if resp.StatusCode != http.StatusOK {
		errorBody, _ := io.ReadAll(resp.Body)
		info.SetFirstResponseTime()
		service.IOCopyBytesGracefully(c, resp, errorBody)
		service.CloseResponseBodyGracefully(resp)
		return nil
	}

	var usage *dto.Usage
	if info.IsStream {
		usage, newAPIError = handleClaudePassThroughStream(c, resp, info)
	} else {
		usage, newAPIError = handleClaudePassThroughJSON(c, resp, info)
	}
	if newAPIError != nil {
		service.ResetStatusCode(newAPIError, statusCodeMappingStr)
		return newAPIError
	}

	service.PostClaudeConsumeQuota(c, info, usage)
	latencyMs := info.FirstResponseTime.Sub(info.StartTime).Milliseconds()
	logger.LogInfo(c, fmt.Sprintf("claude passthrough usage: prompt=%d completion=%d total=%d latency_ms=%d",
		usage.PromptTokens, usage.CompletionTokens, usage.TotalTokens, latencyMs))
	return nil
}

func handleClaudePassThroughJSON(c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo) (*dto.Usage, *types.NewAPIError) {
	defer service.CloseResponseBodyGracefully(resp)

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, types.NewError(err, types.ErrorCodeBadResponseBody, types.ErrOptionWithSkipRetry())
	}

	usage := &dto.Usage{}
	var claudeResp dto.ClaudeResponse
	if err := common.Unmarshal(body, &claudeResp); err == nil {
		updateClaudeUsageFromResponse(claudeResp, usage, info)
	}

	info.SetFirstResponseTime()
	service.IOCopyBytesGracefully(c, resp, body)
	return usage, nil
}

func handleClaudePassThroughStream(c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo) (*dto.Usage, *types.NewAPIError) {
	defer service.CloseResponseBodyGracefully(resp)

	copyUpstreamHeaders(c, resp.Header)
	c.Writer.WriteHeader(resp.StatusCode)

	flusher, _ := c.Writer.(http.Flusher)
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64<<10), 64<<20)

	usage := &dto.Usage{}
	firstChunk := true

	for scanner.Scan() {
		line := scanner.Text()
		if firstChunk {
			info.SetFirstResponseTime()
			logger.LogInfo(c, "claude passthrough stream: first chunk received")
			firstChunk = false
		}

		if _, err := c.Writer.Write([]byte(line + "\n")); err != nil {
			return nil, types.NewError(err, types.ErrorCodeBadResponseBody, types.ErrOptionWithSkipRetry())
		}
		if flusher != nil {
			flusher.Flush()
		}

		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "data:") {
			payload := strings.TrimSpace(strings.TrimPrefix(trimmed, "data:"))
			updateClaudeUsageFromPayload(payload, usage, info)
		}
	}

	if err := scanner.Err(); err != nil && err != io.EOF {
		return nil, types.NewError(err, types.ErrorCodeBadResponseBody, types.ErrOptionWithSkipRetry())
	}

	return usage, nil
}

func buildClaudePassThroughHeaders(c *gin.Context, info *relaycommon.RelayInfo) http.Header {
	headers := http.Header{}
	headers.Set("Content-Type", "application/json")
	anthropicVersion := c.Request.Header.Get("anthropic-version")
	if anthropicVersion == "" {
		anthropicVersion = "2023-06-01"
	}
	headers.Set("anthropic-version", anthropicVersion)
	headers.Set("x-api-key", info.ApiKey)
	if accept := c.Request.Header.Get("Accept"); accept != "" {
		headers.Set("Accept", accept)
	}
	return headers
}

func copyUpstreamHeaders(c *gin.Context, headers http.Header) {
	if headers == nil {
		return
	}
	for k, v := range headers {
		if k == "Content-Length" {
			continue
		}
		if len(v) > 0 {
			c.Writer.Header().Set(k, v[0])
		}
	}
}

func updateClaudeUsageFromPayload(payload string, usage *dto.Usage, info *relaycommon.RelayInfo) {
	if usage == nil {
		return
	}
	var claudeResp dto.ClaudeResponse
	if err := common.UnmarshalJsonStr(payload, &claudeResp); err != nil {
		return
	}
	updateClaudeUsageFromResponse(claudeResp, usage, info)
}

func updateClaudeUsageFromResponse(claudeResp dto.ClaudeResponse, usage *dto.Usage, info *relaycommon.RelayInfo) {
	if claudeResp.Model != "" {
		info.UpstreamModelName = claudeResp.Model
	}
	if claudeResp.Usage != nil {
		usage.PromptTokens = claudeResp.Usage.InputTokens
		usage.CompletionTokens = claudeResp.Usage.OutputTokens
		usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
		usage.PromptTokensDetails.CachedTokens = claudeResp.Usage.CacheReadInputTokens
		usage.PromptTokensDetails.CachedCreationTokens = claudeResp.Usage.CacheCreationInputTokens
		usage.ClaudeCacheCreation5mTokens = claudeResp.Usage.GetCacheCreation5mTokens()
		usage.ClaudeCacheCreation1hTokens = claudeResp.Usage.GetCacheCreation1hTokens()
	}
	if claudeResp.Message != nil {
		if claudeResp.Message.Model != "" {
			info.UpstreamModelName = claudeResp.Message.Model
		}
		if claudeResp.Message.Usage != nil {
			usage.PromptTokens = claudeResp.Message.Usage.InputTokens
			usage.CompletionTokens = claudeResp.Message.Usage.OutputTokens
			usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
			usage.PromptTokensDetails.CachedTokens = claudeResp.Message.Usage.CacheReadInputTokens
			usage.PromptTokensDetails.CachedCreationTokens = claudeResp.Message.Usage.CacheCreationInputTokens
			usage.ClaudeCacheCreation5mTokens = claudeResp.Message.Usage.GetCacheCreation5mTokens()
			usage.ClaudeCacheCreation1hTokens = claudeResp.Message.Usage.GetCacheCreation1hTokens()
		}
	}
}

func shouldClaudePassThrough(info *relaycommon.RelayInfo) bool {
	if info.ChannelSetting.ClaudePassThrough {
		return true
	}
	base := strings.TrimSuffix(strings.TrimSpace(info.ChannelBaseUrl), "/")
	//fmt.Println("===== Claude PassThrough Base URL =====", base)
	return base == claudePassthroughURL
}
