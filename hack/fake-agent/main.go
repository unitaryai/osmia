// fake-agent is a minimal test double for AI coding agents used in E2E workflow
// tests. It reads the OSMIA_SCENARIO environment variable and emits a
// pre-scripted sequence of NDJSON events to stdout, then exits with the
// appropriate exit code.
//
// The 50ms inter-event delay ensures that the stream reader can process
// events before the pod exits. The "thrash" scenario loops indefinitely with
// slower 2500ms delays so the watchdog has time to fire and terminate the task.
package main

import (
	"fmt"
	"os"
	"time"
)

func main() {
	scenario := os.Getenv("OSMIA_SCENARIO")
	if scenario == "" {
		scenario = "success"
	}

	switch scenario {
	case "success":
		runSuccess()
	case "loop":
		runLoop()
	case "thrash":
		runThrash()
	case "fail":
		runFail()
	case "tournament_a":
		runTournamentA()
	case "tournament_b":
		runTournamentB()
	case "judge":
		runJudge()
	default:
		runSuccess()
	}
}

func emit(line string) {
	fmt.Println(line)
}

func delay(d time.Duration) {
	time.Sleep(d)
}

// runSuccess emits 3 tool calls, a cost update, and a successful result.
func runSuccess() {
	delay(50 * time.Millisecond)
	emit(`{"type":"tool_use","tool":"Bash","args":{"command":"make test"}}`)
	delay(50 * time.Millisecond)
	emit(`{"type":"tool_use","tool":"Bash","args":{"command":"go build ./..."}}`)
	delay(50 * time.Millisecond)
	emit(`{"type":"tool_use","tool":"Bash","args":{"command":"git status"}}`)
	delay(50 * time.Millisecond)
	emit(`{"type":"cost","input_tokens":500,"output_tokens":100,"cost_usd":0.04}`)
	delay(50 * time.Millisecond)
	emit(`{"type":"result","success":true,"summary":"Fixed the bug"}`)
	delay(50 * time.Millisecond)
	os.Exit(0)
}

// runLoop emits 12 identical tool calls (triggering PRM loop detection)
// followed by a successful result.
func runLoop() {
	for i := 0; i < 12; i++ {
		delay(50 * time.Millisecond)
		emit(`{"type":"tool_use","tool":"Bash","args":{"command":"go test ./..."}}`)
	}
	delay(50 * time.Millisecond)
	emit(`{"type":"result","success":true,"summary":"Done"}`)
	delay(50 * time.Millisecond)
	os.Exit(0)
}

// runThrash emits cost events with increasing cumulative token counts every
// 2500ms and never exits. This allows the watchdog thrashing detector to fire
// before the pod terminates, triggering a forced task termination.
func runThrash() {
	for i := 1; ; i++ {
		inputTokens := i * 60000
		outputTokens := i * 20000
		costUSD := float64(i) * 3.0
		emit(fmt.Sprintf(
			`{"type":"cost","input_tokens":%d,"output_tokens":%d,"cost_usd":%.1f}`,
			inputTokens, outputTokens, costUSD,
		))
		delay(2500 * time.Millisecond)
	}
}

// runFail emits 2 tool calls and a failed result, then exits non-zero.
func runFail() {
	delay(50 * time.Millisecond)
	emit(`{"type":"tool_use","tool":"Bash","args":{"command":"make test"}}`)
	delay(50 * time.Millisecond)
	emit(`{"type":"tool_use","tool":"Read","args":{"path":"/workspace/src"}}`)
	delay(50 * time.Millisecond)
	emit(`{"type":"result","success":false,"summary":"Wrong approach: tried to modify tests instead of source"}`)
	delay(50 * time.Millisecond)
	os.Exit(1)
}

// runTournamentA emits 3 tool calls and a clean-implementation result.
func runTournamentA() {
	delay(50 * time.Millisecond)
	emit(`{"type":"tool_use","tool":"Bash","args":{"command":"make test"}}`)
	delay(50 * time.Millisecond)
	emit(`{"type":"tool_use","tool":"Read","args":{"path":"/workspace"}}`)
	delay(50 * time.Millisecond)
	emit(`{"type":"tool_use","tool":"Write","args":{"path":"/workspace/solution.go"}}`)
	delay(50 * time.Millisecond)
	emit(`{"type":"result","success":true,"summary":"Clean implementation using interfaces"}`)
	delay(50 * time.Millisecond)
	os.Exit(0)
}

// runTournamentB emits 2 tool calls and a quick-but-duplicated result.
func runTournamentB() {
	delay(50 * time.Millisecond)
	emit(`{"type":"tool_use","tool":"Bash","args":{"command":"make test"}}`)
	delay(50 * time.Millisecond)
	emit(`{"type":"tool_use","tool":"Write","args":{"path":"/workspace/solution.go"}}`)
	delay(50 * time.Millisecond)
	emit(`{"type":"result","success":true,"summary":"Quick implementation with duplication"}`)
	delay(50 * time.Millisecond)
	os.Exit(0)
}

// runJudge emits a single Read tool call and a JSON-embedded winner result.
// The summary embeds a JSON object so handleJudgeComplete can extract the
// winner index (the first { ... } block).
func runJudge() {
	delay(50 * time.Millisecond)
	emit(`{"type":"tool_use","tool":"Read","args":{"path":"/workspace"}}`)
	delay(50 * time.Millisecond)
	emit(`{"type":"result","success":true,"summary":"{\"winner_index\":0,\"reasoning\":\"candidate 0 provided the cleaner solution\"}"}`)
	delay(50 * time.Millisecond)
	os.Exit(0)
}
