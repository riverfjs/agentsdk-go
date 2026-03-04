# Permissions V2 (Breaking): DSL-First with Independent Sandbox

This document defines a **breaking** permissions redesign:
- Permissions move to **DSL-only** configuration.
- Sandbox remains a **separate, independent** config block.
- Legacy permission formats are rejected.

---

## 1) Breaking Contract

1. `permissions.dsl` is the only rule source.
2. Legacy `permissions.allow/ask/deny` is invalid.
3. Legacy pattern strings like `Bash(ls:*)` are invalid.
4. Default unmatched action is explicitly configured in `permissions.default`.
5. Rule resolution priority remains: `deny > ask > allow > default`.

---

## 2) Settings Shape

Permissions and sandbox are independent:

```json
{
  "permissions": {
    "default": "deny",
    "dsl": [
      "allow Bash git status|diff|log|add",
      "ask Bash git commit|push|rebase",
      "deny Bash git push --force|--force-with-lease",
      "allow Read /Users/fanjinsong/.aevitas/**",
      "allow Glob /Users/fanjinsong/.aevitas/**"
    ]
  },
  "sandbox": {
    "enabled": true,
    "autoAllowBashIfSandboxed": false,
    "additionalDirectories": [
      "/Users/fanjinsong/.aevitas"
    ]
  }
}
```

Notes:
- `permissions` controls **authorization decisions**.
- `sandbox` controls **runtime execution boundaries**.
- Neither section implicitly rewrites the other.
- `sandbox.additionalDirectories` is a filesystem allowlist used by built-in file tools (`Read/Write/Edit/Glob/Grep/Bash`).
- `sandbox.additionalDirectories` does **not** expand `~`; use absolute paths only.

---

## 3) Tool Names (Strict)

Tool name validation uses the actual registered tool names from runtime registry.
Input must match registered names exactly (case-sensitive).

- `Bash`, `Read`, `Write`, `Edit`, `Glob`, `Grep`
- `WebFetch`, `WebSearch`
- `Skill`, `SlashCommand`, `Task`
- `MemorySearch`, `MemoryGet`, `MemoryWrite`, `ListSkills`
- `SendFile`, `AskUserQuestion`, `TodoWrite`
- `BashOutput`, `BashStatus`, `KillTask`, `TaskCreate`, `TaskList`, `TaskGet`, `TaskUpdate`

---

## 4) DSL Grammar (Draft)

Each line has shape:

```text
<effect> <Tool> <expression>
```

Where:
- `<effect>` is one of `allow|ask|deny`
- `<Tool>` is an exact registered tool name (case-sensitive)
- `<expression>` is tool-specific

### Bash expressions

Examples:

```text
allow Bash git status|diff|log|add
ask Bash git commit|push|rebase
deny Bash git push --force|--force-with-lease
allow Bash ls|pwd
```

Semantics:
- Parse shell command using AST parser.
- Match command/subcommand/flags against expression terms.
- No string-substring fallback.

### Path tool expressions (Read/Write/Edit/Glob/Grep)

Examples:

```text
allow Read /Users/fanjinsong/.aevitas/**
allow Glob /Users/fanjinsong/.aevitas/**
deny Write /Users/fanjinsong/.aevitas/config.json
```

Path normalization requirements:
- convert to absolute path
- resolve symlink before final decision

### Web expressions

Examples:

```text
allow WebFetch domain:docs.anthropic.com
deny WebFetch domain:*
```

---

## 5) Internal Compilation

DSL lines compile to internal AST rules:

- `effect`
- `tool`
- `matcher.kind` (`bash_ast`, `path_query`, `web_query`, ...)
- optional normalized match payload

Compilation is strict:
- parse errors fail runtime init after tool registration
- unknown tools fail runtime init after tool registration
- ambiguous expressions fail config load

---

## 6) Evaluation Algorithm

For each call:
1. Normalize tool name.
2. Build normalized target (Bash AST or path/domain projection).
3. Evaluate all matching rules.
4. Apply first decision by priority (`deny > ask > allow`).
5. If none matched, apply `permissions.default`.

Decision metadata should include:
- matched DSL line
- normalized target summary
- final effect

---

## 7) Migration (Breaking)

On startup:
- If `permissions.allow/ask/deny` exists -> fail with migration error.
- If `permissions.dsl` missing and permissions configured -> fail.
- After tools are registered, validate each DSL rule tool token against
  the registry name set (exact/case-sensitive match). Unknown tool names fail startup.
- Error message should show:
  - unsupported field
  - expected new field
  - one concrete DSL rewrite example

No auto-migration in runtime path.

---

## 8) Required Test Matrix

1. DSL parser:
   - valid/invalid lines
   - tool normalization
2. Bash AST matching:
   - quoted args, escaped spaces, pipelines, redirections
3. Path rules:
   - no `~` expansion, symlink escape, absolute normalization
4. Priority:
   - deny over ask, ask over allow
5. Default action:
   - unmatched follows `permissions.default`
6. Independence:
   - sandbox config changes do not alter permission rule parsing behavior

---

## 9) Implementation Phases

Phase 1:
- Add `permissions.default/dsl` schema and strict validation.
- Implement DSL parser + internal rule compiler.

Phase 2:
- Implement unified evaluator and wire to tool execution path.
- Remove legacy matcher and legacy config fields.

Phase 3:
- Update docs/examples and provide migration guide snippets.
