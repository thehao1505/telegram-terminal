// fakeclaude giả lập giao thức stream-json của Claude Code để test bot mà không
// cần gọi API thật: bắt tay initialize, stream vài delta chữ, gọi 1 tool, xin
// quyền, rồi kết thúc lượt. Quyết định nhận được ghi ra file TT_FAKE_DECISION.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

func out(v any) {
	data, _ := json.Marshal(v)
	fmt.Println(string(data))
}

// turns đếm số lượt để total_cost_usd cộng dồn giống CLI thật.
var turns int

func main() {
	// Ghi lại args để test kiểm tra bot có truyền --resume … hay không.
	if f := os.Getenv("TT_FAKE_ARGS"); f != "" {
		os.WriteFile(f, []byte(strings.Join(os.Args[1:], " ")), 0o600)
	}
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 1024*1024), 8*1024*1024)
	for sc.Scan() {
		var m map[string]json.RawMessage
		if json.Unmarshal(sc.Bytes(), &m) != nil {
			continue
		}
		var typ string
		json.Unmarshal(m["type"], &typ)

		switch typ {
		case "control_request":
			var reqID string
			json.Unmarshal(m["request_id"], &reqID)
			var req struct {
				Subtype string `json:"subtype"`
				Mode    string `json:"mode"`
			}
			json.Unmarshal(m["request"], &req)

			if req.Subtype == "set_permission_mode" {
				// Ghi lại các mode nhận được để test kiểm tra.
				if f := os.Getenv("TT_FAKE_MODES"); f != "" {
					fh, err := os.OpenFile(f, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
					if err == nil {
						fmt.Fprintln(fh, req.Mode)
						fh.Close()
					}
				}
				// Giống CLI thật: bypassPermissions bị từ chối nếu không khởi
				// động kèm --dangerously-skip-permissions.
				if req.Mode == "bypassPermissions" {
					out(map[string]any{"type": "control_response", "response": map[string]any{
						"subtype": "error", "request_id": reqID,
						"error": "Cannot set permission mode to bypassPermissions because the session was not launched with --dangerously-skip-permissions",
					}})
					continue
				}
				out(map[string]any{"type": "control_response", "response": map[string]any{
					"subtype": "success", "request_id": reqID,
					"response": map[string]any{"mode": req.Mode},
				}})
				continue
			}
			resp := map[string]any{}
			if req.Subtype == "initialize" {
				resp["current_permission_mode"] = wireMode(flagValue("--permission-mode"))
			}
			out(map[string]any{"type": "control_response", "response": map[string]any{
				"subtype": "success", "request_id": reqID, "response": resp,
			}})

		case "user":
			// Ghi lại nguyên văn message để test kiểm tra content block (ảnh…).
			if f := os.Getenv("TT_FAKE_MESSAGE"); f != "" {
				os.WriteFile(f, m["message"], 0o600)
			}
			out(map[string]any{"type": "system", "subtype": "init",
				"session_id": "sess-123", "model": "fake-model"})
			for _, t := range []string{"Xin ", "chào ", "bạn"} {
				out(map[string]any{"type": "stream_event", "event": map[string]any{
					"type": "content_block_delta", "index": 0,
					"delta": map[string]any{"type": "text_delta", "text": t},
				}})
			}
			// TT_FAKE_ASKQ: gọi AskUserQuestion thay vì Bash. "one" = 1 câu
			// chọn-một; giá trị khác = 2 câu, câu sau cho chọn nhiều.
			if mode := os.Getenv("TT_FAKE_ASKQ"); mode != "" {
				askQuestions(mode)
				continue
			}
			// TT_FAKE_NOASK: kết thúc lượt ngay, không gọi tool / không xin quyền.
			if os.Getenv("TT_FAKE_NOASK") != "" {
				out(map[string]any{"type": "result", "subtype": "success", "is_error": false,
					"duration_ms": 12, "result": "xong"})
				continue
			}
			out(map[string]any{"type": "assistant", "message": map[string]any{
				"role": "assistant",
				"content": []any{map[string]any{"type": "tool_use", "id": "tu_1", "name": "Bash",
					"input": map[string]any{"command": "ls -la", "description": "liệt kê file"}}},
			}})
			out(map[string]any{"type": "control_request", "request_id": "req-1",
				"request": map[string]any{
					"subtype": "can_use_tool", "tool_name": "Bash", "tool_use_id": "tu_1",
					"input":                  map[string]any{"command": "ls -la"},
					"permission_suggestions": []any{map[string]any{"type": "setMode", "mode": "acceptEdits", "destination": "session"}},
				}})

		case "control_response":
			if f := os.Getenv("TT_FAKE_DECISION"); f != "" {
				os.WriteFile(f, m["response"], 0o600)
			}
			turns++
			out(map[string]any{"type": "result", "subtype": "success", "is_error": false,
				"duration_ms": 1234, "num_turns": 2,
				// Giống CLI thật: đây là TỔNG cộng dồn của cả phiên.
				"total_cost_usd": 0.0042 * float64(turns), "result": "xong"})
		}
	}
}

// askQuestions phát 1 lượt AskUserQuestion: tool_use rồi can_use_tool.
func askQuestions(mode string) {
	questions := []any{map[string]any{
		"question":    "Dùng thư viện nào?",
		"header":      "Thư viện",
		"multiSelect": false,
		"options": []any{
			map[string]any{"label": "stdlib", "description": "không thêm phụ thuộc"},
			map[string]any{"label": "cobra", "description": "nhiều tính năng hơn"},
		},
	}}
	if mode != "one" {
		questions = append(questions, map[string]any{
			"question":    "Bật thêm phần nào?",
			"header":      "Tính năng",
			"multiSelect": true,
			"options": []any{
				map[string]any{"label": "log", "description": "ghi log ra file"},
				map[string]any{"label": "metrics", "description": "đếm số liệu"},
				map[string]any{"label": "tracing", "description": "vết gọi hàm"},
			},
		})
	}
	input := map[string]any{"questions": questions}
	out(map[string]any{"type": "assistant", "message": map[string]any{
		"role": "assistant",
		"content": []any{map[string]any{"type": "tool_use", "id": "tu_q",
			"name": "AskUserQuestion", "input": input}},
	}})
	out(map[string]any{"type": "control_request", "request_id": "req-q",
		"request": map[string]any{
			"subtype": "can_use_tool", "tool_name": "AskUserQuestion",
			"tool_use_id": "tu_q", "input": input,
		}})
}

// flagValue lấy giá trị của 1 cờ trong os.Args.
func flagValue(name string) string {
	for i, a := range os.Args {
		if a == name && i+1 < len(os.Args) {
			return os.Args[i+1]
		}
	}
	return ""
}

// wireMode: trên dây, chế độ mặc định tên là "default" (cờ CLI gọi "manual").
func wireMode(m string) string {
	if m == "manual" || m == "" {
		return "default"
	}
	return m
}
