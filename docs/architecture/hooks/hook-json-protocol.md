# Hook JSON-RPC Protocol Details

All hooks use `JSON-RPC 2.0` format, with one JSON message per line, transmitted via stdio.

---

## Basic Protocol Structure

### Request (MintClaw → Hook)

```json
{"jsonrpc":"2.0","id":1,"method":"hook.xxx","params":{...}}
```

### Response (Hook → MintClaw)

Success:
```json
{"jsonrpc":"2.0","id":1,"result":{...}}
```

Error:
```json
{"jsonrpc":"2.0","id":1,"error":{"code":-32000,"message":"error message"}}
```

---

## 1. `hook.hello` (Handshake)

Handshake must be completed at startup, otherwise the hook process will be terminated.

### Request

```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "method": "hook.hello",
  "params": {
    "name": "py_review_gate",
    "version": 1,
    "modes": ["observe", "tool", "approve"]
  }
}
```

| Field | Description |
|-------|-------------|
| `name` | hook name (from configuration) |
| `version` | protocol version, currently `1` |
| `modes` | capability modes supported by the hook |

### Response

```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "result": {
    "ok": true,
    "name": "python-review-gate"
  }
}
```

---

## 2. `hook.before_llm`

Triggered before sending a request to the LLM. A hook may select a model,
adjust allowed options, append tail context, or rewrite the current dynamic
tail. System prompts, provider tool definitions, frozen root-turn envelopes,
and completed transcript messages are runtime-owned and cannot be changed by
this hook.

### Request

```json
{
  "jsonrpc": "2.0",
  "id": 2,
  "method": "hook.before_llm",
  "params": {
    "meta": {
      "AgentID": "agent-1",
      "TurnID": "turn-1",
      "ParentTurnID": "",
      "SessionKey": "session-1",
      "Iteration": 0,
      "TracePath": "runTurn",
      "Source": "turn.llm.request"
    },
    "model": "claude-sonnet",
    "messages": [
      {"role": "user", "content": "hello"}
    ],
    "tools": [
      {
        "type": "function",
        "function": {
          "name": "echo",
          "description": "echo text",
          "parameters": {"type": "object"}
        }
      }
    ],
    "options": {
      "temperature": 0.7
    },
    "channel": "cli",
    "chat_id": "chat-1",
    "graceful_terminal": false
  }
}
```

| Field | Description |
|-------|-------------|
| `meta` | event metadata for tracing |
| `model` | requested model name |
| `messages` | conversation history |
| `tools` | list of available tool definitions |
| `options` | LLM parameters (temperature, max_tokens, etc.) |
| `channel` | request source channel |
| `chat_id` | session ID |

### Response (Tail Context Example)

```json
{
  "jsonrpc": "2.0",
  "id": 2,
  "result": {
    "action": "modify",
    "request": {
      "model": "claude-sonnet",
      "messages": [
        {"role": "user", "content": "hello"},
        {"role": "user", "content": "additional current-turn context"}
      ],
      "tools": [
        {
          "type": "function",
          "function": {
            "name": "echo",
            "description": "echo",
            "parameters": {}
          }
        }
      ]
    }
  }
}
```

| Field | Description |
|-------|-------------|
| `action` | decision action (see table below) |
| `request` | modified request object |

If a response changes a protected prefix message or a tool schema, MintClaw
keeps the prior messages or tools. Appending context after the existing request
does not rewrite the protected prefix.

---

## 3. `hook.after_llm`

Triggered after receiving LLM response. Can modify response content.

### Request

```json
{
  "jsonrpc": "2.0",
  "id": 3,
  "method": "hook.after_llm",
  "params": {
    "meta": {
      "AgentID": "agent-1",
      "TurnID": "turn-1",
      "SessionKey": "session-1"
    },
    "model": "claude-sonnet",
    "response": {
      "role": "assistant",
      "content": "Hi!",
      "tool_calls": [
        {
          "id": "tc-1",
          "type": "function",
          "function": {
            "name": "echo",
            "arguments": "{\"text\":\"hi\"}"
          }
        }
      ]
    },
    "channel": "cli",
    "chat_id": "chat-1"
  }
}
```

### Response

```json
{
  "jsonrpc": "2.0",
  "id": 3,
  "result": {
    "action": "continue"
  }
}
```

---

## 4. `hook.before_tool`

Triggered before tool execution. Can modify tool name and arguments, deny execution, or return result directly.

### Request

```json
{
  "jsonrpc": "2.0",
  "id": 4,
  "method": "hook.before_tool",
  "params": {
    "meta": {
      "AgentID": "agent-1",
      "TurnID": "turn-1",
      "SessionKey": "session-1"
    },
    "tool": "echo_text",
    "arguments": {
      "text": "hello"
    },
    "channel": "cli",
    "chat_id": "chat-1"
  }
}
```

| Field | Description |
|-------|-------------|
| `tool` | tool name |
| `arguments` | tool arguments |

### Response (Modify Arguments)

```json
{
  "jsonrpc": "2.0",
  "id": 4,
  "result": {
    "action": "modify",
    "call": {
      "tool": "echo_text",
      "arguments": {
        "text": "modified hello"
      }
    }
  }
}
```

### Response (Deny Execution)

```json
{
  "jsonrpc": "2.0",
  "id": 4,
  "result": {
    "action": "deny_tool",
    "reason": "Invalid arguments"
  }
}
```

### Response (Return Result Directly - respond)

```json
{
  "jsonrpc": "2.0",
  "id": 4,
  "result": {
    "action": "respond",
    "call": {
      "tool": "my_plugin_tool",
      "arguments": {
        "query": "hello"
      }
    },
    "result": {
      "for_llm": "Plugin tool executed successfully",
      "for_user": "",
      "silent": false,
      "is_error": false
    }
  }
}
```

The `respond` action allows hooks to return tool results directly for an already
registered and admitted tool call, skipping actual tool execution. Use cases:
1. **External tool execution**: A trusted hook can execute an admitted tool through an external service
2. **Tool result caching**: Return cached results for repeated calls
3. **Tool mocking**: Return mock results during testing

| Field | Description |
|-------|-------------|
| `action` | must be `respond` |
| `call` | modified call information (optional) |
| `result` | tool result to return directly |

---

## 5. `hook.after_tool`

Triggered after tool execution completes. Can modify the result returned to LLM.

### Request

```json
{
  "jsonrpc": "2.0",
  "id": 5,
  "method": "hook.after_tool",
  "params": {
    "meta": {
      "AgentID": "agent-1",
      "TurnID": "turn-1",
      "SessionKey": "session-1"
    },
    "tool": "echo_text",
    "arguments": {
      "text": "hello"
    },
    "result": {
      "for_llm": "echoed: hello",
      "for_user": "",
      "silent": false,
      "is_error": false,
      "async": false,
      "media": [],
      "artifact_tags": [],
      "response_handled": false
    },
    "duration": 15000000,
    "channel": "cli",
    "chat_id": "chat-1"
  }
}
```

| Field | Description |
|-------|-------------|
| `result.for_llm` | content returned to LLM |
| `result.for_user` | content sent to user |
| `result.silent` | whether silent (not sent to user) |
| `result.is_error` | whether it's an error |
| `result.async` | whether executed asynchronously |
| `result.media` | list of media references |
| `result.artifact_tags` | local artifact path tags |
| `result.response_handled` | whether response has been handled |
| `duration` | execution time (nanoseconds) |

### Response

```json
{
  "jsonrpc": "2.0",
  "id": 5,
  "result": {
    "action": "continue"
  }
}
```

---

## 6. `hook.approve_tool`

Approval hook for deciding whether to allow execution of sensitive tools.

### Request

```json
{
  "jsonrpc": "2.0",
  "id": 6,
  "method": "hook.approve_tool",
  "params": {
    "meta": {
      "AgentID": "agent-1",
      "TurnID": "turn-1",
      "SessionKey": "session-1"
    },
    "tool": "bash",
    "arguments": {
      "command": "rm -rf /"
    },
    "channel": "cli",
    "chat_id": "chat-1"
  }
}
```

### Response (Approved)

```json
{
  "jsonrpc": "2.0",
  "id": 6,
  "result": {
    "approved": true
  }
}
```

### Response (Denied)

```json
{
  "jsonrpc": "2.0",
  "id": 6,
  "result": {
    "approved": false,
    "reason": "Dangerous command, execution denied"
  }
}
```

### Response (Human Approval Required)

```json
{
  "jsonrpc": "2.0",
  "id": 6,
  "result": {
    "require_human": true,
    "action_summary": "Delete the production cache namespace",
    "timeout_seconds": 3600
  }
}
```

`action_summary` is required when `require_human` is true. It is trusted,
action-specific presentation data produced by the policy hook and must not
contain secrets. MintClaw displays it with the runtime-owned tool name; it does
not render arbitrary tool arguments. Runtime separately binds approval to the
exact canonical arguments and revalidates policy before one-time execution.

---

## 7. `hook.runtime_event` (notification)

Runtime observer event, broadcast only, no response required. `id` is `0` or absent.

```json
{
  "jsonrpc": "2.0",
  "method": "hook.runtime_event",
  "params": {
    "kind": "agent.tool.exec_start",
    "source": {
      "component": "agent",
      "name": "agent-1"
    },
    "scope": {
      "agent_id": "agent-1",
      "session_key": "session-1",
      "turn_id": "turn-1",
      "channel": "cli",
      "chat_id": "chat-1"
    },
    "payload": {
      "Tool": "echo_text",
      "Arguments": {"text": "hello"}
    }
  }
}
```

Common `Kind` values:
- `agent.turn.start` / `agent.turn.end`
- `agent.llm.request` / `agent.llm.response`
- `agent.tool.exec_start` / `agent.tool.exec_end` / `agent.tool.exec_skipped` / `agent.tool.loop_decision`
- `agent.steering.injected`
- `agent.interrupt.received`
- `agent.error`

Legacy observe configuration names such as `turn_end` and `tool_exec_start` are still accepted and normalized to runtime event names. New process hook notifications use `hook.runtime_event`.

---

## Action Options

| action | Applicable hooks | Effect |
|--------|-----------------|--------|
| `continue` | All interceptor types | Pass through without modification |
| `modify` | `before_llm`, `before_tool`, `after_llm`, `after_tool` | Modify request/response and pass through |
| `respond` | `before_tool` | Return tool result directly, skip actual execution. **Note: AfterTool is NOT called (design decision - respond provides final answer).** |
| `deny_tool` | `before_tool` | Deny tool execution |
| `abort_turn` | All interceptor types | Abort current turn, return error |
| `hard_abort` | All interceptor types | Force stop entire agent loop |

---

## Complete Flow Example

```json
{"jsonrpc":"2.0","id":1,"method":"hook.hello","params":{"name":"my_hook","version":1,"modes":["tool","approve"]}}
{"jsonrpc":"2.0","id":1,"result":{"ok":true,"name":"my_hook"}}
{"jsonrpc":"2.0","id":2,"method":"hook.before_llm","params":{"model":"claude-sonnet","messages":[{"role":"user","content":"hello"}],"tools":[]}}
{"jsonrpc":"2.0","id":2,"result":{"action":"continue"}}
{"jsonrpc":"2.0","id":3,"method":"hook.before_tool","params":{"tool":"bash","arguments":{"command":"ls"}}}
{"jsonrpc":"2.0","id":3,"result":{"action":"continue"}}
{"jsonrpc":"2.0","id":4,"method":"hook.approve_tool","params":{"tool":"bash","arguments":{"command":"ls"}}}
{"jsonrpc":"2.0","id":4,"result":{"approved":true}}
{"jsonrpc":"2.0","id":5,"method":"hook.after_tool","params":{"tool":"bash","arguments":{"command":"ls"},"result":{"for_llm":"file1.txt\nfile2.txt"},"duration":5000000}}
{"jsonrpc":"2.0","id":5,"result":{"action":"continue"}}
{"jsonrpc":"2.0","id":6,"method":"hook.after_llm","params":{"model":"claude-sonnet","response":{"role":"assistant","content":"Files listed"}}}
{"jsonrpc":"2.0","id":6,"result":{"action":"continue"}}
```

---

## Tool Definitions And `before_tool` Responses

`before_llm` cannot add or rewrite provider tool definitions. Tools must be
registered through MintClaw's capability registry so that prompt visibility,
turn-profile filtering, execution, and approval all refer to the same typed
capability. A `before_tool` hook may still return `respond` for an already
registered and admitted tool call; it cannot make an unregistered tool callable
by injecting a schema into the LLM request.
