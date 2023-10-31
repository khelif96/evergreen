package thirdparty

import (
	"fmt"
	"math"
	"strings"

	"github.com/sashabaranov/go-openai"
)

func CalculateTokenUsage(tokens ...string) int {
	// 4 characters represent 1 token and tokens are rounded up to the nearest whole number
	return int(math.Ceil(float64(len(strings.Join(tokens, ""))) / 4))
}

func CalculateGPT3Cost(tokenCount int) float64 {
	// gpt3 costs $0.002 per 1000 tokens
	return float64(tokenCount) * 0.002 / 1000

}

func GetGPTSystemPromptsForResmokeLog(logLine string) []openai.ChatCompletionMessage {
	initialPrompt := openai.ChatCompletionMessage{
		Role:    openai.ChatMessageRoleSystem,
		Content: "You are ParsleyAI, a bot embedded in the Parsley Log viewer at MongoDB. Your job is to help users understand the logs they are viewing. You are a friendly bot, and you are here to help.",
	}
	resmokeDescriptionPrompt := openai.ChatCompletionMessage{
		Role:    openai.ChatMessageRoleSystem,
		Content: "You are about to be presented with a resmoke log. Resmoke is a format used to render MongoDB server logs. The format is as follows: [<resmoke_function_name>] <port>| <timeStamp> <shellPrefix> <configSrv> <pid> [<ctx>] \"<msg>\"",
	}
	resmokeAnswerPrompt := openai.ChatCompletionMessage{
		Role:    openai.ChatMessageRoleUser,
		Content: fmt.Sprintf("Using what you know about resmoke logs and the context of the log, please explain the components of the following log and what they mean.: %s", logLine),
	}

	return []openai.ChatCompletionMessage{initialPrompt, resmokeDescriptionPrompt, resmokeAnswerPrompt}

}
